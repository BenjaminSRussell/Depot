package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/depot/services/shopfloor/internal/store"
)

func (f *fakeStore) ListPIExecutions(context.Context, store.PIExecutionFilter) ([]store.PIExecution, error) {
	return []store.PIExecution{}, nil
}
func (f *fakeStore) GetPIExecution(_ context.Context, id string) (store.PIExecutionDetail, error) {
	return store.PIExecutionDetail{PIExecution: store.PIExecution{ID: id}}, nil
}
func (f *fakeStore) VerifyPIExecution(context.Context, string) (domain.VerifyResult, error) {
	return domain.VerifyResult{Valid: true}, nil
}

func TestPIExecutionRoutes_Duties(t *testing.T) {
	h := newRouter(&fakeStore{})
	for _, p := range []string{"/v1/shopfloor/pi-executions", "/v1/shopfloor/pi-executions/" + edgeID} {
		if rec := call(t, h, http.MethodGet, p, "", DutyView); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
	v := "/v1/shopfloor/pi-executions/" + edgeID + ":verify"
	if rec := call(t, h, http.MethodPost, v, "", DutyView); rec.Code != http.StatusForbidden {
		t.Fatalf("verify with the view duty: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, v, "", DutyPIExecutionVerify); rec.Code != http.StatusOK {
		t.Fatalf("verify: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/pi-executions?orderId=x", "", DutyView); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad filter: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/pi-executions/nope", "", DutyView); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if rec := call(t, h, m, "/v1/shopfloor/pi-executions", "{}", DutyPIExecutionVerify+","+DutyView); rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
			t.Errorf("%s on the collection: %d (a record is never written by a client)", m, rec.Code)
		}
	}
}

// A kiosk sheet end to end: the run is sealed when the recipe is completed, with what the operator entered, the deviation,
// who completed it and which sheet it came from; it verifies; the database refuses any change; and a change made behind its
// back is found by the verify route.
func TestPIExecutionRecordOnADatabase(t *testing.T) {
	e := newRecipeEnv(t)
	const duties = DutyPISheetMaintain + "," + DutyView + "," + DutyControlRecipeCreate + "," + DutyControlRecipeExecute + "," + DutyPIExecutionVerify
	send := func(method, path, body, user string) *httptest.ResponseRecorder {
		return e.send(method, path, body, duties+","+processDuty, user)
	}
	var sh store.PISheet
	rec := send(http.MethodPost, "/v1/shopfloor/pi-sheets", sheetBody("EXEC-1", 72), "designer-1")
	_ = json.Unmarshal(rec.Body.Bytes(), &sh)
	if rec.Code != http.StatusCreated {
		t.Fatalf("sheet: %d %s", rec.Code, rec.Body)
	}
	if rec := send(http.MethodPost, "/v1/shopfloor/pi-sheets/"+sh.ID+":release", `{"rev":1}`, "designer-1"); rec.Code != http.StatusOK {
		t.Fatalf("release: %d %s", rec.Code, rec.Body)
	}
	var rc store.ControlRecipe
	rec = send(http.MethodPost, "/v1/shopfloor/control-recipes:from-pi-sheet", fmt.Sprintf(`{"piSheetId":%q,"orderId":%q,"resourceId":%q}`, sh.ID, recipeOrder, recipeResource), "planner-1")
	_ = json.Unmarshal(rec.Body.Bytes(), &rc)
	if rec.Code != http.StatusCreated {
		t.Fatalf("recipe: %d %s", rec.Code, rec.Body)
	}
	report := func(status string) int {
		return send(http.MethodPost, "/v1/shopfloor/control-recipes/"+rc.ID+":report", `{"status":"`+status+`"}`, "operator-1").Code
	}
	if report("received") != 200 || report("in-execution") != 200 {
		t.Fatal("recipe did not start")
	}
	if rec := send(http.MethodGet, "/v1/shopfloor/pi-executions?recipeId="+rc.ID, "", "operator-1"); !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("a record before the sheet is finished: %s", rec.Body)
	}
	at := time.Now().UTC().Format(time.RFC3339)
	message := func(step int, values string) {
		body := fmt.Sprintf(`{"source":"kiosk","sourceMessageId":"%s:Reactor/Heat/Ramp/%d","categoryCode":"process-data","orderId":%q,"phasePath":"Batch/Reactor/Heat/Ramp","occurredAt":%q,"values":[%s]}`, rc.ID, step, recipeOrder, at, values)
		if rec := send(http.MethodPost, "/v1/shopfloor/process-messages", body, "operator-1"); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Fatalf("message %d: %d %s", step, rec.Code, rec.Body)
		}
	}
	message(2, `{"name":"Temperature","type":"numeric","value":"35","min":"10","max":"30","unit":"degC"},{"name":"comment","type":"text","value":"probe was cold"}`)
	message(3, `{"name":"Jacket","type":"numeric","value":"72","min":"60","max":"90","unit":"degC"}`)
	if report("completed") != 200 {
		t.Fatal("recipe did not complete")
	}
	if report("completed") != 200 {
		t.Fatal("a repeated completion is not accepted")
	}
	var list struct{ Items []store.PIExecution }
	rec = send(http.MethodGet, "/v1/shopfloor/pi-executions?orderId="+recipeOrder+"&recipeId="+rc.ID, "", "operator-1")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Items) != 1 {
		t.Fatalf("records for the recipe: %s", rec.Body)
	}
	x := list.Items[0]
	if x.ExecutedBy != "operator-1" || x.StepCount != 3 || x.DeviationCount != 1 || x.SheetCode == nil || *x.SheetCode != "EXEC-1" || x.SheetVersion == nil || *x.SheetVersion != 1 || len(x.RecordHash) != 64 {
		t.Fatalf("header: %+v", x)
	}
	var d store.PIExecutionDetail
	rec = send(http.MethodGet, "/v1/shopfloor/pi-executions/"+x.ID, "", "operator-1")
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if len(d.Steps) != 3 || d.Steps[0].Status != "acknowledged" || d.Steps[1].Status != "recorded" || !d.Steps[1].Deviation || d.Steps[1].Comment != "probe was cold" || d.Steps[2].Deviation {
		t.Fatalf("steps: %s", rec.Body)
	}
	if d.Steps[0].PrevHash != domain.ChainSeed(x.ID, rc.ID) || d.Steps[1].PrevHash != d.Steps[0].Hash || d.Steps[2].Hash != x.HeadHash {
		t.Fatalf("the chain does not link: %+v", d.Steps)
	}
	verify := func() domain.VerifyResult {
		var r domain.VerifyResult
		rec := send(http.MethodPost, "/v1/shopfloor/pi-executions/"+x.ID+":verify", "", "auditor-1")
		_ = json.Unmarshal(rec.Body.Bytes(), &r)
		return r
	}
	if r := verify(); !r.Valid || r.CheckedSteps != 3 || r.RecordHash != x.RecordHash {
		t.Fatalf("verify: %+v", r)
	}
	// Nothing edits a record: the database refuses it for the admin, and the application role has no right to try.
	ctx := context.Background()
	for _, stmt := range []string{
		`update pi_execution_step set deviation = false where execution_id = '` + x.ID + `'`,
		`delete from pi_execution_step where execution_id = '` + x.ID + `'`,
		`update pi_execution set executed_by = 'x' where id = '` + x.ID + `'`,
		`delete from pi_execution where id = '` + x.ID + `'`,
		`truncate pi_execution_step`,
	} {
		if _, err := e.admin.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "append only") {
			t.Errorf("%s: not refused as append only: %v", stmt, err)
		}
	}
	// Someone with the table owner's rights switches the guard off and rewrites a value: verification finds the step.
	for _, stmt := range []string{
		`alter table pi_execution_step disable trigger pi_execution_step_append_only`,
		`update pi_execution_step set body = jsonb_set(body, '{values,0,value}', '"25"') where execution_id = '` + x.ID + `' and seq = 2`,
		`alter table pi_execution_step enable trigger pi_execution_step_append_only`,
	} {
		if _, err := e.admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if r := verify(); r.Valid || r.BrokenAtSeq == nil || *r.BrokenAtSeq != 2 || !strings.Contains(r.Reason, "content") {
		t.Fatalf("a rewritten value was not found: %+v", r)
	}
	if rec := send(http.MethodGet, "/v1/shopfloor/pi-executions/0195f0a0-0000-7000-8000-00000000ffff", "", "operator-1"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown record: %d", rec.Code)
	}
}
