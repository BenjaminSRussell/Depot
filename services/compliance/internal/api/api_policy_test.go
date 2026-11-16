package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/store"
)

type fakePolicies struct {
	byID   map[string]store.AuditPolicy
	filter store.AuditPolicyFilter
	lastIn store.AuditPolicyInput
	key    string
}

func (f *fakeStore) CreateAuditPolicy(_ context.Context, in store.AuditPolicyInput, key string, at time.Time) (store.AuditPolicy, error) {
	f.policies.lastIn, f.policies.key = in, key
	id, version := in.PolicyID, in.Version
	if id == "" {
		id = ids.New()
	}
	if version == 0 {
		version = 1
	}
	p := store.AuditPolicy{PolicyID: id, Code: in.Code, Name: in.Name, RuleType: in.Spec.Type, Spec: in.Spec, Active: true, Version: version, RowVersion: 1, CreatedAt: at, UpdatedAt: at}
	if f.policies.byID == nil {
		f.policies.byID = map[string]store.AuditPolicy{}
	}
	f.policies.byID[p.PolicyID] = p
	return p, nil
}

func (f *fakeStore) GetAuditPolicy(_ context.Context, id string) (store.AuditPolicy, error) {
	p, ok := f.policies.byID[id]
	if !ok {
		return p, httpx.NewProblem(http.StatusNotFound, store.CodeAuditPolicyNotFound, "no audit policy with that id")
	}
	return p, nil
}

func (f *fakeStore) ListAuditPolicies(_ context.Context, filter store.AuditPolicyFilter) ([]store.AuditPolicy, error) {
	f.policies.filter = filter
	return []store.AuditPolicy{}, nil
}

func (f *fakeStore) UpdateAuditPolicy(_ context.Context, id string, expected int, in store.AuditPolicyPatch, _ time.Time) (store.AuditPolicy, error) {
	p, ok := f.policies.byID[id]
	if !ok {
		return p, httpx.NewProblem(http.StatusNotFound, store.CodeAuditPolicyNotFound, "no audit policy with that id")
	}
	if p.RowVersion != expected {
		return p, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	p.RowVersion++
	if in.Spec != nil {
		p.Spec, p.RuleType, p.Version = *in.Spec, in.Spec.Type, p.Version+1
	}
	if in.Active != nil {
		p.Active = *in.Active
	}
	f.policies.byID[id] = p
	return p, nil
}

func policyBody(code string, spec domain.AuditSpec) map[string]any {
	return map[string]any{"code": code, "name": "Gift cards", "spec": spec}
}

func TestAuditPolicies_CreateValidatesTheRuleAndNeedsTheMaintainDuty(t *testing.T) {
	f := &fakeStore{}
	h := router(newAPI(f, true))
	keyed := map[string]string{"Idempotency-Key": "k1"}
	ok := serve(t, h, call{method: http.MethodPost, path: "/v1/compliance/audit-policies", body: policyBody(" gift-cards ", domain.AuditSpec{Type: "KEYWORD", Keywords: []string{"gift card"}}),
		duties: []string{DutyAuditPolicyMaintain}, headers: keyed})
	if ok.Code != http.StatusCreated || f.policies.lastIn.Code != "GIFT-CARDS" || f.policies.key != "k1" || ok.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s (stored %+v)", ok.Code, ok.Body, f.policies.lastIn)
	}
	for name, body := range map[string]map[string]any{
		"unconfigured rule": policyBody("EMPTY", domain.AuditSpec{Type: "KEYWORD"}),
		"unknown rule type": policyBody("ODD", domain.AuditSpec{Type: "MAGIC"}),
		"no spec type":      policyBody("NOTYPE", domain.AuditSpec{}),
		"bad code":          policyBody("x", domain.AuditSpec{Type: "DUPLICATE"}),
	} {
		rec := serve(t, h, call{method: http.MethodPost, path: "/v1/compliance/audit-policies", body: body, duties: []string{DutyAuditPolicyMaintain}, headers: keyed})
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := serve(t, h, call{method: http.MethodPost, path: "/v1/compliance/audit-policies", body: policyBody("OK", domain.AuditSpec{Type: "DUPLICATE"}), duties: []string{DutyAuditPolicyRun}, headers: keyed}); rec.Code != http.StatusForbidden {
		t.Fatalf("the run duty must not create: %d", rec.Code)
	}
	if rec := serve(t, h, call{method: http.MethodPost, path: "/v1/compliance/audit-policies", body: policyBody("OK", domain.AuditSpec{Type: "DUPLICATE"}), duties: []string{DutyAuditPolicyMaintain}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no Idempotency-Key: %d", rec.Code)
	}
}

func TestAuditPolicies_ListGetAndPatch(t *testing.T) {
	f := &fakeStore{}
	h := router(newAPI(f, true))
	for _, duty := range []string{DutyAuditPolicyRun, DutyAuditPolicyMaintain} {
		if rec := serve(t, h, call{method: http.MethodGet, path: "/v1/compliance/audit-policies?q=+gift+&code=gc&active=true&limit=5", duties: []string{duty}}); rec.Code != http.StatusOK {
			t.Fatalf("%s list: %d %s", duty, rec.Code, rec.Body)
		}
	}
	if fl := f.policies.filter; fl.Q != "gift" || fl.Code != "GC" || !fl.ActiveOnly || fl.Limit != 5 {
		t.Fatalf("filter %+v", fl)
	}
	if rec := serve(t, h, call{method: http.MethodGet, path: "/v1/compliance/audit-policies?limit=0", duties: []string{DutyAuditPolicyRun}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("limit 0: %d", rec.Code)
	}
	created, _ := f.CreateAuditPolicy(context.Background(), store.AuditPolicyInput{Code: "GC", Name: "x", Spec: domain.AuditSpec{Type: "DUPLICATE"}}, "k", now)
	path := "/v1/compliance/audit-policies/" + created.PolicyID
	if rec := serve(t, h, call{method: http.MethodGet, path: path, duties: []string{DutyAuditPolicyRun}}); rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	if rec := serve(t, h, call{method: http.MethodGet, path: "/v1/compliance/audit-policies/" + ids.New(), duties: []string{DutyAuditPolicyRun}}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", rec.Code)
	}
	if rec := serve(t, h, call{method: http.MethodPatch, path: path, body: map[string]any{"active": false}, duties: []string{DutyAuditPolicyMaintain}}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := serve(t, h, call{method: http.MethodPatch, path: path, body: map[string]any{"active": false}, duties: []string{DutyAuditPolicyMaintain}, headers: map[string]string{"If-Match": `"4"`}}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale: %d", rec.Code)
	}
	if rec := serve(t, h, call{method: http.MethodPatch, path: path, body: map[string]any{}, duties: []string{DutyAuditPolicyMaintain}, headers: map[string]string{"If-Match": `"1"`}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty patch: %d", rec.Code)
	}
	spec := domain.AuditSpec{Type: "THRESHOLD", Currency: "USD", Limits: map[string]decimal.Decimal{"MEALS": decimal.NewFromInt(200)}}
	rec := serve(t, h, call{method: http.MethodPatch, path: path, body: map[string]any{"spec": spec}, duties: []string{DutyAuditPolicyMaintain}, headers: map[string]string{"If-Match": `"1"`}})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` || !strings.Contains(rec.Body.String(), `"ruleType":"THRESHOLD"`) {
		t.Fatalf("respec: %d %s", rec.Code, rec.Body)
	}
}

func TestEvaluateAudit_AStoredPolicyRunsWithoutASpec(t *testing.T) {
	f := &fakeStore{}
	h := router(newAPI(f, true))
	policy, _ := f.CreateAuditPolicy(context.Background(), store.AuditPolicyInput{Code: "GC", Name: "Gift cards", Spec: domain.AuditSpec{Type: "KEYWORD", Keywords: []string{"gift card"}}}, "k", now)
	p := f.policies.byID[policy.PolicyID]
	p.Version = 3
	f.policies.byID[policy.PolicyID] = p
	body := map[string]any{"policyId": policy.PolicyID, "documents": []map[string]any{{"id": "d1", "texts": []string{"Bought a Gift Card"}}, {"id": "d2", "texts": []string{"Paper"}}}}
	rec := serve(t, h, call{method: http.MethodPost, path: "/v1/compliance/audit-policies:evaluate", body: body, duties: []string{DutyAuditPolicyRun}})
	var out AuditEvaluateResponse
	decodeInto(t, rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || out.Count != 1 || out.PolicyVersion != 3 || out.Findings[0].DocumentID != "d1" || !strings.Contains(out.Findings[0].Key, "3") {
		t.Fatalf("stored policy run: %d %s", rec.Code, rec.Body)
	}
	retired := false
	f.UpdateAuditPolicy(context.Background(), policy.PolicyID, 1, store.AuditPolicyPatch{Active: &retired}, now)
	if rec := serve(t, h, call{method: http.MethodPost, path: "/v1/compliance/audit-policies:evaluate", body: body, duties: []string{DutyAuditPolicyRun}}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeAuditPolicyRetired) {
		t.Fatalf("retired: %d %s", rec.Code, rec.Body)
	}
	body["policyId"] = ids.New()
	if rec := serve(t, h, call{method: http.MethodPost, path: "/v1/compliance/audit-policies:evaluate", body: body, duties: []string{DutyAuditPolicyRun}}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown policy: %d", rec.Code)
	}
}

func TestEvaluateAudit_AStoredPolicyRefusesACallersRuleAndAnOldIdStillRunsItsOwn(t *testing.T) {
	f := &fakeStore{}
	h := router(newAPI(f, true))
	policy, _ := f.CreateAuditPolicy(context.Background(), store.AuditPolicyInput{Code: "GC", Name: "x", Spec: domain.AuditSpec{Type: "DUPLICATE"}}, "k", now)
	docs := []map[string]any{{"id": "d1"}}
	for name, body := range map[string]map[string]any{
		"a spec":    {"policyId": policy.PolicyID, "spec": map[string]any{"type": "KEYWORD", "keywords": []string{"x"}}, "version": 1, "documents": docs},
		"a version": {"policyId": policy.PolicyID, "version": 1, "documents": docs},
	} {
		if rec := serve(t, h, call{method: http.MethodPost, path: "/v1/compliance/audit-policies:evaluate", body: body, duties: []string{DutyAuditPolicyRun}}); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s under a stored policy: %d %s", name, rec.Code, rec.Body)
		}
	}
	// An id no policy holds keeps running the rule the request carries.
	adhoc := map[string]any{"policyId": ids.New(), "version": 1, "spec": map[string]any{"type": "KEYWORD", "keywords": []string{"x"}}, "documents": docs}
	if rec := serve(t, h, call{method: http.MethodPost, path: "/v1/compliance/audit-policies:evaluate", body: adhoc, duties: []string{DutyAuditPolicyRun}}); rec.Code != http.StatusOK {
		t.Fatalf("ad hoc run: %d %s", rec.Code, rec.Body)
	}
}

func TestAuditPolicies_RenameAndRetireMoveTheETagAndAPolicyCanBeRegisteredUnderAnOldId(t *testing.T) {
	f := &fakeStore{}
	h := router(newAPI(f, true))
	old := ids.New()
	rec := serve(t, h, call{method: http.MethodPost, path: "/v1/compliance/audit-policies", body: map[string]any{"policyId": old, "version": 3, "code": "OLD-RULE", "name": "Old", "spec": domain.AuditSpec{Type: "DUPLICATE"}},
		duties: []string{DutyAuditPolicyMaintain}, headers: map[string]string{"Idempotency-Key": "k"}})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), old) || !strings.Contains(rec.Body.String(), `"version":3`) {
		t.Fatalf("register under an old id: %d %s", rec.Code, rec.Body)
	}
	bad := serve(t, h, call{method: http.MethodPost, path: "/v1/compliance/audit-policies", body: map[string]any{"policyId": "nope", "code": "X1", "name": "x", "spec": domain.AuditSpec{Type: "DUPLICATE"}},
		duties: []string{DutyAuditPolicyMaintain}, headers: map[string]string{"Idempotency-Key": "k2"}})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", bad.Code)
	}
	path := "/v1/compliance/audit-policies/" + old
	patch := func(etag string, body map[string]any) *httptest.ResponseRecorder {
		return serve(t, h, call{method: http.MethodPatch, path: path, body: body, duties: []string{DutyAuditPolicyMaintain}, headers: map[string]string{"If-Match": etag}})
	}
	first := patch(`"1"`, map[string]any{"name": "Renamed"})
	if first.Code != http.StatusOK || first.Header().Get("ETag") != `"2"` || !strings.Contains(first.Body.String(), `"version":3`) {
		t.Fatalf("rename: %d etag %q %s", first.Code, first.Header().Get("ETag"), first.Body)
	}
	if stale := patch(`"1"`, map[string]any{"name": "Lost update"}); stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("a second writer with the old ETag: %d", stale.Code)
	}
}
