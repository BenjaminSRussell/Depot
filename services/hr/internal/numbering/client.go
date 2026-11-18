package numbering

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

// Problem codes. Both failures are 503: nothing was created, so the caller may retry. CodeTimeout says numbering did not
// answer within the client's deadline (DefaultTimeout unless NewWithTimeout says otherwise); CodeUnavailable covers every
// other failure (not configured, no signing key, unreachable, an error status, a malformed answer), with the cause in the
// detail.
const (
	CodeUnavailable    = "HCM-NUMBERING-UNAVAILABLE"
	CodeTimeout        = "HCM-NUMBERING-TIMEOUT"
	CodeEntityRequired = "HCM-LEGAL-ENTITY-REQUIRED"
	// DefaultTimeout bounds one allocation end to end (connect, request, response body), whatever the caller's deadline.
	DefaultTimeout = 3 * time.Second
	maxResponse    = 1 << 20
	audience       = "numbering"
	allocateDuty   = "numbering.sequence.allocate"
	retryHint      = "no requisition number was given out and nothing was saved; retry the request (with the same Idempotency-Key, if it had one)"
)

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
	timeout time.Duration
}

func New(baseURL string) *Client { return NewWithTimeout(baseURL, DefaultTimeout) }

// NewWithTimeout bounds each allocation by d (DefaultTimeout when d is not positive).
func NewWithTimeout(baseURL string, d time.Duration) *Client {
	if d <= 0 {
		d = DefaultTimeout
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: d}, timeout: d}
}

func (c *Client) WithSigner(s *tenant.ServiceSigner) *Client {
	c.signer = s
	return c
}

// Unavailable is the sequencer when no numbering URL is configured.
type Unavailable struct{}

func (Unavailable) Next(context.Context, string, string) (string, error) {
	return "", httpx.NewProblem(http.StatusServiceUnavailable, CodeUnavailable,
		"the numbering service is not configured for hr (NUMBERING_URL is empty); "+retryHint+" once it is")
}

// statusError is a non-2xx answer from numbering.
type statusError struct{ status int }

func (e statusError) Error() string { return fmt.Sprintf("numbering answered %d", e.status) }

var errNoSigner = errors.New("no service signing key configured")

// problem turns an allocation failure into the problem the caller sees: CodeTimeout when the deadline passed, else
// CodeUnavailable with the cause. Internal details (addresses, bodies) stay in the log.
func (c *Client) problem(ctx context.Context, err error) error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return httpx.NewProblem(http.StatusServiceUnavailable, CodeTimeout,
			fmt.Sprintf("the numbering service did not answer within %s; %s", c.timeout, retryHint))
	}
	cause := "the numbering service could not be reached or gave an unreadable answer"
	var status statusError
	switch {
	case errors.Is(err, errNoSigner):
		cause = "hr has no service signing key to call the numbering service with"
	case errors.As(err, &status):
		cause = fmt.Sprintf("the numbering service answered HTTP %d", status.status)
	}
	return httpx.NewProblem(http.StatusServiceUnavailable, CodeUnavailable, cause+"; "+retryHint)
}

func (c *Client) Next(ctx context.Context, code, idempotencyKey string) (string, error) {
	if p, _ := tenant.FromContext(ctx); !ids.Valid(p.LegalEntityID) {
		return "", httpx.NewProblem(http.StatusUnprocessableEntity, CodeEntityRequired, "a single legal entity (X-Depot-Legal-Entity) is required to allocate a requisition number")
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	formatted, err := c.allocate(ctx, code, idempotencyKey)
	if err != nil {
		slog.Error("numbering allocation failed", "err", err, "code", code, "timeout", c.timeout, "correlationId", middleware.GetReqID(ctx))
		return "", c.problem(ctx, err)
	}
	return formatted, nil
}

func (c *Client) allocate(ctx context.Context, code, idempotencyKey string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/numbering/sequences/"+url.PathEscape(code)+"/next", nil)
	if err != nil {
		return "", err
	}
	p, _ := tenant.FromContext(ctx)
	if c.signer == nil {
		return "", errNoSigner
	}
	if _, err := c.signer.ApplyDuties(req, p, audience, "", []string{allocateDuty}); err != nil {
		return "", err
	}
	req.Header.Set("Idempotency-Key", idempotencyKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", statusError{resp.StatusCode}
	}
	var issued struct {
		Formatted string `json:"formatted"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(&issued); err != nil {
		return "", fmt.Errorf("decode numbering response: %w", err)
	}
	if issued.Formatted == "" {
		return "", fmt.Errorf("numbering response has no formatted value")
	}
	return issued.Formatted, nil
}
