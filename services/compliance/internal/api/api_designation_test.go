package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/domain"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/store"
)

type fakeDesignations struct {
	calls   int
	lastKey string
	last    store.DesignationInput
}

func (f *fakeStore) Designate(_ context.Context, in store.DesignationInput, key string, at time.Time) (store.Designation, error) {
	f.designs.calls++
	f.designs.lastKey, f.designs.last = key, in
	return store.Designation{DesignationID: ids.New(), Scope: in.Scope, LegalEntityID: in.LegalEntityID, Class: in.Class, Reason: in.Reason, DesignatedAt: at, Version: 1}, nil
}

func designationCall(body any, le string, duties ...string) call {
	return call{method: http.MethodPost, path: "/v1/compliance/data-designations", body: body, duties: duties, legalEntity: le,
		headers: map[string]string{"Idempotency-Key": "k-1"}}
}

func designationBody(scope, le string) map[string]any {
	body := map[string]any{"scope": scope, "class": "CUI", "reason": "export-controlled drawings"}
	if le != "" {
		body["legalEntityId"] = le
	}
	return body
}

func boundaryAPI(f *fakeStore, boundary domain.Boundary) *API {
	a := newAPI(f, true)
	a.cfg.Boundary = boundary
	return a
}

func TestDesignation_RequiresTheWriteDuty(t *testing.T) {
	f := &fakeStore{}
	h := router(boundaryAPI(f, domain.BoundaryCUI))
	rec := serve(t, h, designationCall(designationBody("TENANT", ""), "", "compliance.view"))
	if rec.Code != http.StatusForbidden || problemCode(t, rec) != "AUTH-0003" || f.designs.calls != 0 {
		t.Fatalf("without the duty: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDesignation_MalformedTenantIs401(t *testing.T) {
	f := &fakeStore{}
	req := httptest.NewRequest(http.MethodPost, "/v1/compliance/data-designations", strings.NewReader("{}")).
		WithContext(tenant.With(context.Background(), tenant.Principal{TenantID: "not-a-uuid", Duties: map[string]struct{}{DutyDataDesignationWrite: {}}}))
	rec := httptest.NewRecorder()
	router(boundaryAPI(f, domain.BoundaryCUI)).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || problemCode(t, rec) != "AUTH-0001" || f.designs.calls != 0 {
		t.Fatalf("malformed tenant: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDesignation_InvalidRequestsAre400(t *testing.T) {
	le := ids.New()
	long := strings.Repeat("a", 501)
	for name, tc := range map[string]struct {
		body    any
		headers map[string]string
	}{
		"scope absent":                   {body: map[string]any{"class": "CUI", "reason": "r"}},
		"scope RECORD":                   {body: designationBody("RECORD", le)},
		"scope unknown":                  {body: designationBody("GLOBAL", "")},
		"scope wrong type":               {body: map[string]any{"scope": 7, "class": "CUI", "reason": "r"}},
		"legal entity missing":           {body: designationBody("LEGAL_ENTITY", "")},
		"legal entity not a uuid":        {body: designationBody("LEGAL_ENTITY", "not-a-uuid")},
		"legal entity nil uuid":          {body: designationBody("LEGAL_ENTITY", ids.Nil)},
		"legal entity on tenant scope":   {body: designationBody("TENANT", le)},
		"class absent":                   {body: map[string]any{"scope": "TENANT", "reason": "r"}},
		"class lowercase":                {body: map[string]any{"scope": "TENANT", "class": "cui", "reason": "r"}},
		"class P3":                       {body: map[string]any{"scope": "TENANT", "class": "P3", "reason": "r"}},
		"reason absent":                  {body: map[string]any{"scope": "TENANT", "class": "CUI"}},
		"reason whitespace":              {body: map[string]any{"scope": "TENANT", "class": "CUI", "reason": "   "}},
		"reason 501 characters":          {body: map[string]any{"scope": "TENANT", "class": "CUI", "reason": long}},
		"category over 100":              {body: map[string]any{"scope": "TENANT", "class": "CUI", "reason": "r", "category": strings.Repeat("c", 101)}},
		"unknown field":                  {body: map[string]any{"scope": "TENANT", "class": "CUI", "reason": "r", "extra": 1}},
		"idempotency key absent":         {body: designationBody("TENANT", ""), headers: map[string]string{}},
		"idempotency key 201 characters": {body: designationBody("TENANT", ""), headers: map[string]string{"Idempotency-Key": strings.Repeat("k", 201)}},
	} {
		f := &fakeStore{}
		c := designationCall(tc.body, "", DutyDataDesignationWrite)
		if tc.headers != nil {
			c.headers = tc.headers
		}
		rec := serve(t, router(boundaryAPI(f, domain.BoundaryCUI)), c)
		if rec.Code != http.StatusBadRequest || f.designs.calls != 0 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestDesignation_StandardBoundaryRefusesEveryScopeWith409(t *testing.T) {
	le := ids.New()
	for name, boundary := range map[string]domain.Boundary{"explicit standard": domain.BoundaryStandard, "unset": ""} {
		for _, body := range []map[string]any{designationBody("TENANT", ""), designationBody("LEGAL_ENTITY", le)} {
			f := &fakeStore{}
			rec := serve(t, router(boundaryAPI(f, boundary)), designationCall(body, le, DutyDataDesignationWrite))
			if rec.Code != http.StatusConflict || problemCode(t, rec) != domain.CodeCUIBoundary || f.designs.calls != 0 {
				t.Errorf("%s %v: %d %s, store calls %d", name, body["scope"], rec.Code, rec.Body.String(), f.designs.calls)
			}
		}
	}
}

func TestDesignation_LegalEntityOutsideTheCallersScopeIs403(t *testing.T) {
	f := &fakeStore{}
	rec := serve(t, router(boundaryAPI(f, domain.BoundaryCUI)), designationCall(designationBody("LEGAL_ENTITY", ids.New()), ids.New(), DutyDataDesignationWrite))
	if rec.Code != http.StatusForbidden || problemCode(t, rec) != "AUTH-0003" || f.designs.calls != 0 {
		t.Fatalf("out of scope: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDesignation_CUIBoundaryStoresAndReturnsETag(t *testing.T) {
	le := ids.New()
	f := &fakeStore{}
	rec := serve(t, router(boundaryAPI(f, domain.BoundaryCUI)), designationCall(designationBody("LEGAL_ENTITY", strings.ToUpper(le)), le, DutyDataDesignationWrite))
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` || f.designs.calls != 1 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if f.designs.lastKey != "k-1" || f.designs.last.LegalEntityID != le || f.designs.last.Scope != domain.ScopeLegalEntity {
		t.Fatalf("store input: %+v key %q", f.designs.last, f.designs.lastKey)
	}
	tenantWide := serve(t, router(boundaryAPI(&fakeStore{}, domain.BoundaryCUI)), designationCall(designationBody("TENANT", ""), "", DutyDataDesignationWrite))
	if tenantWide.Code != http.StatusCreated {
		t.Fatalf("tenant scope: %d %s", tenantWide.Code, tenantWide.Body.String())
	}
}
