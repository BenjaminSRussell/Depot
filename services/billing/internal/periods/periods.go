// Package periods asks the close service whether the receivables period of a date is open (#35, 35-5): only close decides what may be
// posted into a period, so a self-billed invoice dated by a customer is never posted into a closed one. The call is signed with
// billing's service key and holds only the duty period.check (grant billing -> close).
package periods

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BenjaminSRussell/depot/platform/tenant"
)

const (
	Timeout     = 2 * time.Second
	resolvePath = "/v1/close/periods:resolve"
	duty        = "period.check"
	audience    = "close"
	sourceAR    = "ar"
	statusOpen  = "open"
	dateFormat  = "2006-01-02"
	maxResponse = 1 << 20
)

// Client resolves periods through the close service.
type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

// New returns a client; the signer is required.
func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" || signer == nil {
		return nil, errors.New("periods: a close URL and the service signing key are required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient, signer: signer}, nil
}

// Open reports whether the receivables period of the date in the ledger is open. A date with no period, or in an ambiguous one, is
// not open; a failure to ask is an error (the caller refuses rather than guesses).
func (c *Client) Open(ctx context.Context, legalEntityID, ledgerID string, date time.Time) (bool, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return false, errors.New("periods: no principal in context")
	}
	target := resolvePath + "?" + url.Values{"ledgerId": {ledgerID}, "postingDate": {date.Format(dateFormat)}, "sourceSystem": {sourceAR}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+target, nil)
	if err != nil {
		return false, err
	}
	if _, err := c.signer.ApplyDuties(req, p, audience, legalEntityID, []string{duty}); err != nil {
		return false, fmt.Errorf("periods: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("periods: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return false, fmt.Errorf("periods: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		var out struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(raw, &out); err != nil || out.Status == "" {
			return false, errors.New("periods: the response has no usable status")
		}
		return out.Status == statusOpen, nil
	case http.StatusNotFound, http.StatusConflict:
		return false, nil
	}
	return false, fmt.Errorf("periods: close answered %d", resp.StatusCode)
}
