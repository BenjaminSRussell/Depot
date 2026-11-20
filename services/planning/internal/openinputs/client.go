package openinputs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

const (
	PrincipalName   = "planning-open-inputs"
	DutySalesLines  = "sales.order.open-lines"
	DutyOnHand      = "inventory.read"
	DefaultTimeout  = 10 * time.Second
	salesAudience   = "sales"
	inventoryAud    = "inventory"
	salesLinesPath  = "/v1/sales/open-order-lines"
	onHandPath      = "/v1/inventory/onhand"
	pageSize        = 200
	maxLines        = 100000
	maxResponseSize = 8 << 20
	dateLayout      = "2006-01-02"
)

var ErrUnavailable = errors.New("open inputs unavailable")

type Client struct {
	salesURL     string
	inventoryURL string
	signer       *tenant.ServiceSigner
	http         *http.Client
}

type SalesLine struct {
	Ref       string
	ProductID string
	Date      string
	Qty       decimal.Decimal
	Unit      string
}

type OnHandQty struct {
	Qty  decimal.Decimal
	Unit string
}

func New(salesURL, inventoryURL string, signer *tenant.ServiceSigner, timeout time.Duration) (*Client, error) {
	if salesURL == "" || inventoryURL == "" {
		return nil, errors.New("openinputs: SALES_URL and INVENTORY_URL are required")
	}
	if signer == nil {
		return nil, errors.New("openinputs: a service signing key is required")
	}
	hc := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{salesURL: strings.TrimRight(salesURL, "/"), inventoryURL: strings.TrimRight(inventoryURL, "/"), signer: signer, http: hc}, nil
}

func FromEnv(getenv func(string) string) (*Client, error) {
	salesURL, inventoryURL := getenv("SALES_URL"), getenv("INVENTORY_URL")
	if salesURL == "" || inventoryURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("openinputs: SALES_URL and INVENTORY_URL need DEPOT_SERVICE_SIGNING_KEY or DEPOT_SERVICE_SIGNING_KEY_FILE")
	}
	return New(salesURL, inventoryURL, signer, DefaultTimeout)
}

type salesPage struct {
	Items []struct {
		OrderNumber string `json:"orderNumber"`
		LineNo      int    `json:"lineNo"`
		ProductID   string `json:"productId"`
		OpenQty     string `json:"openQty"`
		Unit        string `json:"unit"`
		DueDate     string `json:"dueDate"`
	} `json:"items"`
	Next string `json:"next"`
}

func (c *Client) OpenSalesLines(ctx context.Context, tenantID, legalEntityID, siteID, dueTo string) ([]SalesLine, error) {
	var out []SalesLine
	cursor := ""
	for {
		q := url.Values{"legalEntityId": {legalEntityID}, "siteId": {siteID}, "limit": {strconv.Itoa(pageSize)}}
		if dueTo != "" {
			q.Set("dueTo", dueTo)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		raw, err := c.get(ctx, c.salesURL, salesLinesPath, q, salesAudience, DutySalesLines, tenantID, legalEntityID)
		if err != nil {
			return nil, err
		}
		var page salesPage
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, refused(ctx, salesLinesPath, "unreadable body", err)
		}
		for _, it := range page.Items {
			line, err := toSalesLine(it.OrderNumber, it.LineNo, it.ProductID, it.OpenQty, it.Unit, it.DueDate)
			if err != nil {
				return nil, refused(ctx, salesLinesPath, "unusable line", err)
			}
			out = append(out, line)
		}
		if len(out) > maxLines {
			return nil, refused(ctx, salesLinesPath, "too many open lines", fmt.Errorf("more than %d lines", maxLines))
		}
		if page.Next == "" {
			return out, nil
		}
		if page.Next == cursor || len(page.Items) == 0 {
			return nil, refused(ctx, salesLinesPath, "cursor does not advance", errors.New(page.Next))
		}
		cursor = page.Next
	}
}

func normalizeUnit(unit string) string { return strings.ToUpper(strings.TrimSpace(unit)) }

func toSalesLine(orderNumber string, lineNo int, productID, openQty, unit, dueDate string) (SalesLine, error) {
	product, ok := ids.Canonical(productID)
	qty, err := decimal.NewFromString(openQty)
	unit = normalizeUnit(unit)
	if _, derr := time.Parse(dateLayout, dueDate); !ok || err != nil || derr != nil || !qty.IsPositive() || orderNumber == "" || lineNo < 1 || unit == "" {
		return SalesLine{}, fmt.Errorf("line %s/%d product %q qty %q unit %q date %q", orderNumber, lineNo, productID, openQty, unit, dueDate)
	}
	return SalesLine{Ref: orderNumber + "/" + strconv.Itoa(lineNo), ProductID: product, Date: dueDate, Qty: qty, Unit: unit}, nil
}

type onHandBody struct {
	Items []struct {
		Group          map[string]string `json:"group"`
		PhysicalInvent string            `json:"physicalInvent"`
		Unit           string            `json:"unit"`
	} `json:"items"`
	Truncated bool `json:"truncated"`
}

func (c *Client) OnHand(ctx context.Context, tenantID, legalEntityID, siteID string) (map[string]OnHandQty, error) {
	raw, err := c.get(ctx, c.inventoryURL, onHandPath, url.Values{"site": {siteID}, "groupBy": {"product"}}, inventoryAud, DutyOnHand, tenantID, legalEntityID)
	if err != nil {
		return nil, err
	}
	var body onHandBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, refused(ctx, onHandPath, "unreadable body", err)
	}
	if body.Truncated {
		return nil, refused(ctx, onHandPath, "answer truncated", errors.New("on-hand answer is cut"))
	}
	out := make(map[string]OnHandQty, len(body.Items))
	for _, it := range body.Items {
		product, ok := ids.Canonical(it.Group["product"])
		qty, err := decimal.NewFromString(it.PhysicalInvent)
		unit := normalizeUnit(it.Unit)
		if !ok || err != nil || unit == "" {
			return nil, refused(ctx, onHandPath, "unusable row", fmt.Errorf("product %q physicalInvent %q unit %q", it.Group["product"], it.PhysicalInvent, unit))
		}
		prior, seen := out[product]
		if seen && prior.Unit != unit {
			return nil, refused(ctx, onHandPath, "one product in two units", fmt.Errorf("product %q units %q and %q", product, prior.Unit, unit))
		}
		out[product] = OnHandQty{Qty: prior.Qty.Add(qty), Unit: unit}
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, base, path string, query url.Values, audience, duty, tenantID, legalEntityID string) ([]byte, error) {
	target := base + path
	principal, err := tenant.ServicePrincipal(PrincipalName, tenantID, legalEntityID)
	if err != nil {
		return nil, refused(ctx, path, "no principal", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target+"?"+query.Encode(), nil)
	if err != nil {
		return nil, refused(ctx, path, "bad request", err)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{duty}); err != nil {
		return nil, refused(ctx, path, "cannot sign", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, refused(ctx, path, "request failed", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil || len(raw) > maxResponseSize {
		return nil, refused(ctx, path, "response unreadable", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, refused(ctx, path, "refused with status "+strconv.Itoa(resp.StatusCode), nil)
	}
	return raw, nil
}

func refused(ctx context.Context, path, what string, err error) error {
	slog.ErrorContext(ctx, "planning live input refused", "what", what, "err", err, "path", path, "correlationId", middleware.GetReqID(ctx))
	return fmt.Errorf("%w: %s %s", ErrUnavailable, path, what)
}
