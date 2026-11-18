package datascope

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/tenant"

	"github.com/BenjaminSRussell/depot/services/finreport/internal/domain"
)

func TestScope_KeyIsCanonical(t *testing.T) {
	a := Scope{"DEPARTMENT": {"B", "A"}, "COST_CENTER": {"200", "100"}}
	b := Scope{"COST_CENTER": {"100", "200"}, "DEPARTMENT": {"A", "B"}}
	if a.Key() != "COST_CENTER=100,200;DEPARTMENT=A,B" || a.Key() != b.Key() {
		t.Fatalf("keys %q and %q", a.Key(), b.Key())
	}
	if (Scope)(nil).Key() != "" || Scope(nil).Restricted() {
		t.Fatal("a nil scope is unrestricted with an empty key")
	}
}

func TestScope_FilterKeepsOnlyPermittedBalances(t *testing.T) {
	balances := []domain.Balance{
		{Account: "4000", Dims: map[string]string{"COST_CENTER": "100"}},
		{Account: "4000", Dims: map[string]string{"COST_CENTER": "200"}},
		{Account: "4000", Dims: map[string]string{}},
		{Account: "4000", Dims: map[string]string{"COST_CENTER": ""}},
		{Account: "4000", Dims: map[string]string{"DEPARTMENT": "A"}},
	}
	got := Scope{"COST_CENTER": {"100"}}.Filter(balances)
	if len(got) != 1 || got[0].Dims["COST_CENTER"] != "100" {
		t.Fatalf("restricted filter kept %+v", got)
	}
	if all := Scope(nil).Filter(balances); len(all) != len(balances) {
		t.Fatalf("unrestricted filter kept %d of %d", len(all), len(balances))
	}
}

func decide(t *testing.T, handler http.HandlerFunc) (Scope, error) {
	t.Helper()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	return NewOPA(srv.URL).Decide(context.Background(), tenant.Principal{TenantID: "t1", UserID: "u1"})
}

func reply(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/data/authz/data_scope" || r.Method != http.MethodPost {
			http.Error(w, "wrong call", http.StatusTeapot)
			return
		}
		_, _ = w.Write([]byte(body))
	}
}

func TestOPA_DecideAnswers(t *testing.T) {
	cases := []struct {
		name, body string
		restricted bool
		err        error
	}{
		{"unrestricted", `{"result":{"unrestricted":true}}`, false, nil},
		{"restricted", `{"result":{"unrestricted":false,"dimensions":{"COST_CENTER":["100"]}}}`, true, nil},
		{"result undefined", `{}`, false, ErrNoDecision},
		{"result null", `{"result":null}`, false, ErrNoDecision},
		{"unrestricted missing", `{"result":{"dimensions":{"COST_CENTER":["100"]}}}`, false, ErrNoDecision},
		{"unrestricted wrong type", `{"result":{"unrestricted":"true"}}`, false, ErrNoDecision},
		{"malformed", `{"result":`, false, ErrNoDecision},
		{"dimensions absent", `{"result":{"unrestricted":false}}`, false, ErrNoPermittedValues},
		{"empty list", `{"result":{"unrestricted":false,"dimensions":{"COST_CENTER":[]}}}`, false, ErrNoPermittedValues},
		{"null list", `{"result":{"unrestricted":false,"dimensions":{"COST_CENTER":null}}}`, false, ErrNoPermittedValues},
		{"object instead of list", `{"result":{"unrestricted":false,"dimensions":{"COST_CENTER":{"a":1}}}}`, false, ErrNoDecision},
		{"number in list", `{"result":{"unrestricted":false,"dimensions":{"COST_CENTER":[100]}}}`, false, ErrNoDecision},
		{"empty attribute", `{"result":{"unrestricted":false,"dimensions":{"":["1"]}}}`, false, ErrNoDecision},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scope, err := decide(t, reply(tc.body))
			if !errors.Is(err, tc.err) || scope.Restricted() != tc.restricted {
				t.Fatalf("scope %v err %v, want restricted %v err %v", scope, err, tc.restricted, tc.err)
			}
		})
	}
}

func TestOPA_DecideFailsClosed(t *testing.T) {
	if _, err := decide(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) }); !errors.Is(err, ErrNoDecision) {
		t.Fatalf("non-200: %v", err)
	}
	slow := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(slow))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := NewOPA(srv.URL).Decide(ctx, tenant.Principal{TenantID: "t1", UserID: "u1"}); !errors.Is(err, ErrNoDecision) {
		t.Fatalf("timeout: %v", err)
	}
	if _, err := NewOPA("http://127.0.0.1:1").Decide(context.Background(), tenant.Principal{TenantID: "t1", UserID: "u1"}); !errors.Is(err, ErrNoDecision) {
		t.Fatalf("unreachable: %v", err)
	}
}
