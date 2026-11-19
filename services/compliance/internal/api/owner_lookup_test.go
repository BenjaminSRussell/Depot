package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

const ownerSigningKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func ownerEnv(extra map[string]string) func(string) string {
	env := map[string]string{"DEPOT_SERVICE_NAME": "compliance", "DEPOT_SERVICE_SIGNING_KEY": ownerSigningKey}
	for k, v := range extra {
		env[k] = v
	}
	return func(k string) string { return env[k] }
}

func ownerClient(t *testing.T, url string) *OwnerClient {
	t.Helper()
	c, err := OwnerFromEnv(ownerEnv(map[string]string{"PROJECTS_URL": url}))
	if err != nil || c == nil {
		t.Fatalf("owner client: %v %v", c, err)
	}
	return c
}

func ownerCtx(scopes string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: ids.New(), UserID: "u-1", LegalEntityID: scopes, Duties: map[string]struct{}{}})
}

func TestOwnerClient_ReadsTheLegalEntityWithASignedToken(t *testing.T) {
	le, record := ids.New(), ids.New()
	var path, token string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, token = r.URL.Path, r.Header.Get(tenant.HeaderServiceToken)
		_, _ = w.Write([]byte(`{"projectId":"` + record + `","legalEntityId":"` + strings.ToUpper(le) + `"}`))
	}))
	defer srv.Close()
	got, err := ownerClient(t, srv.URL).LegalEntity(ownerCtx(le), "project", record)
	if err != nil || got != le {
		t.Fatalf("legal entity %q, %v", got, err)
	}
	if path != "/v1/projects/"+record || token == "" {
		t.Fatalf("path %q token %q", path, token)
	}
}

func TestOwnerClient_TriesEachScopeAndMapsNotFound(t *testing.T) {
	first, second, record := ids.New(), ids.New(), ids.New()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"legalEntityId":"` + second + `"}`))
	}))
	defer srv.Close()
	c := ownerClient(t, srv.URL)
	if got, err := c.LegalEntity(ownerCtx(first+","+second), "project", record); err != nil || got != second || calls != 2 {
		t.Fatalf("second scope: %q %v after %d calls", got, err, calls)
	}
	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer missing.Close()
	if _, err := ownerClient(t, missing.URL).LegalEntity(ownerCtx(first), "project", record); !errors.Is(err, ErrOwnerNotFound) {
		t.Fatalf("404: %v", err)
	}
}

func TestOwnerClient_FailuresAreNeverAGuessedValue(t *testing.T) {
	le, record := ids.New(), ids.New()
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"server error":      {http.StatusInternalServerError, ""},
		"forbidden":         {http.StatusForbidden, ""},
		"no legal entity":   {http.StatusOK, `{"projectId":"x"}`},
		"not a uuid":        {http.StatusOK, `{"legalEntityId":"nope"}`},
		"nil legal entity":  {http.StatusOK, `{"legalEntityId":"` + ids.Nil + `"}`},
		"unreadable body":   {http.StatusOK, `<html>`},
		"redirect refused":  {http.StatusFound, ""},
		"wrong typed field": {http.StatusOK, `{"legalEntityId":7}`},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		got, err := ownerClient(t, srv.URL).LegalEntity(ownerCtx(le), "project", record)
		srv.Close()
		if err == nil || errors.Is(err, ErrOwnerNotFound) || got != "" {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
}

func TestOwnerClient_RefusesWhatItCannotLookUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("owner called") }))
	defer srv.Close()
	c := ownerClient(t, srv.URL)
	record := ids.New()
	for name, tc := range map[string]struct {
		ctx        context.Context
		entityType string
	}{
		"unsupported type":      {ownerCtx(ids.New()), "contract"},
		"no legal-entity claim": {ownerCtx(""), "project"},
		"no principal":          {context.Background(), "project"},
	} {
		if _, err := c.LegalEntity(tc.ctx, tc.entityType, record); !errors.Is(err, errOwnerUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()
	if _, err := ownerClient(t, url).LegalEntity(ownerCtx(ids.New()), "project", record); !errors.Is(err, errOwnerUnavailable) {
		t.Errorf("connection refused: %v", err)
	}
}

func TestOwnerFromEnv(t *testing.T) {
	if c, err := OwnerFromEnv(ownerEnv(nil)); c != nil || err != nil {
		t.Fatalf("no URL: %v %v", c, err)
	}
	unsigned := func(k string) string { return map[string]string{"PROJECTS_URL": "http://projects"}[k] }
	if _, err := OwnerFromEnv(unsigned); err == nil {
		t.Fatal("a URL without a signing key must fail")
	}
}
