package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

const piStepID0 = "0195f0a0-0000-7000-8000-0000000000d1"

func (f *fakeStore) CreatePIStep(_ context.Context, in store.PIStepInput, _ string) (store.PIStepTemplate, error) {
	f.piStepIn = &in
	return store.PIStepTemplate{ID: piStepID0, Code: in.Code, Version: 1, Name: in.Name, Kind: in.Spec.Category, Status: "draft", Rev: 1}, nil
}
func (f *fakeStore) ListPISteps(_ context.Context, fl store.PIStepFilter) ([]store.PIStepTemplate, error) {
	f.piStepFilter = &fl
	return []store.PIStepTemplate{}, nil
}
func (f *fakeStore) GetPIStep(_ context.Context, id string) (store.PIStepTemplate, error) {
	return store.PIStepTemplate{ID: id, Code: "CURE", Version: 2, Status: "published", Rev: 4}, nil
}
func (f *fakeStore) UpdatePIStep(_ context.Context, id string, rev int, in store.PIStepInput, _ string) (store.PIStepTemplate, error) {
	f.piStepIn, f.updateVersion = &in, rev
	return store.PIStepTemplate{ID: id, Rev: rev + 1, Status: "draft"}, nil
}
func (f *fakeStore) PublishPIStep(_ context.Context, id string, rev int, _ string) (store.PIStepTemplate, error) {
	f.updateVersion = rev
	return store.PIStepTemplate{ID: id, Rev: rev + 1, Status: "published"}, nil
}
func (f *fakeStore) NewPIStepVersion(_ context.Context, id string, _ string) (store.PIStepTemplate, error) {
	return store.PIStepTemplate{ID: id, Version: 3, Rev: 1, Status: "draft"}, nil
}
func (f *fakeStore) ResolvePISteps(_ context.Context, refs []domain.TemplateRef) (map[domain.TemplateRef]store.PIStepTemplate, error) {
	out := map[domain.TemplateRef]store.PIStepTemplate{}
	for _, r := range refs {
		if t, ok := f.piSteps[r]; ok {
			out[r] = t
		}
	}
	return out, nil
}

const timerSpec = `{"category":"timer","text":"Let the resin cure.","durationSeconds":600,"blocking":true}`

func TestPIStepRoutes_DutiesETagAndValidation(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	path := "/v1/shopfloor/pi-step-templates"
	body := `{"code":"CURE-HOLD","name":"Cure hold","spec":` + timerSpec + `}`
	if rec := call(t, h, http.MethodPost, path, body, DutyView); rec.Code != http.StatusForbidden {
		t.Fatalf("create without the maintain duty: %d", rec.Code)
	}
	rec := call(t, h, http.MethodPost, path, body, DutyPIStepMaintain)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` || f.piStepIn.Spec.DurationSeconds != 600 || !f.piStepIn.Spec.Blocking {
		t.Fatalf("create: %d %s %+v", rec.Code, rec.Body, f.piStepIn)
	}
	for name, tc := range map[string]struct{ body, field string }{
		"lower-case code":               {`{"code":"cure","name":"x","spec":` + timerSpec + `}`, "code"},
		"no name":                       {`{"code":"CURE","name":" ","spec":` + timerSpec + `}`, "name"},
		"timer without a duration":      {`{"code":"CURE","name":"x","spec":{"category":"timer","text":"w"}}`, "spec.durationSeconds"},
		"calculation without a formula": {`{"code":"NET","name":"x","spec":{"category":"calculation","text":"Net weight."}}`, "spec.expression"},
		"formula with eval":             {`{"code":"NET","name":"x","spec":{"category":"calculation","text":"n","expression":"eval(a)"}}`, "spec.expression"},
		"branch with a target":          {`{"code":"BR","name":"x","spec":{"category":"branch","text":"q","condition":"a > 1","target":3}}`, "spec.target"},
		"readout without a node":        {`{"code":"RD","name":"x","spec":{"category":"machine-readout","characteristic":{"name":"T","type":"numeric"}}}`, "spec.node"},
		"bad category id":               {`{"code":"CURE","name":"x","categoryId":"nope","spec":` + timerSpec + `}`, "categoryId"},
		"unknown field":                 {`{"code":"CURE","name":"x","spec":` + timerSpec + `,"extra":1}`, ""},
		"unknown spec field":            {`{"code":"CURE","name":"x","spec":{"category":"timer","text":"w","durationSeconds":5,"script":"x"}}`, ""},
	} {
		rec := call(t, h, http.MethodPost, path, tc.body, DutyPIStepMaintain)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), tc.field) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	one := path + "/" + piStepID0
	if rec := callWith(t, h, http.MethodPut, one, `{"name":"x","spec":`+timerSpec+`}`, DutyPIStepMaintain, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("update without If-Match: %d", rec.Code)
	}
	rec = callWith(t, h, http.MethodPut, one, `{"name":"x","spec":`+timerSpec+`}`, DutyPIStepMaintain, map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusOK || f.updateVersion != 3 || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if rec := callWith(t, h, http.MethodPost, one+":publish", ``, DutyPIStepMaintain, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("publish without If-Match: %d", rec.Code)
	}
	if rec := callWith(t, h, http.MethodPost, one+":publish", ``, DutyView, map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("publish with the view duty: %d", rec.Code)
	}
	rec = callWith(t, h, http.MethodPost, one+":publish", ``, DutyPIStepMaintain, map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"published"`) {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, http.MethodPost, one+":new-version", ``, DutyPIStepMaintain); rec.Code != http.StatusCreated {
		t.Fatalf("new version: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, http.MethodGet, path+"/nope", "", DutyView); rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodePIStepNotFound {
		t.Fatalf("get of a bad id: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, one, "", DutyView); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("get: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, path+"?status=published&code=CURE&version=2&kind=timer", "", DutyView); rec.Code != http.StatusOK || f.piStepFilter.Version != 2 || f.piStepFilter.Kind != "timer" {
		t.Fatalf("list: %d %+v", rec.Code, f.piStepFilter)
	}
	for _, q := range []string{"?status=nope", "?version=0", "?kind=script", "?categoryId=x"} {
		if rec := call(t, h, http.MethodGet, path+q, "", DutyView); rec.Code != http.StatusBadRequest {
			t.Errorf("list %s: %d", q, rec.Code)
		}
	}
}

// A sheet that names a step template must name one that exists, is not a draft, and is of the step's kind.
func TestPISheetTemplateReferences(t *testing.T) {
	f := &fakeStore{piSteps: map[domain.TemplateRef]store.PIStepTemplate{
		{Code: "CURE-HOLD", Version: 1}: {Code: "CURE-HOLD", Version: 1, Kind: domain.InstTimer, Status: "published"},
		{Code: "CURE-HOLD", Version: 2}: {Code: "CURE-HOLD", Version: 2, Kind: domain.InstTimer, Status: "draft"},
		{Code: "WEIGH", Version: 1}:     {Code: "WEIGH", Version: 1, Kind: domain.InstDataRequest, Status: "superseded"},
	}}
	h := newRouter(f)
	sheet := func(ref string, category string) string {
		return fmt.Sprintf(`{"code":"CHARGE","name":"Charge","recipe":{"procedure":{"name":"B","unitProcedures":[{"name":"U","operations":[{"name":"O","phases":[{"name":"P","instructions":[
			{"seq":1,"category":%q,"text":"Cure.","durationSeconds":60,"template":%s}]}]}]}]}}}`, category, ref)
	}
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/pi-sheets", sheet(`{"code":"CURE-HOLD","version":1}`, "timer"), DutyPISheetMaintain); rec.Code != http.StatusCreated {
		t.Fatalf("a published template: %d %s", rec.Code, rec.Body)
	}
	for name, tc := range map[string]struct{ body, want string }{
		"unknown template": {sheet(`{"code":"NOPE","version":1}`, "timer"), "does not exist"},
		"unknown version":  {sheet(`{"code":"CURE-HOLD","version":9}`, "timer"), "does not exist"},
		"a draft":          {sheet(`{"code":"CURE-HOLD","version":2}`, "timer"), "is a draft"},
		"another kind":     {sheet(`{"code":"CURE-HOLD","version":1}`, "branch"), "not a branch step"},
		"malformed":        {sheet(`{"code":"bad code","version":1}`, "timer"), "by its code"},
	} {
		rec := call(t, h, http.MethodPost, "/v1/shopfloor/pi-sheets", tc.body, DutyPISheetMaintain)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

// The library on a real database: a draft is edited, published, then immutable; a new version supersedes it when it is
// published; sheets refer to a template by code and version; the database itself refuses to change a published one.
func TestPIStepLibraryLifecycleOnADatabase(t *testing.T) {
	e := newRecipeEnv(t)
	const duties = DutyPIStepMaintain + "," + DutyView + "," + DutyPISheetMaintain + "," + DutyPICategoryMaintain
	send := func(method, path, body, ifMatch string) (*httptest.ResponseRecorder, store.PIStepTemplate) {
		e.calls++
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-User", "engineer-1")
		req.Header.Set("X-Passage-Duties", duties)
		req.Header.Set("Idempotency-Key", fmt.Sprintf("step-%d", e.calls))
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		var tpl store.PIStepTemplate
		_ = json.Unmarshal(rec.Body.Bytes(), &tpl)
		return rec, tpl
	}
	// A category to file the template under, and an inactive one.
	var cat, off store.PICategory
	rec := e.send(http.MethodPost, "/v1/shopfloor/pi-categories", `{"code":"CURING","name":"Curing"}`, duties, "engineer-1")
	_ = json.Unmarshal(rec.Body.Bytes(), &cat)
	rec = e.send(http.MethodPost, "/v1/shopfloor/pi-categories", `{"code":"OLD-STEPS","name":"Old","active":false}`, duties, "engineer-1")
	_ = json.Unmarshal(rec.Body.Bytes(), &off)
	if cat.ID == "" || off.ID == "" {
		t.Fatal("categories were not created")
	}
	body := func(name string, seconds int, category string) string {
		return fmt.Sprintf(`{"code":"CURE-HOLD","name":%q,"categoryId":%q,"spec":{"category":"timer","text":"Let the resin cure.","durationSeconds":%d,"blocking":true}}`, name, category, seconds)
	}
	if rec, _ := send(http.MethodPost, "/v1/shopfloor/pi-step-templates", body("Cure hold", 600, off.ID), ""); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodePICategoryInactive) {
		t.Fatalf("a template in an inactive category: %d %s", rec.Code, rec.Body)
	}
	rec, v1 := send(http.MethodPost, "/v1/shopfloor/pi-step-templates", body("Cure hold", 600, cat.ID), "")
	if rec.Code != http.StatusCreated || v1.Version != 1 || v1.Status != "draft" || v1.Kind != "timer" || v1.CategoryID == nil || *v1.CategoryID != cat.ID {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := send(http.MethodPost, "/v1/shopfloor/pi-step-templates", body("Cure hold", 600, cat.ID), ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodePIStepCode) {
		t.Fatalf("a second template with the code: %d %s", rec.Code, rec.Body)
	}
	one := "/v1/shopfloor/pi-step-templates/" + v1.ID
	rec, v1 = send(http.MethodPut, one, body("Cure hold", 900, cat.ID), `"1"`)
	if rec.Code != http.StatusOK || v1.Rev != 2 || !strings.Contains(string(v1.Spec), "900") {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := send(http.MethodPut, one, body("Cure hold", 900, cat.ID), `"1"`); rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), store.CodeVersionMismatch) {
		t.Fatalf("a stale edit: %d %s", rec.Code, rec.Body)
	}
	// A draft cannot be referred to by a sheet.
	sheet := func(code string, version int) string {
		return fmt.Sprintf(`{"code":%q,"name":"Cure sheet","recipe":{"procedure":{"name":"B","unitProcedures":[{"name":"U","operations":[{"name":"O","phases":[{"name":"P","instructions":[
			{"seq":1,"category":"timer","text":"Let the resin cure.","durationSeconds":900,"blocking":true,"template":{"code":"CURE-HOLD","version":%d}}]}]}]}]}}}`, code, version)
	}
	if rec, _ := send(http.MethodPost, "/v1/shopfloor/pi-sheets", sheet("SH-DRAFT", 1), ""); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "is a draft") {
		t.Fatalf("a sheet from a draft template: %d %s", rec.Code, rec.Body)
	}
	rec, v1 = send(http.MethodPost, one+":publish", ``, `"2"`)
	if rec.Code != http.StatusOK || v1.Status != "published" || v1.PublishedBy == nil || *v1.PublishedBy != "engineer-1" {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := send(http.MethodPut, one, body("Changed", 5, cat.ID), fmt.Sprintf(`"%d"`, v1.Rev)); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodePIStepState) {
		t.Fatalf("edit of a published version: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := send(http.MethodPost, one+":publish", ``, fmt.Sprintf(`"%d"`, v1.Rev)); rec.Code != http.StatusConflict {
		t.Fatalf("publish of a published version: %d", rec.Code)
	}
	// The database refuses what the service would not do: changing a published version.
	ctx := context.Background()
	for name, stmt := range map[string]string{
		"the spec":   `update pi_step_template set spec = '{"category":"timer","text":"x","durationSeconds":1}' where id = '` + v1.ID + `'`,
		"the name":   `update pi_step_template set name = 'tampered' where id = '` + v1.ID + `'`,
		"the status": `update pi_step_template set status = 'draft', published_at = null where id = '` + v1.ID + `'`,
	} {
		if _, err := e.admin.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "cannot be changed") {
			t.Errorf("a direct change of %s was not refused: %v", name, err)
		}
	}
	// A sheet refers to the published version by code and version; the reference is kept in its document.
	rec, _ = send(http.MethodPost, "/v1/shopfloor/pi-sheets", sheet("SH-CURE", 1), "")
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"template":{"code":"CURE-HOLD","version":1}`) {
		t.Fatalf("a sheet from a published template: %d %s", rec.Code, rec.Body)
	}
	// A new version is a draft copy; one draft per code; publishing it supersedes version 1, which stays referable.
	rec, v2 := send(http.MethodPost, one+":new-version", "", "")
	if rec.Code != http.StatusCreated || v2.Version != 2 || v2.Status != "draft" || !strings.Contains(string(v2.Spec), "900") {
		t.Fatalf("new version: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := send(http.MethodPost, one+":new-version", "", ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodePIStepCode) {
		t.Fatalf("a second draft: %d %s", rec.Code, rec.Body)
	}
	two := "/v1/shopfloor/pi-step-templates/" + v2.ID
	rec, v2 = send(http.MethodPut, two, body("Cure hold v2", 1200, cat.ID), `"1"`)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit of version 2: %d %s", rec.Code, rec.Body)
	}
	if rec, got := send(http.MethodPost, two+":publish", ``, fmt.Sprintf(`"%d"`, v2.Rev)); rec.Code != http.StatusOK || got.Status != "published" {
		t.Fatalf("publish of version 2: %d %s", rec.Code, rec.Body)
	}
	if _, old := send(http.MethodGet, one, "", ""); old.Status != "superseded" || !strings.Contains(string(old.Spec), "900") {
		t.Fatalf("version 1 after version 2 was published: %s %s", old.Status, old.Spec)
	}
	if rec, _ := send(http.MethodPost, "/v1/shopfloor/pi-sheets", sheet("SH-OLD", 1), ""); rec.Code != http.StatusCreated {
		t.Fatalf("a sheet that still names the superseded version: %d %s", rec.Code, rec.Body)
	}
	var list struct{ Items []store.PIStepTemplate }
	lr, _ := send(http.MethodGet, "/v1/shopfloor/pi-step-templates?code=CURE-HOLD&categoryId="+cat.ID+"&kind=timer", "", "")
	_ = json.Unmarshal(lr.Body.Bytes(), &list)
	if len(list.Items) != 2 || list.Items[0].Version != 2 || list.Items[1].Status != "superseded" {
		t.Fatalf("list: %s", lr.Body)
	}
}
