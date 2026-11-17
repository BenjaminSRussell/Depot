// Package statements runs the scheduled customer statements (#46, 46-4): it reads each customer's open items from receivables,
// renders the statement with the tenant's template and text blocks, and delivers it by the output rules.
package statements

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/print/internal/domain"
)

// Source is where statement data comes from. HTTPSource reads receivables and party; tests use a fake.
type Source interface {
	// Customers lists the customers of the legal entity that have a balance to state.
	Customers(ctx context.Context, legalEntityID string) ([]string, error)
	// Statement returns the customer's open items as of the date, by currency.
	Statement(ctx context.Context, legalEntityID, customerID string, asOf time.Time) ([]domain.StatementSection, error)
	// CustomerName is the name printed on the statement; an unknown customer has none.
	CustomerName(ctx context.Context, legalEntityID, customerID string) string
}

const (
	// Principal is the service principal the runner acts as; its duties are the two read duties print holds on receivables and party.
	Principal = "print-statements"

	DutyReceivablesView = "receivables.view"
	DutyPartyRead       = "party.read"

	maxResponse = 8 << 20
	httpTimeout = 20 * time.Second
)

// HTTPSource reads receivables' aging snapshots and statements and party's customer roles, signed with print's service key.
type HTTPSource struct {
	ReceivablesURL string
	PartyURL       string
	Signer         *tenant.ServiceSigner
	HTTP           *http.Client
}

// NewHTTPSource returns a source; the signer and both base URLs are required.
func NewHTTPSource(receivablesURL, partyURL string, signer *tenant.ServiceSigner, c *http.Client) (*HTTPSource, error) {
	if receivablesURL == "" || partyURL == "" || signer == nil {
		return nil, errors.New("statements: the receivables and party URLs and the service signing key are required")
	}
	if c == nil {
		c = &http.Client{Timeout: httpTimeout}
	}
	nr := *c
	nr.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPSource{ReceivablesURL: strings.TrimRight(receivablesURL, "/"), PartyURL: strings.TrimRight(partyURL, "/"), Signer: signer, HTTP: &nr}, nil
}

func (s *HTTPSource) get(ctx context.Context, base, path, aud, legalEntityID string, duty string, into any) error {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("statements: no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if _, err := s.Signer.ApplyDuties(req, p, aud, legalEntityID, []string{duty}); err != nil {
		return fmt.Errorf("statements: %w", err)
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("statements: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("statements: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("statements: %s answered %d", aud, resp.StatusCode)
	}
	return json.Unmarshal(raw, into)
}

const (
	snapshotPage     = 500
	maxSnapshotPages = 1000
)

// Customers lists the customers with a non-zero balance in the latest aging snapshot of the legal entity, paging through every row
// (one per customer and currency) and counting each customer once.
func (s *HTTPSource) Customers(ctx context.Context, legalEntityID string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for page := 0; page < maxSnapshotPages; page++ {
		var body struct {
			Items []struct {
				CustomerID string `json:"customerId"`
				TotalOpen  string `json:"totalOpen"`
			} `json:"items"`
		}
		q := url.Values{"legalEntityId": {legalEntityID}, "limit": {strconv.Itoa(snapshotPage)}, "offset": {strconv.Itoa(page * snapshotPage)}}
		if err := s.get(ctx, s.ReceivablesURL, "/v1/receivables/aging-snapshots?"+q.Encode(), "receivables", legalEntityID, DutyReceivablesView, &body); err != nil {
			return nil, err
		}
		for _, it := range body.Items {
			if strings.Trim(it.TotalOpen, "-0.") == "" || seen[it.CustomerID] {
				continue
			}
			seen[it.CustomerID] = true
			out = append(out, it.CustomerID)
		}
		if len(body.Items) < snapshotPage {
			return out, nil
		}
	}
	return nil, errors.New("statements: the aging snapshot has more rows than the runner pages through")
}

// CustomerBelongs reports whether the customer role is one of the legal entity (party answers 404 for another entity's customer).
func (s *HTTPSource) CustomerBelongs(ctx context.Context, legalEntityID, customerID string) (bool, error) {
	var role struct {
		LegalEntityID string `json:"legalEntityId"`
	}
	err := s.get(ctx, s.PartyURL, "/v1/party/customers/"+url.PathEscape(customerID), "party", legalEntityID, DutyPartyRead, &role)
	if err != nil {
		if strings.Contains(err.Error(), "answered 404") {
			return false, nil
		}
		return false, err
	}
	return strings.EqualFold(role.LegalEntityID, legalEntityID), nil
}

// Statement reads receivables' statement of one customer.
func (s *HTTPSource) Statement(ctx context.Context, legalEntityID, customerID string, asOf time.Time) ([]domain.StatementSection, error) {
	var body struct {
		Sections []domain.StatementSection `json:"sections"`
	}
	q := url.Values{"legalEntityId": {legalEntityID}, "customer": {customerID}, "asOf": {asOf.Format(time.DateOnly)}}
	if err := s.get(ctx, s.ReceivablesURL, "/v1/receivables/statements?"+q.Encode(), "receivables", legalEntityID, DutyReceivablesView, &body); err != nil {
		return nil, err
	}
	return body.Sections, nil
}

// CustomerName reads the customer role and then its party; "" when either cannot be read.
func (s *HTTPSource) CustomerName(ctx context.Context, legalEntityID, customerID string) string {
	var role struct {
		PartyID string `json:"partyId"`
	}
	if err := s.get(ctx, s.PartyURL, "/v1/party/customers/"+url.PathEscape(customerID), "party", legalEntityID, DutyPartyRead, &role); err != nil || role.PartyID == "" {
		return ""
	}
	var party struct {
		Name string `json:"name"`
	}
	if err := s.get(ctx, s.PartyURL, "/v1/party/parties/"+url.PathEscape(role.PartyID), "party", legalEntityID, DutyPartyRead, &party); err != nil {
		return ""
	}
	return party.Name
}
