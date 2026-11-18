package datascope

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/tracectx"

	"github.com/BenjaminSRussell/depot/services/finreport/internal/domain"
)

var (
	ErrNoDecision        = errors.New("datascope: the policy engine gave no usable decision")
	ErrNoPermittedValues = errors.New("datascope: the subject is restricted to no dimension values")
)

const (
	decisionPath = "/v1/data/authz/data_scope"
	maxAnswer    = 1 << 20
	askTimeout   = 2 * time.Second
)

type Scope map[string][]string

func (s Scope) Restricted() bool { return len(s) > 0 }

func (s Scope) Key() string {
	attributes := make([]string, 0, len(s))
	for attribute := range s {
		attributes = append(attributes, attribute)
	}
	slices.Sort(attributes)
	parts := make([]string, len(attributes))
	for i, attribute := range attributes {
		parts[i] = attribute + "=" + strings.Join(slices.Sorted(slices.Values(s[attribute])), ",")
	}
	return strings.Join(parts, ";")
}

func (s Scope) Allows(dims map[string]string) bool {
	for attribute, permitted := range s {
		value := dims[attribute]
		if value == "" || !slices.Contains(permitted, value) {
			return false
		}
	}
	return true
}

func (s Scope) Filter(balances []domain.Balance) []domain.Balance {
	if !s.Restricted() {
		return balances
	}
	kept := make([]domain.Balance, 0, len(balances))
	for _, b := range balances {
		if s.Allows(b.Dims) {
			kept = append(kept, b)
		}
	}
	return kept
}

type Decider interface {
	Decide(ctx context.Context, p tenant.Principal) (Scope, error)
}

type OPA struct {
	url    string
	client *http.Client
}

func NewOPA(url string) *OPA {
	return &OPA{url: strings.TrimRight(url, "/"), client: &http.Client{Timeout: askTimeout}}
}

type answer struct {
	Result *struct {
		Unrestricted *bool               `json:"unrestricted"`
		Dimensions   map[string][]string `json:"dimensions"`
	} `json:"result"`
}

func (o *OPA) Decide(ctx context.Context, p tenant.Principal) (Scope, error) {
	body, err := json.Marshal(map[string]any{"input": map[string]any{"subject": map[string]string{"tenant_id": p.TenantID, "user_id": p.UserID}}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url+decisionPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	tracectx.Inject(req)
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoDecision, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrNoDecision, resp.StatusCode)
	}
	var got answer
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAnswer)).Decode(&got); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoDecision, err)
	}
	return got.scope()
}

func (a answer) scope() (Scope, error) {
	if a.Result == nil || a.Result.Unrestricted == nil {
		return nil, ErrNoDecision
	}
	if *a.Result.Unrestricted {
		return nil, nil
	}
	if len(a.Result.Dimensions) == 0 {
		return nil, ErrNoPermittedValues
	}
	scope := Scope{}
	for attribute, values := range a.Result.Dimensions {
		if attribute == "" {
			return nil, fmt.Errorf("%w: empty dimension attribute", ErrNoDecision)
		}
		if len(values) == 0 {
			return nil, ErrNoPermittedValues
		}
		scope[attribute] = values
	}
	return scope, nil
}
