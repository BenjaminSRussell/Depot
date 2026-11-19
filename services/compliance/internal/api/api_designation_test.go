package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/domain"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/store"
)

type fakeDesignations struct {
	calls        int
	lastKey      string
	last         store.DesignationInput
	resolveCalls int
	resolved     []store.ResolveRef
	cuiRecords   map[string]bool
	resolveErr   error
	tenantCUI    bool
	changeCalls  int
	changedID    string
	changedIn    store.DesignationChange
	changeErr    error
	history      []store.DesignationHistoryEntry
	listed       store.DesignationFilter
}

func (f *fakeStore) Resolve(_ context.Context, refs []store.ResolveRef) ([]store.ResolvedRef, error) {
	f.designs.resolveCalls++
	f.designs.resolved = refs
	if f.designs.resolveErr != nil {
		return nil, f.designs.resolveErr
	}
	out := make([]store.ResolvedRef, len(refs))
	for i, ref := range refs {
		out[i] = store.ResolvedRef{EntityType: ref.EntityType, RecordID: ref.RecordID, Class: domain.ClassNONE, Source: domain.SourceDefault}
		if f.designs.tenantCUI {
			out[i].Class, out[i].Source = domain.ClassCUI, domain.SourceTenant
		}
		if f.designs.cuiRecords[ref.RecordID] {
			out[i].Class, out[i].Source = domain.ClassCUI, domain.SourceRecord
		}
	}
	return out, nil
}

type fakeOwner struct {
	mu      sync.Mutex
	calls   int
	legal   map[string]string
	failFor map[string]bool
}

func (o *fakeOwner) LegalEntity(_ context.Context, _, recordID string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	if o.failFor[recordID] {
		return "", errOwnerUnavailable
	}
	if le, ok := o.legal[recordID]; ok {
		return le, nil
	}
	return "", ErrOwnerNotFound
}

func ownerAPI(f *fakeStore, owner OwnerLookup) *API {
	a := boundaryAPI(f, domain.BoundaryCUI)
	a.cfg.Owner = owner
	return a
}

func recordBody(recordID string) map[string]any {
	return map[string]any{"scope": "RECORD", "entityType": "project", "recordId": recordID, "class": "CUI", "reason": "export-controlled drawings"}
}

func resolveCall(refs any, duties ...string) call {
	return call{method: http.MethodPost, path: "/v1/compliance/data-designations:resolve", body: map[string]any{"refs": refs}, duties: duties, legalEntity: testLE}
}

func refsOf(entityType string, recordIDs ...string) []map[string]string {
	refs := make([]map[string]string, len(recordIDs))
	for i, id := range recordIDs {
		refs[i] = map[string]string{"entityType": entityType, "recordId": id}
	}
	return refs
}

type resolveItem struct{ EntityType, RecordID, Class, Source string }

func resolveItems(t *testing.T, rec *httptest.ResponseRecorder) []resolveItem {
	t.Helper()
	var body struct{ Items []resolveItem }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		t.Fatalf("resolve: %d %s", rec.Code, rec.Body.String())
	}
	return body.Items
}

var testLE = ids.New()

func (f *fakeStore) Designate(_ context.Context, in store.DesignationInput, key string, at time.Time) (store.Designation, error) {
	f.designs.calls++
	f.designs.lastKey, f.designs.last = key, in
	return store.Designation{DesignationID: ids.New(), Scope: in.Scope, LegalEntityID: in.LegalEntityID, EntityType: in.EntityType, RecordID: in.RecordID, Class: in.Class, Reason: in.Reason, DesignatedAt: at, Version: 1}, nil
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
		"scope RECORD with legal entity": {body: map[string]any{"scope": "RECORD", "entityType": "project", "recordId": ids.New(), "legalEntityId": le, "class": "CUI", "reason": "r"}},
		"record id absent":               {body: map[string]any{"scope": "RECORD", "entityType": "project", "class": "CUI", "reason": "r"}},
		"record id not a uuid":           {body: recordBody("PRJ-1")},
		"record id nil uuid":             {body: recordBody(ids.Nil)},
		"record id wrong type":           {body: map[string]any{"scope": "RECORD", "entityType": "project", "recordId": 7, "class": "CUI", "reason": "r"}},
		"entity type absent":             {body: map[string]any{"scope": "RECORD", "recordId": ids.New(), "class": "CUI", "reason": "r"}},
		"entity type unsupported":        {body: map[string]any{"scope": "RECORD", "entityType": "contract", "recordId": ids.New(), "class": "CUI", "reason": "r"}},
		"entity type on tenant scope":    {body: map[string]any{"scope": "TENANT", "entityType": "project", "class": "CUI", "reason": "r"}},
		"record id on legal entity":      {body: map[string]any{"scope": "LEGAL_ENTITY", "legalEntityId": le, "recordId": ids.New(), "class": "CUI", "reason": "r"}},
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
		for _, body := range []map[string]any{designationBody("TENANT", ""), designationBody("LEGAL_ENTITY", le), recordBody(ids.New())} {
			f, owner := &fakeStore{}, &fakeOwner{}
			a := boundaryAPI(f, boundary)
			a.cfg.Owner = owner
			caller := designationCall(body, le, DutyDataDesignationWrite)
			if body["scope"] == "TENANT" {
				caller = designationCall(body, "", DutyDataDesignationWrite, tenant.DutyTenantAdmin)
			}
			rec := serve(t, router(a), caller)
			if rec.Code != http.StatusConflict || problemCode(t, rec) != domain.CodeCUIBoundary || f.designs.calls != 0 || owner.calls != 0 {
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
	tenantWide := serve(t, router(boundaryAPI(&fakeStore{}, domain.BoundaryCUI)), designationCall(designationBody("TENANT", ""), "", DutyDataDesignationWrite, tenant.DutyTenantAdmin))
	if tenantWide.Code != http.StatusCreated {
		t.Fatalf("tenant scope: %d %s", tenantWide.Code, tenantWide.Body.String())
	}
}

func TestDesignation_CategoryLimitCountsCharactersNotBytes(t *testing.T) {
	f := &fakeStore{}
	body := map[string]any{"scope": "TENANT", "class": "CUI", "reason": "r", "category": strings.Repeat("я", 100)}
	rec := serve(t, router(boundaryAPI(f, domain.BoundaryCUI)), designationCall(body, "", DutyDataDesignationWrite, tenant.DutyTenantAdmin))
	if rec.Code != http.StatusCreated || f.designs.calls != 1 {
		t.Fatalf("100-rune category: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDesignation_RecordScopeTakesTheLegalEntityFromTheOwner(t *testing.T) {
	record := ids.New()
	f := &fakeStore{}
	a := ownerAPI(f, &fakeOwner{legal: map[string]string{record: testLE}})
	rec := serve(t, router(a), designationCall(recordBody(strings.ToUpper(record)), testLE, DutyDataDesignationWrite))
	if rec.Code != http.StatusCreated || f.designs.calls != 1 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.designs.last; got.LegalEntityID != testLE || got.EntityType != "project" || got.RecordID != record || got.Scope != domain.ScopeRecord {
		t.Fatalf("store input: %+v", got)
	}
}

func TestDesignation_RecordOutsideTheCallersScopeOrUnknownIs404(t *testing.T) {
	known, unknown := ids.New(), ids.New()
	owner := &fakeOwner{legal: map[string]string{known: ids.New()}}
	var bodies []string
	for _, record := range []string{known, unknown} {
		f := &fakeStore{}
		rec := serve(t, router(ownerAPI(f, owner)), designationCall(recordBody(record), testLE, DutyDataDesignationWrite))
		if rec.Code != http.StatusNotFound || problemCode(t, rec) != domain.CodeRecordNotFound || f.designs.calls != 0 {
			t.Fatalf("%s: %d %s", record, rec.Code, rec.Body.String())
		}
		var problem struct{ Detail string }
		_ = json.Unmarshal(rec.Body.Bytes(), &problem)
		bodies = append(bodies, problem.Detail)
	}
	if bodies[0] != bodies[1] {
		t.Fatalf("an out-of-scope record must look like an unknown one: %q vs %q", bodies[0], bodies[1])
	}
}

func TestDesignation_RecordScopeFailsClosedWhenTheOwnerIsUnavailable(t *testing.T) {
	record := ids.New()
	for name, owner := range map[string]OwnerLookup{"owner error": &fakeOwner{failFor: map[string]bool{record: true}}, "no owner client": nil} {
		f := &fakeStore{}
		rec := serve(t, router(ownerAPI(f, owner)), designationCall(recordBody(record), testLE, DutyDataDesignationWrite))
		if rec.Code != http.StatusServiceUnavailable || problemCode(t, rec) != domain.CodeOwnerUnavailable || f.designs.calls != 0 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestResolve_RequiresTheReadDuty(t *testing.T) {
	f := &fakeStore{}
	for _, duty := range []string{DutyDataDesignationWrite, "compliance.view"} {
		rec := serve(t, router(ownerAPI(f, &fakeOwner{})), resolveCall(refsOf("project", ids.New()), duty))
		if rec.Code != http.StatusForbidden || problemCode(t, rec) != "AUTH-0003" || f.designs.resolveCalls != 0 {
			t.Fatalf("%s: %d %s", duty, rec.Code, rec.Body.String())
		}
	}
}

func TestResolve_TheServiceResolveDutyReachesOnlyTheResolveRoute(t *testing.T) {
	f := &fakeStore{}
	ok := serve(t, router(ownerAPI(f, &fakeOwner{})), resolveCall(refsOf("project", ids.New()), DutyDataDesignationResolve))
	if ok.Code != http.StatusOK {
		t.Fatalf("resolve with the service duty: %d %s", ok.Code, ok.Body.String())
	}
	for _, c := range []call{
		{method: http.MethodGet, path: "/v1/compliance/data-designations", duties: []string{DutyDataDesignationResolve}, legalEntity: testLE},
		{method: http.MethodGet, path: "/v1/compliance/data-designations/" + ids.New(), duties: []string{DutyDataDesignationResolve}, legalEntity: testLE},
	} {
		rec := serve(t, router(ownerAPI(f, &fakeOwner{})), c)
		if rec.Code != http.StatusForbidden || problemCode(t, rec) != "AUTH-0003" {
			t.Fatalf("%s %s with the resolve duty: %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

func TestResolve_MalformedTenantIs401(t *testing.T) {
	f := &fakeStore{}
	req := httptest.NewRequest(http.MethodPost, "/v1/compliance/data-designations:resolve", strings.NewReader(`{"refs":[]}`)).
		WithContext(tenant.With(context.Background(), tenant.Principal{TenantID: "not-a-uuid", Duties: map[string]struct{}{DutyDataDesignationRead: {}}}))
	rec := httptest.NewRecorder()
	router(ownerAPI(f, &fakeOwner{})).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || problemCode(t, rec) != "AUTH-0001" || f.designs.resolveCalls != 0 {
		t.Fatalf("malformed tenant: %d %s", rec.Code, rec.Body.String())
	}
}

func TestResolve_RefCountBounds(t *testing.T) {
	record := ids.New()
	owner := &fakeOwner{legal: map[string]string{record: testLE}}
	many := func(n int) []map[string]string {
		refs := make([]map[string]string, n)
		for i := range refs {
			refs[i] = map[string]string{"entityType": "project", "recordId": record}
		}
		return refs
	}
	for name, tc := range map[string]struct {
		refs   any
		status int
		code   string
	}{
		"500 refs":       {many(500), http.StatusOK, ""},
		"501 refs":       {many(501), http.StatusUnprocessableEntity, domain.CodeTooManyRefs},
		"empty":          {[]any{}, http.StatusBadRequest, codeInvalid},
		"null":           {nil, http.StatusBadRequest, codeInvalid},
		"a string":       {"project", http.StatusBadRequest, "COM-0001"},
		"an object":      {map[string]string{"entityType": "project"}, http.StatusBadRequest, "COM-0001"},
		"wrong typed id": {[]map[string]any{{"entityType": "project", "recordId": 7}}, http.StatusBadRequest, "COM-0001"},
	} {
		f := &fakeStore{}
		rec := serve(t, router(ownerAPI(f, owner)), resolveCall(tc.refs, DutyDataDesignationRead))
		if rec.Code != tc.status || (tc.code != "" && problemCode(t, rec) != tc.code) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestResolve_ReturnsClassAndSourceInRequestOrder(t *testing.T) {
	designated, plain := ids.New(), ids.New()
	f := &fakeStore{designs: fakeDesignations{cuiRecords: map[string]bool{designated: true}}}
	owner := &fakeOwner{legal: map[string]string{designated: testLE, plain: testLE}}
	rec := serve(t, router(ownerAPI(f, owner)), resolveCall(refsOf("project", plain, strings.ToUpper(designated), plain), DutyDataDesignationRead))
	got := resolveItems(t, rec)
	want := []resolveItem{
		{"project", plain, domain.ClassNONE, domain.SourceDefault},
		{"project", strings.ToUpper(designated), domain.ClassCUI, domain.SourceRecord},
		{"project", plain, domain.ClassNONE, domain.SourceDefault},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("items %v, want %v", got, want)
	}
	if len(f.designs.resolved) != 2 || owner.calls != 2 || f.designs.resolveCalls != 1 {
		t.Fatalf("duplicates must cost one lookup each: %d store refs, %d owner calls, %d store calls", len(f.designs.resolved), owner.calls, f.designs.resolveCalls)
	}
}

func TestResolve_UnknownRefsAreIndistinguishable(t *testing.T) {
	foreign, missing, known := ids.New(), ids.New(), ids.New()
	owner := &fakeOwner{legal: map[string]string{foreign: ids.New(), known: testLE}}
	f := &fakeStore{}
	refs := append(refsOf("project", foreign, missing, "not-a-uuid", ids.Nil), refsOf("contract", known)...)
	refs = append(refs, map[string]string{"recordId": known})
	got := resolveItems(t, serve(t, router(ownerAPI(f, owner)), resolveCall(refs, DutyDataDesignationRead)))
	if len(got) != len(refs) {
		t.Fatalf("items: %v", got)
	}
	for i, item := range got {
		if item.Class != domain.ClassUNKNOWN || item.Source != domain.SourceUnresolved {
			t.Errorf("ref %d: %+v", i, item)
		}
	}
	if f.designs.resolveCalls != 0 {
		t.Fatalf("nothing resolvable, yet the store was read %d times", f.designs.resolveCalls)
	}
}

func TestResolve_OwnerFailureIsUnknown(t *testing.T) {
	down, fine := ids.New(), ids.New()
	f := &fakeStore{designs: fakeDesignations{cuiRecords: map[string]bool{down: true, fine: true}}}
	owner := &fakeOwner{legal: map[string]string{down: testLE, fine: testLE}, failFor: map[string]bool{down: true}}
	got := resolveItems(t, serve(t, router(ownerAPI(f, owner)), resolveCall(refsOf("project", down, fine), DutyDataDesignationRead)))
	if got[0].Class != domain.ClassUNKNOWN || got[1].Class != domain.ClassCUI {
		t.Fatalf("items: %v", got)
	}
	none := resolveItems(t, serve(t, router(ownerAPI(&fakeStore{}, nil)), resolveCall(refsOf("project", fine), DutyDataDesignationRead)))
	if none[0].Class != domain.ClassUNKNOWN {
		t.Fatalf("no owner client must resolve UNKNOWN: %v", none)
	}
}

type blockingOwner struct{}

func (blockingOwner) LegalEntity(ctx context.Context, _, _ string) (string, error) {
	<-ctx.Done()
	return "", errOwnerUnavailable
}

func TestResolve_SlowOwnerHitsTheOverallDeadlineAndResolvesUnknown(t *testing.T) {
	previous := resolveDeadline
	resolveDeadline = 50 * time.Millisecond
	defer func() { resolveDeadline = previous }()
	recordIDs := make([]string, domain.MaxResolveRefs)
	for i := range recordIDs {
		recordIDs[i] = ids.New()
	}
	f := &fakeStore{designs: fakeDesignations{tenantCUI: true}}
	started := time.Now()
	got := resolveItems(t, serve(t, router(ownerAPI(f, blockingOwner{})), resolveCall(refsOf("project", recordIDs...), DutyDataDesignationRead)))
	if time.Since(started) > 2*time.Second {
		t.Fatalf("resolve ran past its deadline: %s", time.Since(started))
	}
	for _, item := range got {
		if item.Class != domain.ClassUNKNOWN {
			t.Fatalf("unresolved ref must be UNKNOWN: %v", item)
		}
	}
}

func TestResolve_StoreFailureIsNotAnAnswer(t *testing.T) {
	record := ids.New()
	f := &fakeStore{designs: fakeDesignations{resolveErr: errors.New("db down")}}
	rec := serve(t, router(ownerAPI(f, &fakeOwner{legal: map[string]string{record: testLE}})), resolveCall(refsOf("project", record), DutyDataDesignationRead))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("store failure: %d %s", rec.Code, rec.Body.String())
	}
}

func TestResolve_ClaimlessCallerSeesTenantCUIAndOtherwiseUnknown(t *testing.T) {
	record := ids.New()
	owner := &fakeOwner{legal: map[string]string{record: testLE}}
	claimless := resolveCall(refsOf("project", record), DutyDataDesignationRead)
	claimless.legalEntity = ""

	cui := &fakeStore{designs: fakeDesignations{tenantCUI: true}}
	got := resolveItems(t, serve(t, router(ownerAPI(cui, owner)), claimless))
	if got[0].Class != domain.ClassCUI || got[0].Source != domain.SourceTenant || owner.calls != 0 {
		t.Fatalf("tenant CUI must reach a claimless caller without an owner lookup: %v, %d owner calls", got, owner.calls)
	}

	plain := &fakeStore{}
	got = resolveItems(t, serve(t, router(ownerAPI(plain, owner)), claimless))
	if got[0].Class != domain.ClassUNKNOWN || got[0].Source != domain.SourceUnresolved {
		t.Fatalf("an undesignated tenant must stay UNKNOWN for a claimless caller: %v", got)
	}
}

func stateOf(f *fakeStore) *fakeDesignations { return &f.designs }

func (f *fakeStore) ChangeDesignation(_ context.Context, id string, expected int, in store.DesignationChange, at time.Time) (store.Designation, error) {
	s := stateOf(f)
	s.changeCalls++
	s.changedID, s.changedIn = id, in
	if s.changeErr != nil {
		return store.Designation{}, s.changeErr
	}
	return store.Designation{DesignationID: id, Scope: domain.ScopeLegalEntity, Class: in.Class, Reason: in.Reason, DesignatedAt: at, Version: expected + 1}, nil
}

func (f *fakeStore) GetDesignation(_ context.Context, id string) (store.Designation, error) {
	return store.Designation{DesignationID: id, Scope: domain.ScopeLegalEntity, Class: domain.ClassCUI, Version: 3}, nil
}

func (f *fakeStore) ListDesignations(_ context.Context, filter store.DesignationFilter) ([]store.Designation, error) {
	stateOf(f).listed = filter
	return []store.Designation{{DesignationID: ids.New(), Scope: domain.ScopeRecord, Class: domain.ClassCUI, Version: 1}}, nil
}

func (f *fakeStore) DesignationHistory(_ context.Context, _ string) ([]store.DesignationHistoryEntry, error) {
	return stateOf(f).history, nil
}

func TestDesignation_TenantScopeNeedsTenantAdministerAndNoLegalEntityClaim(t *testing.T) {
	for name, c := range map[string]call{
		"write duty only":                  designationCall(designationBody("TENANT", ""), "", DutyDataDesignationWrite),
		"legal-entity scoped tenant admin": designationCall(designationBody("TENANT", ""), testLE, DutyDataDesignationWrite, tenant.DutyTenantAdmin),
		"legal-entity scoped write only":   designationCall(designationBody("TENANT", ""), testLE, DutyDataDesignationWrite),
		"tenant admin without write duty":  designationCall(designationBody("TENANT", ""), "", tenant.DutyTenantAdmin),
	} {
		f := &fakeStore{}
		rec := serve(t, router(boundaryAPI(f, domain.BoundaryCUI)), c)
		if rec.Code != http.StatusForbidden || problemCode(t, rec) != "AUTH-0003" || f.designs.calls != 0 {
			t.Errorf("%s: %d %s, store calls %d", name, rec.Code, rec.Body.String(), f.designs.calls)
		}
	}
}

func changeCall(id, version string, body any, le string, duties ...string) call {
	headers := map[string]string{}
	if version != "" {
		headers["If-Match"] = version
	}
	return call{method: http.MethodPatch, path: "/v1/compliance/data-designations/" + id, body: body, duties: duties, legalEntity: le, headers: headers}
}

func TestChangeDesignation_DowngradeNeedsReasonAndIfMatch(t *testing.T) {
	id := ids.New()
	f := &fakeStore{}
	h := router(boundaryAPI(f, domain.BoundaryCUI))
	rec := serve(t, h, changeCall(id, `"2"`, map[string]any{"class": "NONE", "reason": "released to the public"}, testLE, DutyDataDesignationWrite))
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` || stateOf(f).changedID != id || stateOf(f).changedIn.Class != domain.ClassNONE {
		t.Fatalf("downgrade: %d %s", rec.Code, rec.Body.String())
	}
	for name, tc := range map[string]struct {
		version string
		body    map[string]any
		status  int
	}{
		"reason absent":     {`"2"`, map[string]any{"class": "NONE"}, http.StatusBadRequest},
		"reason whitespace": {`"2"`, map[string]any{"class": "NONE", "reason": "  "}, http.StatusBadRequest},
		"reason 501":        {`"2"`, map[string]any{"class": "NONE", "reason": strings.Repeat("r", 501)}, http.StatusBadRequest},
		"class P3":          {`"2"`, map[string]any{"class": "P3", "reason": "r"}, http.StatusBadRequest},
		"class absent":      {`"2"`, map[string]any{"reason": "r"}, http.StatusBadRequest},
		"no If-Match":       {"", map[string]any{"class": "NONE", "reason": "r"}, http.StatusPreconditionRequired},
		"bad If-Match":      {"abc", map[string]any{"class": "NONE", "reason": "r"}, http.StatusBadRequest},
	} {
		g := &fakeStore{}
		rec := serve(t, router(boundaryAPI(g, domain.BoundaryCUI)), changeCall(id, tc.version, tc.body, testLE, DutyDataDesignationWrite))
		if rec.Code != tc.status || stateOf(g).changeCalls != 0 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestChangeDesignation_NeedsTheWriteDutyAndStandardBoundaryRefusesCUI(t *testing.T) {
	id := ids.New()
	g := &fakeStore{}
	rec := serve(t, router(boundaryAPI(g, domain.BoundaryCUI)), changeCall(id, `"1"`, map[string]any{"class": "NONE", "reason": "r"}, testLE, DutyDataDesignationRead))
	if rec.Code != http.StatusForbidden || stateOf(g).changeCalls != 0 {
		t.Fatalf("read duty must not change: %d", rec.Code)
	}
	f := &fakeStore{}
	rec = serve(t, router(boundaryAPI(f, domain.BoundaryStandard)), changeCall(id, `"1"`, map[string]any{"class": "CUI", "reason": "r"}, testLE, DutyDataDesignationWrite))
	if rec.Code != http.StatusConflict || problemCode(t, rec) != domain.CodeCUIBoundary || stateOf(f).changeCalls != 0 {
		t.Fatalf("standard boundary: %d %s", rec.Code, rec.Body.String())
	}
	h := &fakeStore{}
	rec = serve(t, router(boundaryAPI(h, domain.BoundaryStandard)), changeCall(id, `"1"`, map[string]any{"class": "NONE", "reason": "r"}, testLE, DutyDataDesignationWrite))
	if rec.Code != http.StatusOK {
		t.Fatalf("a downgrade is allowed outside the CUI boundary: %d", rec.Code)
	}
}

func TestChangeDesignation_StoreProblemsPassThrough(t *testing.T) {
	for status, err := range map[int]error{http.StatusPreconditionFailed: httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale"), http.StatusNotFound: httpx.NewProblem(http.StatusNotFound, store.CodeDesignationNotFound, "none"), http.StatusForbidden: httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "tenant")} {
		f := &fakeStore{}
		stateOf(f).changeErr = err
		rec := serve(t, router(boundaryAPI(f, domain.BoundaryCUI)), changeCall(ids.New(), `"1"`, map[string]any{"class": "NONE", "reason": "r"}, testLE, DutyDataDesignationWrite))
		if rec.Code != status {
			t.Errorf("%d: got %d %s", status, rec.Code, rec.Body.String())
		}
	}
}

func TestDesignationReads_NeedTheReadDutyAndValidateFilters(t *testing.T) {
	id := ids.New()
	f := &fakeStore{}
	h := router(boundaryAPI(f, domain.BoundaryCUI))
	for _, path := range []string{"/v1/compliance/data-designations", "/v1/compliance/data-designations/" + id, "/v1/compliance/data-designations/" + id + "/history"} {
		if rec := serve(t, h, call{method: http.MethodGet, path: path, duties: []string{DutyDataDesignationWrite}, legalEntity: testLE}); rec.Code != http.StatusForbidden {
			t.Errorf("%s with the write duty only: %d", path, rec.Code)
		}
		if rec := serve(t, h, call{method: http.MethodGet, path: path, duties: []string{DutyDataDesignationRead}, legalEntity: testLE}); rec.Code != http.StatusOK {
			t.Errorf("%s with the read duty: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	record := ids.New()
	rec := serve(t, h, call{method: http.MethodGet, path: "/v1/compliance/data-designations?entityType=Project&recordId=" + strings.ToUpper(record), duties: []string{DutyDataDesignationRead}, legalEntity: testLE})
	if got := stateOf(f).listed; rec.Code != http.StatusOK || got.EntityType != "project" || got.RecordID != record {
		t.Fatalf("filter: %d %+v", rec.Code, got)
	}
	for _, query := range []string{"?scope=ALL", "?entityType=widget", "?recordId=nope"} {
		if rec := serve(t, h, call{method: http.MethodGet, path: "/v1/compliance/data-designations" + query, duties: []string{DutyDataDesignationRead}, legalEntity: testLE}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", query, rec.Code)
		}
	}
}

func TestDesignationHistory_ListsEveryChangeWithActorReasonAndBeforeAfter(t *testing.T) {
	f := &fakeStore{}
	stateOf(f).history = []store.DesignationHistoryEntry{{AuditID: ids.New(), Action: "DOWNGRADE", Scope: domain.ScopeRecord, Actor: "u-1", Reason: "public release", Before: json.RawMessage(`{"class":"CUI"}`), After: json.RawMessage(`{"class":"NONE"}`)}}
	rec := serve(t, router(boundaryAPI(f, domain.BoundaryCUI)), call{method: http.MethodGet, path: "/v1/compliance/data-designations/" + ids.New() + "/history", duties: []string{DutyDataDesignationRead}, legalEntity: testLE})
	var body struct{ Items []map[string]any }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || len(body.Items) != 1 {
		t.Fatalf("history: %d %s", rec.Code, rec.Body.String())
	}
	for _, key := range []string{"actor", "occurredAt", "scope", "reason", "before", "after"} {
		if _, ok := body.Items[0][key]; !ok {
			t.Errorf("history entry lacks %s: %v", key, body.Items[0])
		}
	}
}
