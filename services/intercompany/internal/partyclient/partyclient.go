// Package partyclient asks the party service whether a pairing's customer and vendor accounts exist, so a pairing that names an
// account the invoice consumers could not use is refused when it is saved and not when its first charge event reaches the dead
// letter queue (#40). Calls are signed with the service identity and carry only the lookup duty.
package partyclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/depot/platform/tenant"
)

const (
	// Timeout bounds one call unless the caller passes its own HTTP client.
	Timeout = 3 * time.Second

	audience          = "party"
	dutyCustomerLook  = "party.customer.lookup"
	dutyVendorLook    = "party.vendor.lookup"
	customerPath      = "/v1/party/customers/%s"
	vendorPath        = "/v1/party/vendors/%s"
	maxResponseSize   = 1 << 20
	statusUnavailable = "party service did not answer"
)

var (
	// ErrNotFound is an account the party master does not hold.
	ErrNotFound = errors.New("partyclient: not found")
	// ErrUnavailable is any other answer: the account could not be checked.
	ErrUnavailable = errors.New("partyclient: " + statusUnavailable)
)

// Directory is what the API needs of the party service. Client implements it.
type Directory interface {
	CustomerExists(ctx context.Context, legalEntityID, id string) error
	VendorExists(ctx context.Context, legalEntityID, id string) error
}

// Client calls the party service.
type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

// New returns a client; signer is required.
func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("partyclient: a base URL is required")
	}
	if signer == nil {
		return nil, errors.New("partyclient: a service signing key is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer}, nil
}

// FromEnv builds the client from PARTY_URL, PARTY_TIMEOUT and the service signing key variables; nil, nil when PARTY_URL is unset.
func FromEnv(getenv func(string) string) (*Client, error) {
	baseURL := getenv("PARTY_URL")
	if baseURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("partyclient: PARTY_URL needs the service signing key")
	}
	timeout := Timeout
	if v, err := time.ParseDuration(getenv("PARTY_TIMEOUT")); err == nil && v > 0 {
		timeout = v
	}
	return New(baseURL, signer, &http.Client{Timeout: timeout})
}

// CustomerExists is nil when the customer is in the party master, ErrNotFound when it is not.
func (c *Client) CustomerExists(ctx context.Context, legalEntityID, id string) error {
	return c.head(ctx, legalEntityID, id, customerPath, dutyCustomerLook)
}

// VendorExists is nil when the vendor is in the party master, ErrNotFound when it is not.
func (c *Client) VendorExists(ctx context.Context, legalEntityID, id string) error {
	return c.head(ctx, legalEntityID, id, vendorPath, dutyVendorLook)
}

func (c *Client) head(ctx context.Context, legalEntityID, id, path, duty string) error {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("partyclient: no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+fmt.Sprintf(path, url.PathEscape(id)), nil)
	if err != nil {
		return fmt.Errorf("partyclient: %w", err)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{duty}); err != nil {
		return fmt.Errorf("partyclient: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "party request failed", "err", err, "path", path, "correlationId", middleware.GetReqID(ctx))
		return ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseSize))
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrNotFound
	default:
		slog.ErrorContext(ctx, "party rejected the account lookup", "status", resp.StatusCode, "path", path, "correlationId", middleware.GetReqID(ctx))
		return ErrUnavailable
	}
}
