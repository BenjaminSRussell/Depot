package productblock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

const (
	PurchaseRequisition = "purchase_requisition"
	PurchaseOrder       = "purchase_order"
	SalesQuotation      = "sales_quotation"
	SalesOrder          = "sales_order"

	DefaultTTL      = 15 * time.Second
	Timeout         = 3 * time.Second
	CodeBlocked     = "PRD-LC-BLOCKED"
	CodeUnavailable = "PRD-POLICY-UNAVAILABLE"

	audience        = "product"
	dutyRead        = "product.read"
	maxResponseSize = 1 << 20
	maxCacheEntries = 10000
)

type Checker interface {
	Check(ctx context.Context, legalEntityID, productID, process string) error
}

type verdict struct {
	blockedDetail string
	blocked       bool
	until         time.Time
}

type Client struct {
	baseURL string
	http    *http.Client
	sign    func(req *http.Request, p tenant.Principal, legalEntityID string) error
	ttl     time.Duration
	now     func() time.Time
	mu      sync.Mutex
	cache   map[string]verdict
}

func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("product block check needs a product base URL")
	}
	if signer == nil {
		return nil, errors.New("product block check needs a service signing key")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	sign := func(req *http.Request, p tenant.Principal, legalEntityID string) error {
		_, err := signer.ApplyDuties(req, p, audience, legalEntityID, []string{dutyRead})
		return err
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient, sign: sign, ttl: DefaultTTL, now: time.Now, cache: map[string]verdict{}}, nil
}

func FromEnv(getenv func(string) string) (Checker, error) {
	baseURL := getenv("PRODUCT_URL")
	if baseURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("PRODUCT_URL needs DEPOT_SERVICE_SIGNING_KEY or DEPOT_SERVICE_SIGNING_KEY_FILE")
	}
	c, err := New(baseURL, signer, nil)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func Blocked(detail string) *httpx.Problem {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeBlocked, detail)
}

func unavailable() *httpx.Problem {
	return httpx.NewProblem(http.StatusServiceUnavailable, CodeUnavailable, "The product block check is not available right now. Try again shortly.")
}

func (c *Client) Check(ctx context.Context, legalEntityID, productID, process string) error {
	if process == "" {
		return errors.New("product block check: process is required")
	}
	if !ids.Valid(productID) || !ids.Valid(legalEntityID) {
		return nil
	}
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		slog.ErrorContext(ctx, "product block check without a principal", "correlationId", middleware.GetReqID(ctx))
		return unavailable()
	}
	key := strings.Join([]string{principal.TenantID, legalEntityID, productID, process}, "|")
	if v, hit := c.cached(key); hit {
		return v.err()
	}
	v, err := c.ask(ctx, principal, legalEntityID, productID, process)
	if err != nil {
		slog.ErrorContext(ctx, "product block check failed", "err", err, "productId", productID, "process", process, "correlationId", middleware.GetReqID(ctx))
		return unavailable()
	}
	c.store(key, v)
	return v.err()
}

func (v verdict) err() error {
	if v.blocked {
		return Blocked(v.blockedDetail)
	}
	return nil
}

func (c *Client) cached(key string) (verdict, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.cache[key]
	if !ok || !c.now().Before(v.until) {
		return verdict{}, false
	}
	return v, true
}

func (c *Client) store(key string, v verdict) {
	now := c.now()
	v.until = now.Add(c.ttl)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cache) >= maxCacheEntries {
		for k, old := range c.cache {
			if !now.Before(old.until) {
				delete(c.cache, k)
			}
		}
		if len(c.cache) >= maxCacheEntries {
			c.cache = map[string]verdict{}
		}
	}
	c.cache[key] = v
}

func (c *Client) ask(ctx context.Context, p tenant.Principal, legalEntityID, productID, process string) (verdict, error) {
	query := url.Values{"legalEntityId": {legalEntityID}, "process": {process}}
	target := c.baseURL + "/v1/product/products/" + url.PathEscape(productID) + "/process-policy?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return verdict{}, err
	}
	req.Header.Set("Accept", "application/json")
	if err := c.sign(req, p, legalEntityID); err != nil {
		return verdict{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return verdict{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return verdict{}, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		var out struct {
			Policy string `json:"policy"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return verdict{}, fmt.Errorf("unreadable policy answer: %w", err)
		}
		return verdict{}, nil
	case http.StatusNotFound:
		return verdict{}, nil
	case http.StatusUnprocessableEntity:
		var problem struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(body, &problem); err != nil || problem.Code != CodeBlocked {
			return verdict{}, fmt.Errorf("product refused the policy read with status %d", resp.StatusCode)
		}
		return verdict{blocked: true, blockedDetail: problem.Detail}, nil
	}
	return verdict{}, fmt.Errorf("product answered the policy read with status %d", resp.StatusCode)
}

type Fake struct {
	BlockedDetail map[string]string
	Err           error
	Calls         []string
}

func (f *Fake) Check(_ context.Context, _, productID, process string) error {
	f.Calls = append(f.Calls, productID+"|"+process)
	if f.Err != nil {
		return f.Err
	}
	if detail, ok := f.BlockedDetail[productID]; ok {
		return Blocked(detail)
	}
	return nil
}
