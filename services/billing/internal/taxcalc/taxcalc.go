package taxcalc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/billing/internal/api"
	"github.com/BenjaminSRussell/depot/services/billing/internal/domain"
)

const (
	PrincipalName   = "billing-tax-calc"
	DutyCalculate   = "tax.tax.calculate"
	DefaultTimeout  = 3 * time.Second
	audience        = "tax"
	calculatePath   = "/v1/tax/calculate"
	maxResponseSize = 1 << 20
)

type Client struct {
	baseURL string
	signer  *tenant.ServiceSigner
	http    *http.Client
}

func New(baseURL string, signer *tenant.ServiceSigner, timeout time.Duration) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("taxcalc: a base URL is required")
	}
	if signer == nil {
		return nil, errors.New("taxcalc: a service signing key is required")
	}
	hc := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), signer: signer, http: hc}, nil
}

// FromEnv builds the client from TAX_URL, TAX_TIMEOUT and the service signing key; nil, nil when TAX_URL is unset.
func FromEnv(getenv func(string) string) (*Client, error) {
	base := getenv("TAX_URL")
	if base == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("taxcalc: TAX_URL needs DEPOT_SERVICE_SIGNING_KEY or DEPOT_SERVICE_SIGNING_KEY_FILE")
	}
	timeout := DefaultTimeout
	if v, err := time.ParseDuration(getenv("TAX_TIMEOUT")); err == nil && v > 0 {
		timeout = v
	}
	return New(base, signer, timeout)
}

type party struct {
	TaxGroup    string `json:"tax_group,omitempty"`
	TaxID       string `json:"tax_id,omitempty"`
	Country     string `json:"country,omitempty"`
	Certificate string `json:"certificate,omitempty"`
}

type line struct {
	LineRef      string `json:"line_ref"`
	Amount       string `json:"amount"`
	ItemTaxGroup string `json:"item_tax_group,omitempty"`
}

type calculateBody struct {
	Currency     string `json:"currency"`
	Direction    string `json:"direction"`
	DocumentDate string `json:"document_date"`
	TaxDate      string `json:"tax_date"`
	StoredConfig bool   `json:"stored_config"`
	Party        party  `json:"party"`
	Lines        []line `json:"lines"`
}

type calculateResponse struct {
	Lines []struct {
		LineRef string `json:"line_ref"`
		Taxes   []struct {
			Code      string `json:"code"`
			TaxAmount string `json:"tax_amount"`
		} `json:"taxes"`
	} `json:"lines"`
}

// Quote asks the tax engine for the output tax of each line with the tenant's stored configuration. A line's code is the
// engine's single code, the code with the largest tax when it returns several, and empty when it returns none (exempt).
func (c *Client) Quote(ctx context.Context, in api.QuoteInput) (api.QuoteResult, error) {
	caller, ok := tenant.FromContext(ctx)
	if !ok {
		return api.QuoteResult{}, errors.New("taxcalc: no principal in context")
	}
	principal, err := tenant.ServicePrincipal(PrincipalName, caller.TenantID, in.LegalEntityID)
	if err != nil {
		return api.QuoteResult{}, fmt.Errorf("taxcalc: %w", err)
	}
	body := calculateBody{Currency: in.Currency, Direction: "output", DocumentDate: in.Date.Format(time.DateOnly),
		TaxDate: in.Date.Format(time.DateOnly), StoredConfig: true, Lines: make([]line, len(in.Lines))}
	if s := in.ShipTo; s != nil {
		body.Party = party{TaxGroup: s.TaxGroup, TaxID: s.TaxID, Country: s.Country, Certificate: s.Certificate}
	}
	for i, l := range in.Lines {
		body.Lines[i] = line{LineRef: strconv.Itoa(i), Amount: l.Net(in.Currency).String(), ItemTaxGroup: l.ItemTaxGroup}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return api.QuoteResult{}, fmt.Errorf("taxcalc: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+calculatePath, bytes.NewReader(payload))
	if err != nil {
		return api.QuoteResult{}, fmt.Errorf("taxcalc: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := c.signer.ApplyDuties(req, principal, audience, in.LegalEntityID, []string{DutyCalculate}); err != nil {
		return api.QuoteResult{}, fmt.Errorf("taxcalc: sign: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "tax calculate request failed", "err", err, "path", calculatePath, "correlationId", middleware.GetReqID(ctx))
		return api.QuoteResult{}, api.ErrTaxUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil || len(raw) > maxResponseSize {
		slog.ErrorContext(ctx, "tax calculate response unreadable", "err", err, "path", calculatePath, "correlationId", middleware.GetReqID(ctx))
		return api.QuoteResult{}, api.ErrTaxUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		slog.ErrorContext(ctx, "tax calculate refused", "status", resp.StatusCode, "path", calculatePath, "correlationId", middleware.GetReqID(ctx))
		if resp.StatusCode == http.StatusUnprocessableEntity {
			return api.QuoteResult{}, &api.UnknownTaxCodeError{Code: firstCode(in.Lines)}
		}
		return api.QuoteResult{}, api.ErrTaxUnavailable
	}
	out, err := parse(raw, len(in.Lines))
	if err != nil {
		slog.ErrorContext(ctx, "tax calculate answer unusable", "err", err, "path", calculatePath, "correlationId", middleware.GetReqID(ctx))
		return api.QuoteResult{}, api.ErrTaxUnavailable
	}
	return out, nil
}

func firstCode(lines []domain.Line) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[0].TaxCode
}

func parse(raw []byte, want int) (api.QuoteResult, error) {
	var res calculateResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return api.QuoteResult{}, err
	}
	if len(res.Lines) != want {
		return api.QuoteResult{}, fmt.Errorf("the engine answered %d lines for %d", len(res.Lines), want)
	}
	out := api.QuoteResult{Taxes: make([]decimal.Decimal, want), Codes: make([]string, want)}
	for _, l := range res.Lines {
		i, err := strconv.Atoi(l.LineRef)
		if err != nil || i < 0 || i >= want {
			return api.QuoteResult{}, fmt.Errorf("the engine answered unknown line_ref %q", l.LineRef)
		}
		largest := decimal.Zero
		for _, t := range l.Taxes {
			v, err := decimal.NewFromString(t.TaxAmount)
			if err != nil || v.IsNegative() {
				return api.QuoteResult{}, fmt.Errorf("the engine answered tax_amount %q", t.TaxAmount)
			}
			out.Taxes[i] = out.Taxes[i].Add(v)
			if out.Codes[i] == "" || v.GreaterThan(largest) {
				out.Codes[i], largest = t.Code, v
			}
		}
	}
	return out, nil
}
