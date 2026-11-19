package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/domain"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/store"
)

type fakeNeedToKnow struct {
	settings store.NeedToKnowSettings
	readErr  error
	setCalls int
	last     store.NeedToKnowInput
}

func (f *fakeStore) GetNeedToKnow(context.Context) (store.NeedToKnowSettings, error) {
	out := f.needToKnow.settings
	if out.Overrides == nil {
		out.Overrides = []store.NeedToKnowSetting{}
	}
	return out, f.needToKnow.readErr
}

func (f *fakeStore) SetNeedToKnow(_ context.Context, in store.NeedToKnowInput, at time.Time) (store.NeedToKnowSetting, error) {
	f.needToKnow.setCalls++
	f.needToKnow.last = in
	return store.NeedToKnowSetting{SettingID: ids.New(), LegalEntityID: in.LegalEntityID, Enabled: in.Enabled, Reason: in.Reason, ChangedAt: at, Version: in.ExpectedVersion + 1}, nil
}

const needToKnowURL = "/v1/compliance/need-to-know"

func needToKnowPut(body any, le string, ifMatchValue string, duties ...string) call {
	headers := map[string]string{}
	if ifMatchValue != "" {
		headers["If-Match"] = ifMatchValue
	}
	return call{method: http.MethodPut, path: needToKnowURL, body: body, duties: duties, legalEntity: le, headers: headers}
}

func needToKnowGet(query string, duties ...string) call {
	return call{method: http.MethodGet, path: needToKnowURL + query, duties: duties}
}

func TestNeedToKnowGet_DeploymentDefaultFollowsTheBoundaryAndCarriesTheNeverSetETag(t *testing.T) {
	for boundary, want := range map[domain.Boundary]bool{domain.BoundaryCUI: true, domain.BoundaryStandard: false} {
		rec := serve(t, router(boundaryAPI(&fakeStore{}, boundary)), needToKnowGet("", domain.DutyCUIPolicyRead))
		var out struct {
			Enabled   bool
			Source    string
			Overrides json.RawMessage
			Enforced  *bool
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Enabled != want || out.Source != domain.SourceDeploymentDefault || string(out.Overrides) != "[]" || out.Enforced == nil || *out.Enforced {
			t.Fatalf("%s: %d %s", boundary, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("ETag") != `"1"` {
			t.Fatalf("%s: ETag %q", boundary, rec.Header().Get("ETag"))
		}
	}
}

func TestNeedToKnowGet_OverrideWinsForItsLegalEntityOnly(t *testing.T) {
	le, other := ids.New(), ids.New()
	f := &fakeStore{}
	f.needToKnow.settings = store.NeedToKnowSettings{
		Tenant:    &store.NeedToKnowSetting{Enabled: true, Version: 4},
		Overrides: []store.NeedToKnowSetting{{LegalEntityID: le, Enabled: false, Version: 7}},
	}
	h := router(boundaryAPI(f, domain.BoundaryStandard))
	for query, want := range map[string]struct {
		enabled bool
		source  string
		etag    string
	}{
		"":                        {true, domain.SourceTenant, `"4"`},
		"?legalEntityId=" + le:    {false, domain.SourceLegalEntity, `"7"`},
		"?legalEntityId=" + other: {true, domain.SourceTenant, `"1"`},
	} {
		c := needToKnowGet(query, domain.DutyCUIPolicyRead, tenant.DutyTenantAdmin)
		rec := serve(t, h, c)
		var out struct {
			Enabled bool
			Source  string
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Enabled != want.enabled || out.Source != want.source || rec.Header().Get("ETag") != want.etag {
			t.Errorf("%q: %d %s etag %s", query, rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
		}
	}
}

func TestNeedToKnowGet_Refusals(t *testing.T) {
	for name, tc := range map[string]struct {
		c      call
		store  *fakeStore
		status int
		code   string
	}{
		"without the read duty":             {needToKnowGet("", domain.DutyCUIPolicyAdmin), &fakeStore{}, http.StatusForbidden, "AUTH-0003"},
		"legalEntityId not a UUID":          {needToKnowGet("?legalEntityId=x", domain.DutyCUIPolicyRead), &fakeStore{}, http.StatusBadRequest, "COM-0002"},
		"another legal entity than claimed": {needToKnowGet("?legalEntityId="+ids.New(), domain.DutyCUIPolicyRead), &fakeStore{}, http.StatusForbidden, "AUTH-0003"},
		"store down":                        {needToKnowGet("", domain.DutyCUIPolicyRead), &fakeStore{needToKnow: fakeNeedToKnow{readErr: errors.New("connection refused: db.internal:5432")}}, http.StatusInternalServerError, "COM-0500"},
	} {
		tc.c.legalEntity = testLE
		rec := serve(t, router(boundaryAPI(tc.store, domain.BoundaryCUI)), tc.c)
		if rec.Code != tc.status || problemCode(t, rec) != tc.code || strings.Contains(rec.Body.String(), "db.internal") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestNeedToKnowPut_RefusalsNeverReachTheStore(t *testing.T) {
	good := map[string]any{"enabled": true, "reason": "contract requires it"}
	with := func(k string, v any) map[string]any {
		out := map[string]any{}
		for key, value := range good {
			out[key] = value
		}
		out[k] = v
		return out
	}
	admin := domain.DutyCUIPolicyAdmin
	for name, tc := range map[string]struct {
		c      call
		status int
		code   string
	}{
		"holds only the read duty":               {needToKnowPut(good, "", `"1"`, domain.DutyCUIPolicyRead, tenant.DutyTenantAdmin), http.StatusForbidden, "AUTH-0003"},
		"no If-Match":                            {needToKnowPut(good, "", "", admin, tenant.DutyTenantAdmin), http.StatusPreconditionRequired, store.CodeVersionConflict},
		"If-Match abc":                           {needToKnowPut(good, "", "abc", admin, tenant.DutyTenantAdmin), http.StatusBadRequest, "COM-0002"},
		"If-Match 0":                             {needToKnowPut(good, "", "0", admin, tenant.DutyTenantAdmin), http.StatusBadRequest, "COM-0002"},
		"enabled absent":                         {needToKnowPut(map[string]any{"reason": "r"}, "", `"1"`, admin, tenant.DutyTenantAdmin), http.StatusBadRequest, "COM-0002"},
		"enabled a string":                       {needToKnowPut(with("enabled", "yes"), "", `"1"`, admin, tenant.DutyTenantAdmin), http.StatusBadRequest, "COM-0001"},
		"enabled a number":                       {needToKnowPut(with("enabled", 1), "", `"1"`, admin, tenant.DutyTenantAdmin), http.StatusBadRequest, "COM-0001"},
		"reason empty":                           {needToKnowPut(with("reason", ""), "", `"1"`, admin, tenant.DutyTenantAdmin), http.StatusBadRequest, "COM-0002"},
		"reason blank":                           {needToKnowPut(with("reason", "   "), "", `"1"`, admin, tenant.DutyTenantAdmin), http.StatusBadRequest, "COM-0002"},
		"reason 501 characters":                  {needToKnowPut(with("reason", strings.Repeat("x", 501)), "", `"1"`, admin, tenant.DutyTenantAdmin), http.StatusBadRequest, "COM-0002"},
		"reason a number":                        {needToKnowPut(with("reason", 7), "", `"1"`, admin, tenant.DutyTenantAdmin), http.StatusBadRequest, "COM-0001"},
		"legalEntityId not a UUID":               {needToKnowPut(with("legalEntityId", "x"), "", `"1"`, admin, tenant.DutyTenantAdmin), http.StatusBadRequest, "COM-0002"},
		"legalEntityId a number":                 {needToKnowPut(with("legalEntityId", 3), "", `"1"`, admin, tenant.DutyTenantAdmin), http.StatusBadRequest, "COM-0001"},
		"unknown field":                          {needToKnowPut(with("scope", "TENANT"), "", `"1"`, admin, tenant.DutyTenantAdmin), http.StatusBadRequest, "COM-0001"},
		"tenant level without administer":        {needToKnowPut(good, "", `"1"`, admin), http.StatusForbidden, "AUTH-0003"},
		"tenant level with a legal-entity claim": {needToKnowPut(good, testLE, `"1"`, admin, tenant.DutyTenantAdmin), http.StatusForbidden, "AUTH-0003"},
		"scoped admin turns OFF":                 {needToKnowPut(map[string]any{"enabled": false, "reason": "r", "legalEntityId": testLE}, testLE, `"1"`, admin), http.StatusForbidden, "AUTH-0003"},
		"scoped admin, another entity":           {needToKnowPut(with("legalEntityId", ids.New()), testLE, `"1"`, admin), http.StatusForbidden, "AUTH-0003"},
	} {
		f := &fakeStore{}
		rec := serve(t, router(boundaryAPI(f, domain.BoundaryCUI)), tc.c)
		if rec.Code != tc.status || problemCode(t, rec) != tc.code || f.needToKnow.setCalls != 0 {
			t.Errorf("%s: %d %s, store calls %d", name, rec.Code, rec.Body.String(), f.needToKnow.setCalls)
		}
	}
}

func TestNeedToKnowPut_AuthorisedChangesReachTheStoreWithTheGetETag(t *testing.T) {
	scoped := domain.DutyCUIPolicyAdmin
	for name, tc := range map[string]struct {
		c    call
		want store.NeedToKnowInput
	}{
		"tenant-wide admin sets the tenant level": {
			needToKnowPut(map[string]any{"enabled": false, "reason": " lifted "}, "", `"3"`, scoped, tenant.DutyTenantAdmin),
			store.NeedToKnowInput{Enabled: false, Reason: " lifted ", ExpectedVersion: 3},
		},
		"tenant-wide admin sets any legal entity off": {
			needToKnowPut(map[string]any{"enabled": false, "reason": "r", "legalEntityId": strings.ToUpper(testLE)}, "", `"1"`, scoped, tenant.DutyTenantAdmin),
			store.NeedToKnowInput{LegalEntityID: testLE, Enabled: false, Reason: "r", ExpectedVersion: 1},
		},
		"scoped admin tightens its own entity": {
			needToKnowPut(map[string]any{"enabled": true, "reason": "r", "legalEntityId": testLE}, testLE, `"1"`, scoped),
			store.NeedToKnowInput{LegalEntityID: testLE, Enabled: true, Reason: "r", ExpectedVersion: 1},
		},
	} {
		f := &fakeStore{}
		rec := serve(t, router(boundaryAPI(f, domain.BoundaryCUI)), tc.c)
		if rec.Code != http.StatusOK || f.needToKnow.setCalls != 1 || f.needToKnow.last != tc.want || rec.Header().Get("ETag") != `"`+string(rune('0'+tc.want.ExpectedVersion+1))+`"` {
			t.Errorf("%s: %d %s, calls %d, last %+v", name, rec.Code, rec.Body.String(), f.needToKnow.setCalls, f.needToKnow.last)
		}
	}
}
