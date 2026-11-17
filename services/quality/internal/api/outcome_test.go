package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/quality/internal/domain"
)

func TestOutcomes_CreateListRenameAndRetire(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/outcomes", body: `{"code":" no-defect ","name":"No visible defect"}`, duties: allDuties, key: "o1"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"code":"NO-DEFECT"`) || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created domain.Outcome
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if dup := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/outcomes", body: `{"code":"NO-DEFECT","name":"Again"}`, duties: allDuties, key: "o2"}); dup.Code != http.StatusConflict {
		t.Fatalf("duplicate code: %d", dup.Code)
	}
	if bad := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/outcomes", body: `{"code":"x","name":""}`, duties: allDuties, key: "o3"}); bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid: %d", bad.Code)
	}
	if rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/outcomes?q=+defect+&code=no-defect&active=true&limit=5", duties: "quality.view"}); rec.Code != http.StatusOK ||
		f.outFilter.Q != "defect" || f.outFilter.Code != "NO-DEFECT" || !f.outFilter.ActiveOnly || f.outFilter.Limit != 5 {
		t.Fatalf("list: %d filter %+v", rec.Code, f.outFilter)
	}
	path := "/v1/quality/outcomes/" + created.ID
	if rec := call(h, reqSpec{method: http.MethodPatch, path: path, body: `{"active":false}`, duties: allDuties}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := call(h, reqSpec{method: http.MethodPatch, path: path, body: `{"active":false}`, duties: allDuties, ifMatch: `"7"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d", rec.Code)
	}
	if rec := call(h, reqSpec{method: http.MethodPatch, path: path, body: `{}`, duties: allDuties, ifMatch: `"1"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty patch: %d", rec.Code)
	}
	rec = call(h, reqSpec{method: http.MethodPatch, path: path, body: `{"name":"Clean","active":false}`, duties: allDuties, ifMatch: `"1"`})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"active":false`) || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/outcomes/" + ids.New(), duties: "quality.view"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", rec.Code)
	}
	if rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/outcomes", body: `{"code":"AB","name":"x"}`, duties: "quality.view", key: "o9"}); rec.Code != http.StatusForbidden {
		t.Fatalf("setup duty required: %d", rec.Code)
	}
}

func TestResults_NameTheOutcomeByCode(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	groupID, testID, outcomeID := createGroup(t, h, true)
	f.outcomes = map[string]domain.Outcome{outcomeID: {ID: outcomeID, Code: "OK", Name: "Fine", Active: true, Version: 1}}
	orderID := ids.New()
	f.orders[orderID] = domain.Order{ID: orderID, LotQty: decimal.NewFromInt(5), SampleQty: decimal.NewFromInt(1), TestGroupID: groupID, Status: domain.StatusCreated, Version: 1}
	path := "/v1/quality/quality-orders/" + orderID + "/results"
	unknown := `{"results":[{"testId":"` + testID + `","sampleNo":1,"outcomeCode":"NOPE"}]}`
	if rec := call(h, reqSpec{method: http.MethodPost, path: path, body: unknown, duties: allDuties, key: "c1"}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeOutcomeUnknown) {
		t.Fatalf("unknown code: %d %s", rec.Code, rec.Body.String())
	}
	known := `{"results":[{"testId":"` + testID + `","sampleNo":1,"outcomeCode":"ok"}]}`
	if rec := call(h, reqSpec{method: http.MethodPost, path: path, body: known, duties: allDuties, key: "c2"}); rec.Code != http.StatusCreated {
		t.Fatalf("known code: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.results[orderID]; len(got) != 1 || got[0].OutcomeID != outcomeID || !got[0].Passed {
		t.Fatalf("recorded %+v, want the master's id and a pass", got)
	}
}

func TestTestGroups_PassingOutcomesAreRegisteredIdsOrCodes(t *testing.T) {
	f := newFake()
	f.strictOutcomes = true
	known := ids.New()
	f.outcomes = map[string]domain.Outcome{known: {ID: known, Code: "PASS", Name: "Pass", Active: true, Version: 1}}
	h := newHandler(f)
	body := func(ref string) string {
		return `{"code":"GRP-1","tests":[{"testId":"` + ids.New() + `","name":"look","kind":"ATTRIBUTE","required":true,"passingOutcomeIds":["` + ref + `"]}]}`
	}
	if rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/test-groups", body: body("pass"), duties: allDuties, key: "g1"}); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), known) {
		t.Fatalf("a code resolves to its id: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/test-groups", body: body(known), duties: allDuties, key: "g2"}); rec.Code != http.StatusCreated {
		t.Fatalf("a registered id: %d %s", rec.Code, rec.Body)
	}
	for _, ref := range []string{"NOPE", ids.New()} {
		if rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/test-groups", body: body(ref), duties: allDuties, key: "g3"}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "passingOutcomeIds") {
			t.Fatalf("%s: %d %s", ref, rec.Code, rec.Body)
		}
	}
}
