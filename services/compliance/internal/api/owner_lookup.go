package api

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

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/domain"
)

const (
	ownerTimeout      = 3 * time.Second
	ownerMaxBody      = 1 << 20
	projectsAudience  = "projects"
	dutyProjectsRead  = "projects.inquire"
	projectsPathStart = "/v1/projects/"
)

var (
	ErrOwnerNotFound    = errors.New("owner: no such record in the caller's scope")
	errOwnerUnavailable = errors.New("owner: lookup unavailable")
)

type OwnerLookup interface {
	LegalEntity(ctx context.Context, entityType, recordID string) (string, error)
}

type OwnerClient struct {
	projectsURL string
	http        *http.Client
	signer      *tenant.ServiceSigner
}

func NewOwnerClient(projectsURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*OwnerClient, error) {
	if projectsURL == "" {
		return nil, errors.New("owner: a projects URL is required")
	}
	if signer == nil {
		return nil, errors.New("owner: a service signing key is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: ownerTimeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &OwnerClient{projectsURL: strings.TrimRight(projectsURL, "/"), http: &noRedirect, signer: signer}, nil
}

func OwnerFromEnv(getenv func(string) string) (*OwnerClient, error) {
	projectsURL := getenv("PROJECTS_URL")
	if projectsURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	return NewOwnerClient(projectsURL, signer, nil)
}

// ponytail: one call per legal-entity scope of the caller, upgrade to a bulk lookup on the owning service
func (c *OwnerClient) LegalEntity(ctx context.Context, entityType, recordID string) (string, error) {
	if entityType != domain.EntityProject {
		return "", fmt.Errorf("%w: no owning service for %q", errOwnerUnavailable, entityType)
	}
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("%w: no principal in context", errOwnerUnavailable)
	}
	scopes := tenant.LegalEntityScopeList(p)
	if len(scopes) == 0 {
		return "", fmt.Errorf("%w: the caller has no legal-entity scope", errOwnerUnavailable)
	}
	for _, scope := range scopes {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("%w: %v", errOwnerUnavailable, err)
		}
		le, err := c.project(ctx, p, scope, recordID)
		if errors.Is(err, ErrOwnerNotFound) {
			continue
		}
		return le, err
	}
	return "", ErrOwnerNotFound
}

func (c *OwnerClient) project(ctx context.Context, p tenant.Principal, scope, recordID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.projectsURL+projectsPathStart+url.PathEscape(recordID), nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errOwnerUnavailable, err)
	}
	if _, err := c.signer.ApplyDuties(req, p, projectsAudience, scope, []string{dutyProjectsRead}); err != nil {
		return "", fmt.Errorf("%w: %v", errOwnerUnavailable, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errOwnerUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", ErrOwnerNotFound
	default:
		slog.ErrorContext(ctx, "projects refused the legal-entity lookup", "status", resp.StatusCode)
		return "", fmt.Errorf("%w: status %d", errOwnerUnavailable, resp.StatusCode)
	}
	var body struct {
		LegalEntityID string `json:"legalEntityId"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, ownerMaxBody))
	if err != nil {
		return "", fmt.Errorf("%w: %v", errOwnerUnavailable, err)
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", fmt.Errorf("%w: unreadable body", errOwnerUnavailable)
	}
	le, ok := ids.Canonical(body.LegalEntityID)
	if !ok || le == ids.Nil {
		return "", fmt.Errorf("%w: response has no legal entity", errOwnerUnavailable)
	}
	return le, nil
}
