package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/BenjaminSRussell/depot/platform/tenant"
)

// LookupDuty is the service-only identity duty of the legal-entity existence check (grant shopfloor -> identity).
const LookupDuty = "identity.legal-entity.lookup"

// legalEntityNotFoundCode is identity's problem code for a legal entity the tenant does not have.
const legalEntityNotFoundCode = "IDN-LEGAL-ENTITY-NOT-FOUND"

// ErrLegalEntityUnknown is a legal entity identity does not know in the tenant.
var ErrLegalEntityUnknown = errors.New("the tenant has no such legal entity")

// LegalEntities asks identity whether a legal entity exists in the caller's tenant and is active (B-03b): the shop
// floor checks it before it assigns workers to a legal entity, because that assignment freezes once a worker has time
// or pay. It signs each call with a service token for the calling tenant administrator.
type LegalEntities struct {
	BaseURL string
	Signer  *tenant.ServiceSigner
	Client  *http.Client
}

// NewLegalEntities builds the check against identity at baseURL.
func NewLegalEntities(baseURL string, signer *tenant.ServiceSigner, client *http.Client) (*LegalEntities, error) {
	switch {
	case baseURL == "":
		return nil, errors.New("the legal-entity check needs IDENTITY_URL")
	case signer == nil:
		return nil, errors.New("the legal-entity check needs a service signing key (DEPOT_SERVICE_NAME and DEPOT_SERVICE_SIGNING_KEY or DEPOT_SERVICE_SIGNING_KEY_FILE)")
	}
	if client == nil {
		client = &http.Client{Timeout: reqTimout}
	}
	// Never follow a redirect: the request carries a service token, which must reach identity and nothing else. A
	// redirect is answered as it is, and Active reports it as unavailable.
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &LegalEntities{BaseURL: strings.TrimRight(baseURL, "/"), Signer: signer, Client: &noRedirect}, nil
}

// Active reports whether legalEntityID is an active legal entity of the caller's tenant. ErrLegalEntityUnknown when
// identity knows none; any other error means identity could not answer, which the caller must treat as unavailable.
func (l *LegalEntities) Active(ctx context.Context, legalEntityID string) (bool, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return false, errors.New("tenant context is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.BaseURL+"/v1/identity/legal-entities/"+url.PathEscape(legalEntityID), nil)
	if err != nil {
		return false, err
	}
	if _, err := l.Signer.ApplyDuties(req, p, audience, "", []string{LookupDuty}); err != nil {
		return false, fmt.Errorf("sign the legal-entity check: %w", err)
	}
	res, err := l.Client.Do(req)
	if err != nil {
		return false, fmt.Errorf("check the legal entity: %w", err)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	res.Body.Close()
	if err != nil {
		return false, fmt.Errorf("check the legal entity: %w", err)
	}
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// Only identity's own answer means unknown; any other 404 (a wrong IDENTITY_URL, a proxy's "no route") is an
		// unavailable check.
		var problem struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(body, &problem) == nil && problem.Code == legalEntityNotFoundCode {
			return false, ErrLegalEntityUnknown
		}
		return false, fmt.Errorf("identity answered 404 without %s to the legal-entity check", legalEntityNotFoundCode)
	default:
		return false, fmt.Errorf("identity answered %d to the legal-entity check", res.StatusCode)
	}
	var out struct {
		LegalEntityID string `json:"legalEntityId"`
		Active        bool   `json:"active"`
	}
	if err := json.Unmarshal(body, &out); err != nil || !strings.EqualFold(out.LegalEntityID, legalEntityID) {
		return false, fmt.Errorf("identity's legal-entity answer is not for %s", legalEntityID)
	}
	return out.Active, nil
}
