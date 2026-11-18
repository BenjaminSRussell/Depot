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
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

// Problem codes. Both failures are 503: nothing was created, so the caller may retry. CodeTimeout says numbering did not
// answer within the client's deadline (DefaultTimeout unless NewWithTimeout says otherwise); CodeUnavailable covers every
// other failure (not configured, unreachable, an error status, a malformed answer), with the cause in the detail.
const (
	CodeUnavailable    = "PTY-NUMBERING-UNAVAILABLE"
	CodeTimeout        = "PTY-NUMBERING-TIMEOUT"
	CodeEntityRequired = "PTY-LEGAL-ENTITY-REQUIRED"
	// DefaultTimeout bounds one allocation end to end (connect, request, response body), whatever the caller's deadline.
	DefaultTimeout = 3 * time.Second
	maxResponse    = 1 << 20
	retryHint      = "no party number was given out and nothing was saved; retry the request (with the same Idempotency-Key, if it had one)"
)

const audience = "numbering"

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
	timeout time.Duration
}

func (c *Client) WithSigner(s *tenant.ServiceSigner) *Client {
	c.signer = s
	return c
}

func New(baseURL string) *Client { return NewWithTimeout(baseURL, DefaultTimeout) }

// NewWithTimeout bounds each allocation by d (DefaultTimeout when d is not positive).
func NewWithTimeout(baseURL string, d time.Duration) *Client {
	if d <= 0 {
		d = DefaultTimeout
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: d}, timeout: d}
}

// Unavailable is the sequencer when no numbering URL is configured.
type Unavailable struct{}

func (Unavailable) Next(context.Context, string, string) (string, error) {
	return "", httpx.NewProblem(http.StatusServiceUnavailable, CodeUnavailable,
		"the numbering service is not configured for party (NUMBERING_URL is empty); "+retryHint+" once it is")
}

// statusError is a non-2xx answer from numbering.
type statusError struct{ status int }

func (e statusError) Error() string { return fmt.Sprintf("numbering answered %d", e.status) }

// problem turns an allocation failure into the problem the caller sees: CodeTimeout when the deadline passed, else
// CodeUnavailable with the cause. Internal details (addresses, bodies) stay in the log.
func (c *Client) problem(ctx context.Context, err error) error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return httpx.NewProblem(http.StatusServiceUnavailable, CodeTimeout,
			fmt.Sprintf("the numbering service did not answer within %s; %s", c.timeout, retryHint))
	}
	cause := "could not be reached or gave an unreadable answer"
	var status statusError
	if errors.As(err, &status) {
		cause = fmt.Sprintf("answered HTTP %d", status.status)
	}
	return httpx.NewProblem(http.StatusServiceUnavailable, CodeUnavailable, "the numbering service "+cause+"; "+retryHint)
}

func (c *Client) Next(ctx context.Context, code, idempotencyKey string) (string, error) {
	if p, _ := tenant.FromContext(ctx); !ids.Valid(p.LegalEntityID) {
		return "", httpx.NewProblem(http.StatusUnprocessableEntity, CodeEntityRequired, "a single legal entity (X-Depot-Legal-Entity) is required to allocate a party number")
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
	signed, err := c.signer.ApplyDuties(req, p, audience, "", []string{"numbering.sequence.allocate"})
	if err != nil {
		return "", err
	}
	if !signed {
		duties := make([]string, 0, len(p.Duties))
		for d := range p.Duties {
			duties = append(duties, d)
		}
		sort.Strings(duties)
		tenant.ForwardGatewaySecret(req)
		req.Header.Set(tenant.HeaderTenant, p.TenantID)
		req.Header.Set(tenant.HeaderLegalEntity, p.LegalEntityID)
		req.Header.Set(tenant.HeaderUser, p.UserID)
		req.Header.Set(tenant.HeaderDuties, strings.Join(duties, ","))
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
