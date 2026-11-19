package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
	"github.com/BenjaminSRussell/depot/services/audit/internal/store"
)

const (
	aiAccessPath = "/v1/audit/ai-data-access"
	aiAccessBody = `{"kind":"READ","agentId":"copilot","runId":"run-1","tool":"depot.ap.invoice.list","entityType":"ap.invoice",` +
		`"recordIds":["inv-1","inv-2"],"recordClasses":["NONE","CUI"],"highestClass":"P2","routeId":"route-1","region":"us-east-1",` +
		`"redactionCounts":{"email":2},"excludedCounts":{"policy":1},"policyVersion":3,"decision":"REDACTED","traceId":"run-1"}`
)

func (f *fakeStore) RecordAIAccess(_ context.Context, access domain.AIAccess) (store.AIAccessRecord, error) {
	if f.err != nil {
		return store.AIAccessRecord{}, f.err
	}
	f.aiAccess = append(f.aiAccess, access)
	return store.AIAccessRecord{ID: "a1", Seq: int64(len(f.aiAccess))}, nil
}

func aiAccessPrincipal(service string, duties ...string) tenant.Principal {
	p := tenant.Principal{TenantID: testkit.TenantID(1), UserID: "ana", Service: service, LegalEntityID: testkit.TenantID(7), Duties: map[string]struct{}{}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return p
}

func postAIAccess(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, aiAccessPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRecordAIAccessTakesTheActorFromTheVerifiedToken(t *testing.T) {
	f := &fakeStore{}
	rec := postAIAccess(verifiedRouter(f, aiAccessPrincipal("agents", dutyAIAccessWrite)), aiAccessBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got := f.aiAccess[0]
	if got.Actor != "agent:copilot@user:ana" || got.UserID != "ana" || got.LegalEntityID != testkit.TenantID(7) || got.Tool != "depot.ap.invoice.list" ||
		strings.Join(got.RecordIDs, ",") != "inv-1,inv-2" || got.RedactionCounts["email"] != 2 || got.OccurredAt.IsZero() {
		t.Fatalf("stored %+v", got)
	}
}

func TestRecordAIAccessAcceptsDocumentNumbersAsIDs(t *testing.T) {
	f := &fakeStore{}
	body := strings.Replace(aiAccessBody, `"inv-1"`, `"INV/2026/001"`, 1)
	rec := postAIAccess(verifiedRouter(f, aiAccessPrincipal("agents", dutyAIAccessWrite)), body)
	if rec.Code != http.StatusCreated || f.aiAccess[0].RecordIDs[0] != "INV/2026/001" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRecordAIAccessRefusals(t *testing.T) {
	tests := []struct {
		name, body string
		principal  tenant.Principal
		wantStatus int
		wantBody   string
	}{
		{"missing duty", aiAccessBody, aiAccessPrincipal("agents", "audit.write", "audit.read-log.write"), http.StatusForbidden, "AUTH-0003"},
		{"identity headers only", aiAccessBody, aiAccessPrincipal("", dutyAIAccessWrite), http.StatusForbidden, "AUD-0007"},
		{"names the user", strings.Replace(aiAccessBody, `"kind"`, `"userId":"bob","kind"`, 1), aiAccessPrincipal("agents", dutyAIAccessWrite), http.StatusUnprocessableEntity, "AUD-0002"},
		{"names the actor", strings.Replace(aiAccessBody, `"kind"`, `"actor":"user:bob","kind"`, 1), aiAccessPrincipal("agents", dutyAIAccessWrite), http.StatusUnprocessableEntity, "AUD-0002"},
		{"service actor", aiAccessBody, func() tenant.Principal {
			p := aiAccessPrincipal("agents", dutyAIAccessWrite)
			p.UserID = tenant.ServiceActor("search-index")
			return p
		}(), http.StatusUnprocessableEntity, "AUD-0006"},
		{"id with spaces", strings.Replace(aiAccessBody, `"inv-1"`, `"what is the total"`, 1), aiAccessPrincipal("agents", dutyAIAccessWrite), http.StatusBadRequest, `"recordIds"`},
		{"unknown decision", strings.Replace(aiAccessBody, `"REDACTED"`, `"MAYBE"`, 1), aiAccessPrincipal("agents", dutyAIAccessWrite), http.StatusBadRequest, `"decision"`},
		{"empty tool", strings.Replace(aiAccessBody, `"depot.ap.invoice.list"`, `""`, 1), aiAccessPrincipal("agents", dutyAIAccessWrite), http.StatusBadRequest, `"tool"`},
		{"policy version zero", strings.Replace(aiAccessBody, `"policyVersion":3`, `"policyVersion":0`, 1), aiAccessPrincipal("agents", dutyAIAccessWrite), http.StatusBadRequest, `"policyVersion"`},
		{"class count differs", strings.Replace(aiAccessBody, `["NONE","CUI"]`, `["NONE"]`, 1), aiAccessPrincipal("agents", dutyAIAccessWrite), http.StatusBadRequest, `"recordClasses"`},
		{"negative count", strings.Replace(aiAccessBody, `"email":2`, `"email":-1`, 1), aiAccessPrincipal("agents", dutyAIAccessWrite), http.StatusBadRequest, `"redactionCounts"`},
		{"future time", strings.Replace(aiAccessBody, `"traceId"`, `"occurredAt":"`+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)+`","traceId"`, 1), aiAccessPrincipal("agents", dutyAIAccessWrite), http.StatusBadRequest, `"occurredAt"`},
		{"export kind", strings.Replace(aiAccessBody, `"READ"`, `"EXPORT"`, 1), aiAccessPrincipal("agents", dutyAIAccessWrite), http.StatusBadRequest, `"kind"`},
		{"ids as a string", strings.Replace(aiAccessBody, `["inv-1","inv-2"]`, `"inv-1"`, 1), aiAccessPrincipal("agents", dutyAIAccessWrite), http.StatusBadRequest, "COM-0001"},
		{"201 ids", strings.Replace(aiAccessBody, `["inv-1","inv-2"]`, `[`+strings.TrimSuffix(strings.Repeat(`"i",`, 201), ",")+`]`, 1), aiAccessPrincipal("agents", dutyAIAccessWrite), http.StatusBadRequest, `"recordIds"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			rec := postAIAccess(verifiedRouter(f, tc.principal), tc.body)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) || len(f.aiAccess) != 0 {
				t.Fatalf("got %d %s stored %d; want %d containing %s", rec.Code, rec.Body.String(), len(f.aiAccess), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestRecordAIAccessWithoutATenantIsRefused(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	if rec := do(r, http.MethodPost, aiAccessPath, aiAccessBody, "", false); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "AUTH-0001") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestRecordAIAccessStoreFailureIsCounted(t *testing.T) {
	r, a := verifiedAPI(&fakeStore{err: context.DeadlineExceeded}, aiAccessPrincipal("agents", dutyAIAccessWrite))
	if rec := postAIAccess(r, aiAccessBody); rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	w := httptest.NewRecorder()
	a.Metrics().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(w.Body.String(), "audit_ai_data_access_failed_total 1") {
		t.Fatalf("metrics %s", w.Body.String())
	}
}

const dutyAIAccessWrite = "audit.ai-access.write"
