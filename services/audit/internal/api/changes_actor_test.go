package api_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/audit/internal/api"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
)

const changeBody = `{"entity":"party.vendor","recordId":"v-1","field":"bankAccount","oldValue":"DE01","newValue":"DE02","traceId":"trace-1"`

// verifiedRouter serves the audit API to callers whose verified principal is p, as tenant.Middleware would hand it on
// after checking the gateway headers or a service token.
func verifiedRouter(f *fakeStore, p tenant.Principal) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), p)))
		})
	})
	api.New(f).Register(r)
	return r
}

func post(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, changes, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func duty() map[string]struct{} { return map[string]struct{}{"audit.write": {}} }

var (
	person     = tenant.Principal{TenantID: testkit.TenantID(1), UserID: "ana", Duties: duty()}
	viaService = tenant.Principal{TenantID: testkit.TenantID(1), UserID: "ana", Duties: duty(), Service: "billing"}
	onItsOwn   = tenant.Principal{TenantID: testkit.TenantID(1), UserID: "service:search-index", Duties: duty(), Service: "search"}
	viaAgent   = tenant.Principal{TenantID: testkit.TenantID(1), UserID: "ana", Duties: duty(), Service: "agents"}
)

// TestChangeNamingUserIDIsRefused is the AUD-API-1 contract: whoever the caller is, a body that names userId is refused
// with AUD-0002, even when it names the caller itself, and nothing is stored.
func TestChangeNamingUserIDIsRefused(t *testing.T) {
	for _, p := range []tenant.Principal{person, viaService, onItsOwn, viaAgent} {
		for _, userID := range []string{`"mallory"`, `"` + p.UserID + `"`, `""`} {
			f := &fakeStore{}
			rec := post(verifiedRouter(f, p), changeBody+`,"userId":`+userID+`}`)
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"AUD-0002"`) {
				t.Fatalf("%s/%s userId %s: got %d %s; want 422 AUD-0002", p.Service, p.UserID, userID, rec.Code, rec.Body.String())
			}
			if len(f.applied) != 0 {
				t.Fatalf("refused change was stored: %+v", f.applied)
			}
		}
	}
}

// TestStoredActorIsTheVerifiedPrincipal: in every accepted call the stored actor, its kind and the person it was
// recorded for come from the verified principal; the body may restate them but never change them.
func TestStoredActorIsTheVerifiedPrincipal(t *testing.T) {
	tests := []struct {
		name               string
		p                  tenant.Principal
		extra              string
		wantUser, wantKind string
		wantOnBehalfOf     string
		wantRefusal        string
	}{
		{"person", person, ``, "ana", domain.ActorPerson, "", ""},
		{"person restates kind", person, `,"actorKind":"person"`, "ana", domain.ActorPerson, "", ""},
		{"person sends onBehalfOf", person, `,"onBehalfOf":"bob"`, "", "", "", domain.CodeActorRefused},
		{"person sends onBehalfOf of itself", person, `,"onBehalfOf":"ana"`, "", "", "", domain.CodeActorRefused},
		{"person claims service", person, `,"actorKind":"service"`, "", "", "", domain.CodeActorRefused},
		{"service for its user", viaService, ``, "service:billing", domain.ActorService, "ana", ""},
		{"service names its user", viaService, `,"onBehalfOf":"ana","actorKind":"service"`, "service:billing", domain.ActorService, "ana", ""},
		{"service names another user", viaService, `,"onBehalfOf":"bob"`, "", "", "", domain.CodeActorRefused},
		{"service claims person", viaService, `,"actorKind":"person"`, "", "", "", domain.CodeActorRefused},
		{"service principal on its own", onItsOwn, ``, "service:search-index", domain.ActorService, "", ""},
		{"service principal sends onBehalfOf", onItsOwn, `,"onBehalfOf":"ana"`, "", "", "", domain.CodeActorRefused},
		{"agent for its user", viaAgent, `,"onBehalfOf":"ana"`, "service:agents", domain.ActorAgent, "ana", ""},
		{"agent claims service", viaAgent, `,"actorKind":"service"`, "", "", "", domain.CodeActorRefused},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			rec := post(verifiedRouter(f, tc.p), changeBody+tc.extra+`}`)
			if tc.wantRefusal != "" {
				if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"`+tc.wantRefusal+`"`) || len(f.applied) != 0 {
					t.Fatalf("got %d %s stored %d; want 422 %s and nothing stored", rec.Code, rec.Body.String(), len(f.applied), tc.wantRefusal)
				}
				return
			}
			if rec.Code != http.StatusAccepted || len(f.applied) != 1 {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
			got := f.applied[0]
			if got.UserID != tc.wantUser || got.ActorKind != tc.wantKind || got.OnBehalfOf != tc.wantOnBehalfOf {
				t.Fatalf("stored actor %q kind %q onBehalfOf %q; want %q %q %q", got.UserID, got.ActorKind, got.OnBehalfOf, tc.wantUser, tc.wantKind, tc.wantOnBehalfOf)
			}
		})
	}
}

func TestChangeContextIsStored(t *testing.T) {
	f := &fakeStore{}
	rec := post(verifiedRouter(f, person), changeBody+`,"reason":"vendor letter 12","changeSetId":"cs-1","eventType":"dev.depot.party.vendor.updated.v1","eventId":"evt-1","workflowInstanceId":"wf-1"}`)
	if rec.Code != http.StatusAccepted || len(f.applied) != 1 {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	got := f.applied[0]
	if got.Reason != "vendor letter 12" || got.ChangeSetID != "cs-1" || got.EventType != "dev.depot.party.vendor.updated.v1" || got.EventID != "evt-1" || got.WorkflowInstanceID != "wf-1" {
		t.Fatalf("stored %+v", got)
	}
	rec = post(verifiedRouter(&fakeStore{}, person), changeBody+`,"reason":"`+strings.Repeat("x", domain.MaxChangeReason+1)+`"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"reason"`) {
		t.Fatalf("long reason: got %d %s", rec.Code, rec.Body.String())
	}
	rec = post(verifiedRouter(&fakeStore{}, tenant.Principal{TenantID: testkit.TenantID(1), Duties: duty()}), changeBody+`}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "the caller is unknown") {
		t.Fatalf("anonymous caller: got %d %s", rec.Code, rec.Body.String())
	}
}

// TestEngineeringSignatureThroughAServiceToken: engineering, the only HTTP caller, records a signature with a signed
// service token whose act is the signer and a body without userId. The audit service's own middleware verifies the
// token, and the change is stored as engineering recording it for the signer.
func TestEngineeringSignatureThroughAServiceToken(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("engineering", priv)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeStore{}
	r := chi.NewRouter()
	r.Use(tenant.MiddlewareWith(tenant.NewServiceVerifierKids("audit", map[string]map[string]ed25519.PublicKey{"engineering": {"": pub}})))
	api.New(f).Register(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	carol := tenant.Principal{TenantID: testkit.TenantID(1), UserID: "carol", LegalEntityID: "00000000-0000-0000-0000-0000000000e1"}
	send := func(body map[string]any) *http.Response {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+changes, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if _, err := signer.ApplyDuties(req, carol, "audit", carol.LegalEntityID, []string{"audit.write"}); err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res
	}
	sig := map[string]any{"entity": "document", "recordId": "d1", "field": "signature", "newValue": "Approved for release: Carol Kane (carol)", "traceId": "s1", "onBehalfOf": "carol"}
	if res := send(sig); res.StatusCode != http.StatusAccepted {
		t.Fatalf("signature record: %d", res.StatusCode)
	}
	if len(f.applied) != 1 || f.applied[0].UserID != "service:engineering" || f.applied[0].ActorKind != domain.ActorService || f.applied[0].OnBehalfOf != "carol" {
		t.Fatalf("stored %+v", f.applied)
	}
	sig["onBehalfOf"] = "mallory"
	if res := send(sig); res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("onBehalfOf another user: %d", res.StatusCode)
	}
	delete(sig, "onBehalfOf")
	sig["userId"] = "mallory"
	if res := send(sig); res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("userId in body: %d", res.StatusCode)
	}
	if len(f.applied) != 1 {
		t.Fatalf("refused records were stored: %+v", f.applied)
	}
}

// TestPersonHeadersCannotSendOnBehalfOf: through the real middleware, a person's gateway headers are a person, so
// onBehalfOf is refused.
func TestPersonHeadersCannotSendOnBehalfOf(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	rec := do(r, http.MethodPost, changes, changeBody+`,"onBehalfOf":"bob"}`, duties, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"AUD-0006"`) || len(f.applied) != 0 {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	rec = do(r, http.MethodPost, changes, changeBody+`}`, duties, true)
	if rec.Code != http.StatusAccepted || f.applied[0].UserID != "user-1" || f.applied[0].ActorKind != domain.ActorPerson {
		t.Fatalf("got %d %s %+v", rec.Code, rec.Body.String(), f.applied)
	}
}
