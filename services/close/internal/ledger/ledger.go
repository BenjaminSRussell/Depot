package ledger

import (
	"bytes"
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
	"github.com/BenjaminSRussell/depot/services/close/internal/domain"
)

const (
	Timeout         = 3 * time.Second
	ledgersPath     = "/v1/ledger/ledgers/"
	audience        = "ledger"
	dutyLookup      = "ledger.ledger.lookup"
	dutyPeriodLock  = "ledger.period.lock"
	maxResponseSize = 1 << 20
)

var ErrNotVisible = errors.New("ledger is not visible to the legal entity")

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func New(baseURL string, httpClient *http.Client, signer *tenant.ServiceSigner) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer}
}

func (c *Client) LegalEntity(ctx context.Context, legalEntityID, ledgerID string) (string, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return "", errors.New("ledger: no principal in context")
	}
	if c.signer == nil {
		return "", errors.New("ledger: no service signing key configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+ledgersPath+url.PathEscape(ledgerID), nil)
	if err != nil {
		return "", fmt.Errorf("ledger: %w", err)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{dutyLookup}); err != nil {
		return "", fmt.Errorf("ledger: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "ledger request failed", "err", err, "correlationId", middleware.GetReqID(ctx))
		return "", fmt.Errorf("ledger: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return "", fmt.Errorf("ledger: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusNotFound:
		return "", ErrNotVisible
	default:
		slog.ErrorContext(ctx, "ledger rejected lookup", "status", resp.StatusCode, "correlationId", middleware.GetReqID(ctx))
		return "", fmt.Errorf("ledger: unexpected status %d", resp.StatusCode)
	}
	var body struct {
		LegalEntityID string `json:"legalEntityId"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.LegalEntityID == "" {
		return "", errors.New("ledger: response has no legalEntityId")
	}
	return body.LegalEntityID, nil
}

type lockBody struct {
	StartDate string              `json:"startDate"`
	EndDate   string              `json:"endDate"`
	Version   int64               `json:"version"`
	Sources   []domain.LockSource `json:"sources"`
}

// LockPeriod sets the ledger's period lock of one fiscal period (PUT .../period-locks/{fiscalPeriodId},
// FR-14.1, B-08c) with a service token that carries ledger.period.lock alone, on behalf of the end user in
// ctx. It returns nil only on the ledger's 200, which the ledger sends once the lock is committed.
func (c *Client) LockPeriod(ctx context.Context, lock domain.PeriodLock) error {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("ledger: no principal in context")
	}
	if c.signer == nil {
		return errors.New("ledger: no service signing key configured")
	}
	raw, err := json.Marshal(lockBody{StartDate: lock.Start.Format(time.DateOnly), EndDate: lock.End.Format(time.DateOnly), Version: lock.Version, Sources: lock.Sources})
	if err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	target := c.baseURL + ledgersPath + url.PathEscape(lock.LedgerID) + "/period-locks/" + url.PathEscape(lock.FiscalPeriodID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := c.signer.ApplyDuties(req, principal, audience, lock.LegalEntityID, []string{dutyPeriodLock}); err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseSize))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ledger: period lock answered %d", resp.StatusCode)
	}
	return nil
}
