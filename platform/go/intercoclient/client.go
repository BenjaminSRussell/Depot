package intercoclient

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
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/depot/platform/tenant"
)

const (
	Timeout  = 3 * time.Second
	DutyView = "intercompany.view"

	audience        = "intercompany"
	maxResponseSize = 1 << 20
	runPageSize     = "100"
	resolutionOpen  = "open"
	statusMismatch  = "Mismatch"
)

type Error struct{ Status int }

func (e *Error) Error() string { return fmt.Sprintf("intercoclient: unexpected status %d", e.Status) }

type Mismatches interface {
	OpenMismatches(ctx context.Context, legalEntityID, period string) (int, error)
}

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("intercoclient: a base URL is required")
	}
	if signer == nil {
		return nil, errors.New("intercoclient: a service signing key is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer}, nil
}

func FromEnv(getenv func(string) string) (*Client, error) {
	baseURL := getenv("INTERCOMPANY_URL")
	if baseURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("intercoclient: INTERCOMPANY_URL needs the service signing key")
	}
	timeout := Timeout
	if v, err := time.ParseDuration(getenv("INTERCOMPANY_TIMEOUT")); err == nil && v > 0 {
		timeout = v
	}
	return New(baseURL, signer, &http.Client{Timeout: timeout})
}

func (c *Client) get(ctx context.Context, legalEntityID, path string, q url.Values, into any) error {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("intercoclient: no principal in context")
	}
	target := c.baseURL + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("intercoclient: %w", err)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{DutyView}); err != nil {
		return fmt.Errorf("intercoclient: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("intercoclient: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		slog.ErrorContext(ctx, "intercompany refused reconciliation read", "status", resp.StatusCode, "path", path, "correlationId", middleware.GetReqID(ctx))
		return &Error{Status: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("intercoclient: %w", err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("intercoclient: unusable response: %w", err)
	}
	return nil
}

// OpenMismatches counts the unresolved Mismatch items of the newest reconciliation run of the period; a period with no run has none.
func (c *Client) OpenMismatches(ctx context.Context, legalEntityID, period string) (int, error) {
	var runs struct {
		Items []struct {
			ID     string `json:"id"`
			Period string `json:"period"`
		} `json:"items"`
	}
	if err := c.get(ctx, legalEntityID, "/v1/interco/reconciliations", url.Values{"limit": {runPageSize}}, &runs); err != nil {
		return 0, err
	}
	for _, run := range runs.Items {
		if run.Period != period {
			continue
		}
		var detail struct {
			Items []struct {
				Resolution string `json:"resolution"`
			} `json:"items"`
		}
		if err := c.get(ctx, legalEntityID, "/v1/interco/reconciliations/"+url.PathEscape(run.ID), url.Values{"status": {statusMismatch}}, &detail); err != nil {
			return 0, err
		}
		open := 0
		for _, it := range detail.Items {
			if it.Resolution == resolutionOpen {
				open++
			}
		}
		return open, nil
	}
	return 0, nil
}
