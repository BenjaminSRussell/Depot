package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"
)

const (
	pricingAudience    = "pricing"
	dutyPricingResolve = "pricing.price.resolve"
)

// WithPricing sets the pricing service base URL; without it ResolvePrices answers ErrNotConfigured.
func (c *Client) WithPricing(pricingURL string) *Client {
	c.pricingURL = strings.TrimRight(pricingURL, "/")
	return c
}

// PriceLine is one line to price: the product, the unit it is ordered in and the quantity.
type PriceLine struct {
	ProductID string
	Unit      string
	Quantity  string
}

// PriceRequest asks pricing for the customer's own prices. The customer group and the legal entity come from the party service and
// the portal token, never from the caller's body.
type PriceRequest struct {
	LegalEntityID   string
	CustomerID      string
	CustomerGroupID string
	Currency        string
	PricingDate     string
	SalesChannel    string
	Lines           []PriceLine
}

// PricedLine is pricing's answer for one line. ErrorCode PRC-0001 means no price exists for the customer and product.
type PricedLine struct {
	LineNo       int
	Unit         string
	UnitPrice    decimal.Decimal
	NetUnitPrice decimal.Decimal
	ErrorCode    string
}

// ResolvePrices prices lines for one customer with pricing's resolution (POST /v1/pricing/resolve, duty pricing.price.resolve), which
// applies the customer's negotiated condition records, discounts and the procedure assigned to the sales channel. The answer is in the
// order of the request; a line with no price carries ErrorCode (the request itself still succeeds).
func (c *Client) ResolvePrices(ctx context.Context, req PriceRequest) ([]PricedLine, error) {
	if c == nil || c.pricingURL == "" {
		return nil, ErrNotConfigured
	}
	type line struct {
		ProductID string `json:"productId"`
		Unit      string `json:"unit"`
		Quantity  string `json:"quantity"`
	}
	body := struct {
		LegalEntityID   string `json:"legalEntityId"`
		CustomerID      string `json:"customerId"`
		CustomerGroupID string `json:"customerGroupId,omitempty"`
		Currency        string `json:"currency"`
		PricingDate     string `json:"pricingDate,omitempty"`
		SalesChannel    string `json:"salesChannel,omitempty"`
		Lines           []line `json:"lines"`
	}{LegalEntityID: req.LegalEntityID, CustomerID: req.CustomerID, CustomerGroupID: req.CustomerGroupID, Currency: req.Currency,
		PricingDate: req.PricingDate, SalesChannel: req.SalesChannel}
	for _, l := range req.Lines {
		body.Lines = append(body.Lines, line{ProductID: l.ProductID, Unit: l.Unit, Quantity: l.Quantity})
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	status, raw, err := c.do(ctx, http.MethodPost, c.pricingURL+"/v1/pricing/resolve", pricingAudience, dutyPricingResolve, req.LegalEntityID, "", payload)
	if err != nil {
		return nil, err
	}
	return decodePriced(status, raw, len(req.Lines))
}

func decodePriced(status int, raw []byte, want int) ([]PricedLine, error) {
	if status != http.StatusOK {
		return nil, &StatusError{Audience: pricingAudience, Status: status}
	}
	var out struct {
		Lines []struct {
			LineNo       int    `json:"lineNo"`
			Unit         string `json:"unit"`
			UnitPrice    string `json:"unitPrice"`
			NetUnitPrice string `json:"netUnitPrice"`
			ErrorCode    string `json:"errorCode"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode pricing response: %w", err)
	}
	if len(out.Lines) != want {
		return nil, fmt.Errorf("pricing priced %d lines of %d", len(out.Lines), want)
	}
	priced := make([]PricedLine, 0, want)
	for i, l := range out.Lines {
		p := PricedLine{LineNo: l.LineNo, Unit: l.Unit, ErrorCode: l.ErrorCode}
		if l.ErrorCode == "" {
			var err error
			if p.UnitPrice, err = decimal.NewFromString(l.UnitPrice); err != nil {
				return nil, fmt.Errorf("pricing line %d unit price: %w", i+1, err)
			}
			if p.NetUnitPrice, err = decimal.NewFromString(l.NetUnitPrice); err != nil {
				return nil, fmt.Errorf("pricing line %d net unit price: %w", i+1, err)
			}
		}
		priced = append(priced, p)
	}
	return priced, nil
}
