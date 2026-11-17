package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/depot/services/shopfloor/internal/store"
)

const hardeningDuties = DutyControlRecipeCreate + "," + DutyControlRecipeExecute + "," + DutyView + "," + DutyPIExecutionVerify + "," + DutyEdgeConfigure + "," + DutyEdgeConnect + "," + DutySetup + "," + processDuty

// The server decides what a kiosk step records: forged branches, calculations, limits and sources are refused, a hand entry
// needs a reason, a machine value must be the reading the service handed out (from an edge node at the resource's site, by
// receipt time), a blocking timer needs its time on the service's clock, a signature step needs a signature, a retry returns
// the stored value, and the sealed record says what the service knows.
func TestKioskStepsAreDecidedByTheServer(t *testing.T) {
	e := newRecipeEnv(t)
	resource, order := ids.New(), ids.New()
	site1, site2 := ids.New(), ids.New()
	do := func(method, path, body, user string, hdr map[string]string) *httptest.ResponseRecorder {
		e.calls++
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Depot-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Depot-User", user)
		req.Header.Set("X-Depot-Duties", hardeningDuties)
		req.Header.Set("Idempotency-Key", fmt.Sprintf("h-%d", e.calls))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	send := func(method, path, body, user string) *httptest.ResponseRecorder {
		return do(method, path, body, user, nil)
	}
	must := func(rec *httptest.ResponseRecorder, want ...int) {
		t.Helper()
		for _, w := range want {
			if rec.Code == w {
				return
			}
		}
		t.Fatalf("status %d, want %v: %s", rec.Code, want, rec.Body)
	}
	// Edge nodes at two sites and a machine for the recipe's resource at site 1.
	var n1, n2 store.EdgeNode
	rec := send(http.MethodPost, "/v1/shopfloor/edge-nodes", fmt.Sprintf(`{"name":"Line 1 gateway","platform":"k3s","ring":"all","siteId":%q,"connectorSubject":"edge-h1"}`, site1), "sup")
	must(rec, 201)
	_ = json.Unmarshal(rec.Body.Bytes(), &n1)
	rec = send(http.MethodPost, "/v1/shopfloor/edge-nodes", fmt.Sprintf(`{"name":"Line 2 gateway","platform":"k3s","ring":"all","siteId":%q,"connectorSubject":"edge-h2"}`, site2), "sup")
	must(rec, 201)
	_ = json.Unmarshal(rec.Body.Bytes(), &n2)
	must(send(http.MethodPost, "/v1/shopfloor/machines", fmt.Sprintf(`{"machineId":"M-H1","resourceId":%q,"siteId":%q,"idealCycleSeconds":"60"}`, resource, site1), "sup"), 201)

	recipe := fmt.Sprintf(`{"orderId":%q,"resourceId":%q,"destination":"kiosk","recipe":{"procedure":{"name":"Batch","unitProcedures":[{"name":"U","operations":[{"name":"O","phases":[{"name":"P","instructions":[
		{"seq":1,"category":"process-data-request","text":"Temp.","var":"temp","characteristic":{"name":"Temp","type":"numeric","unit":"degC","min":10,"max":100}},
		{"seq":2,"category":"calculation","text":"Double.","var":"twice","expression":"temp * 2"},
		{"seq":3,"category":"branch","text":"Hot?","condition":"twice > 100","target":6},
		{"seq":4,"category":"machine-readout","text":"Oven.","node":"ns=2;s=OvenH","value":180,"tolerance":5,"characteristic":{"name":"Oven","type":"numeric","unit":"degC","min":150,"max":210}},
		{"seq":5,"category":"timer","text":"Cure.","durationSeconds":5,"blocking":true},
		{"seq":6,"category":"signature-request","text":"Sign.","meaning":"verified"},
		{"seq":7,"category":"instruction-text","text":"Done."}]}]}]}]}}}`, order, resource)
	var rc store.ControlRecipe
	rec = send(http.MethodPost, "/v1/shopfloor/control-recipes", recipe, "planner")
	must(rec, 201)
	_ = json.Unmarshal(rec.Body.Bytes(), &rc)
	report := func(status string) {
		must(send(http.MethodPost, "/v1/shopfloor/control-recipes/"+rc.ID+":report", `{"status":"`+status+`"}`, "operator-1"), 200)
	}
	report("received")
	report("in-execution")

	at := time.Now().UTC().Format(time.RFC3339)
	post := func(step int, values string, hdr map[string]string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"source":"kiosk","sourceMessageId":"%s:U/O/P/%d","categoryCode":"process-data","orderId":%q,"phasePath":"Batch/U/O/P","occurredAt":%q,"values":[%s]}`, rc.ID, step, order, at, values)
		return do(http.MethodPost, "/v1/shopfloor/process-messages", body, "operator-1", hdr)
	}
	refused := func(rec *httptest.ResponseRecorder, code, contains string) {
		t.Helper()
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), code) || !strings.Contains(rec.Body.String(), contains) {
			t.Fatalf("want 422 %s %q, got %d %s", code, contains, rec.Code, rec.Body)
		}
	}
	// Step 1: the limits are the recipe's, whatever the message says; a value the recipe does not ask for is refused.
	refused(post(1, `{"name":"Temp","type":"numeric","value":"40"},{"name":"expected","type":"numeric","value":"40"}`, nil), domain.CodeStepMessage, "not a value of this step")
	refused(post(1, `{"name":"Pressure","type":"numeric","value":"4"}`, nil), domain.CodeStepMessage, "records Temp")
	rec = post(1, `{"name":"Temp","type":"numeric","value":"40.0","min":"-9999","max":"9999"}`, nil)
	must(rec, 201)
	if !strings.Contains(rec.Body.String(), `"max":"100"`) || !strings.Contains(rec.Body.String(), `"value":"40"`) {
		t.Fatalf("the limits were not the recipe's: %s", rec.Body)
	}
	// A retry with another value returns what the service stored (the kiosk adopts it).
	rec = post(1, `{"name":"Temp","type":"numeric","value":"41"}`, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"value":"40"`) || !strings.Contains(rec.Body.String(), `"duplicate":true`) {
		t.Fatalf("a retry: %d %s", rec.Code, rec.Body)
	}
	// Step 2: the calculation is the service's.
	refused(post(2, `{"name":"twice","type":"numeric","value":"81"}`, nil), domain.CodeStepMessage, "not 81")
	must(post(2, `{"name":"twice","type":"numeric","value":"80"}`, nil), 201)
	// Step 3: the branch is the service's decision with the recipe's target; step 6 stays in play.
	refused(post(3, `{"name":"branch-taken","type":"boolean","value":"true"},{"name":"branch-target","type":"numeric","value":"6"}`, nil), domain.CodeStepMessage, "condition")
	refused(post(3, `{"name":"branch-taken","type":"boolean","value":"false"},{"name":"branch-target","type":"numeric","value":"999"}`, nil), domain.CodeStepMessage, "target")
	must(post(3, `{"name":"branch-taken","type":"boolean","value":"false"},{"name":"branch-target","type":"numeric","value":"6"}`, nil), 201)
	// Step 4: a machine value must be the reading handed out; a hand entry needs a reason.
	machine := func(v string) string {
		return fmt.Sprintf(`{"name":"Oven","type":"numeric","value":%q},{"name":"readout-source","type":"text","value":"machine"}`, v)
	}
	refused(post(4, machine("181"), nil), domain.CodeStepMessage, "handed out")
	claim := func() *httptest.ResponseRecorder {
		return send(http.MethodPost, "/v1/shopfloor/control-recipes/"+rc.ID+"/readouts", `{"stepKey":"U/O/P/4"}`, "operator-1")
	}
	if rec := claim(); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeNoReading) {
		t.Fatalf("a claim with no reading: %d %s", rec.Code, rec.Body)
	}
	now := time.Now().UTC()
	rd := func(v string, when time.Time) string {
		return fmt.Sprintf(`{"readings":[{"node":"ns=2;s=OvenH","value":%q,"nodeType":"Double","at":%q}]}`, v, when.Format(time.RFC3339))
	}
	if rec := send(http.MethodPost, "/v1/shopfloor/edge-nodes/"+n1.ID+"/readings", rd("181", now.Add(-20*time.Second)), "edge-h1"); rec.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", rec.Code, rec.Body)
	}
	if rec := send(http.MethodPost, "/v1/shopfloor/edge-nodes/"+n2.ID+"/readings", rd("999", now), "edge-h2"); rec.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", rec.Code, rec.Body)
	}
	if rec := send(http.MethodPost, "/v1/shopfloor/edge-nodes/"+n2.ID+"/readings", rd("5", now.Add(time.Hour)), "edge-h2"); rec.Code != http.StatusBadRequest {
		t.Fatalf("a reading from the future: %d", rec.Code)
	}
	rec = claim()
	must(rec, 200)
	if !strings.Contains(rec.Body.String(), `"value":"181"`) {
		t.Fatalf("the claim is not the reading of the site's edge node: %s", rec.Body)
	}
	refused(post(4, machine("182"), nil), domain.CodeStepMessage, "handed out")
	refused(post(4, `{"name":"Oven","type":"numeric","value":"181"},{"name":"readout-source","type":"text","value":"entered-by-hand"}`, nil), domain.CodeStepMessage, "comment")
	rec = post(4, machine("181"), nil)
	must(rec, 201)
	for _, want := range []string{"readout-claim", "readout-edge", `"name":"expected"`, `"value":"180"`, `"name":"tolerance"`, "readout-read-at"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("the stored readout lacks %s: %s", want, rec.Body)
		}
	}
	// Step 5: a blocking timer is recorded only after its time has run out on the service's clock.
	refused(post(5, `{"name":"timer-seconds","type":"numeric","value":"1"}`, nil), domain.CodeTimerRunning, "not run out")
	ctx := context.Background()
	if _, err := e.admin.Exec(ctx, `update process_message set received_at = received_at - interval '10 minutes' where lower(source_message_id) like $1`, rc.ID+":%"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.admin.Exec(ctx, `update control_recipe_event set at = at - interval '10 minutes' where recipe_id = $1`, rc.ID); err != nil {
		t.Fatal(err)
	}
	must(post(5, `{"name":"timer-seconds","type":"numeric","value":"1"}`, nil), 201)
	// Step 6: a message for a signature step is a signature: it needs the step-up, and the service fills in who and what.
	refused2 := post(6, `{"name":"comment","type":"text","value":"done"}`, nil)
	if refused2.Code != http.StatusForbidden || !strings.Contains(refused2.Body.String(), domain.CodeSignatureReauth) {
		t.Fatalf("an unsigned message for a signature step: %d %s", refused2.Code, refused2.Body)
	}
	if refused3 := post(6, `{"name":"signature-meaning","type":"text","value":"performed"}`, map[string]string{tenant.HeaderAuthTime: strconv.FormatInt(time.Now().Unix(), 10)}); refused3.Code != http.StatusUnprocessableEntity {
		t.Fatalf("another meaning: %d %s", refused3.Code, refused3.Body)
	}
	rec = post(6, `{"name":"comment","type":"text","value":"checked"}`, map[string]string{tenant.HeaderAuthTime: strconv.FormatInt(time.Now().Unix(), 10)})
	must(rec, 201)
	if !strings.Contains(rec.Body.String(), `"signed-by"`) || !strings.Contains(rec.Body.String(), `"verified"`) {
		t.Fatalf("the service did not fill in the signer and the meaning: %s", rec.Body)
	}
	// An upper-case recipe id in the source id still reaches the record (L5): step 7 records nothing, so use the readout
	// retry: it is answered from the store whatever the case.
	rec = do(http.MethodPost, "/v1/shopfloor/process-messages", fmt.Sprintf(`{"source":"kiosk","sourceMessageId":"%s:U/O/P/1","categoryCode":"process-data","orderId":%q,"occurredAt":%q,"values":[{"name":"Temp","type":"numeric","value":"77"}]}`, strings.ToUpper(rc.ID), order, at), "operator-1", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"duplicate":true`) {
		t.Fatalf("an upper-case recipe id: %d %s", rec.Code, rec.Body)
	}
	report("completed")
	var list struct{ Items []store.PIExecution }
	rec = send(http.MethodGet, "/v1/shopfloor/pi-executions?recipeId="+rc.ID, "", "operator-1")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Items) != 1 || list.Items[0].DeviationCount != 0 {
		t.Fatalf("a clean run must seal with no deviations: %s", rec.Body)
	}
	var d store.PIExecutionDetail
	rec = send(http.MethodGet, "/v1/shopfloor/pi-executions/"+list.Items[0].ID, "", "operator-1")
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if len(d.Steps) != 7 || d.Steps[3].Deviation || d.Steps[5].SignerUser != "operator-1" || d.Steps[5].SignatureMeaning != "verified" || d.Steps[6].Status != domain.ExecAcknowledged {
		t.Fatalf("steps: %s", rec.Body)
	}
	if rec := send(http.MethodPost, "/v1/shopfloor/pi-executions/"+list.Items[0].ID+":verify", "", "auditor"); !strings.Contains(rec.Body.String(), `"valid":true`) {
		t.Fatalf("verify: %s", rec.Body)
	}
}

// A hot batch jumps over the readout and the timer: nothing may be recorded for them, and the record shows them skipped.
func TestBranchSkipsAreEnforced(t *testing.T) {
	e := newRecipeEnv(t)
	order, resource := ids.New(), ids.New()
	recipe := fmt.Sprintf(`{"orderId":%q,"resourceId":%q,"destination":"kiosk","recipe":{"procedure":{"name":"Batch","unitProcedures":[{"name":"U","operations":[{"name":"O","phases":[{"name":"P","instructions":[
		{"seq":1,"category":"process-data-request","text":"Temp.","var":"temp","characteristic":{"name":"Temp","type":"numeric","min":0,"max":500}},
		{"seq":2,"category":"branch","text":"Hot?","condition":"temp > 80","target":4},
		{"seq":3,"category":"instruction-text","text":"Cool it."},
		{"seq":4,"category":"instruction-text","text":"Carry on."}]}]}]}]}}}`, order, resource)
	var rc store.ControlRecipe
	rec := e.send(http.MethodPost, "/v1/shopfloor/control-recipes", recipe, hardeningDuties, "planner")
	_ = json.Unmarshal(rec.Body.Bytes(), &rc)
	for _, s := range []string{"received", "in-execution"} {
		e.send(http.MethodPost, "/v1/shopfloor/control-recipes/"+rc.ID+":report", `{"status":"`+s+`"}`, hardeningDuties, "operator-1")
	}
	post := func(step int, values string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"source":"kiosk","sourceMessageId":"%s:U/O/P/%d","categoryCode":"process-data","orderId":%q,"occurredAt":%q,"values":[%s]}`, rc.ID, step, order, time.Now().UTC().Format(time.RFC3339), values)
		return e.send(http.MethodPost, "/v1/shopfloor/process-messages", body, hardeningDuties, "operator-1")
	}
	if rec := post(1, `{"name":"Temp","type":"numeric","value":"90"}`); rec.Code != http.StatusCreated {
		t.Fatalf("temp: %d %s", rec.Code, rec.Body)
	}
	if rec := post(2, `{"name":"branch-taken","type":"boolean","value":"true"},{"name":"branch-target","type":"numeric","value":"4"}`); rec.Code != http.StatusCreated {
		t.Fatalf("branch: %d %s", rec.Code, rec.Body)
	}
	if rec := post(3, `{"name":"comment","type":"text","value":"cooled"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "skipped") {
		t.Fatalf("a message for a skipped step: %d %s", rec.Code, rec.Body)
	}
	e.send(http.MethodPost, "/v1/shopfloor/control-recipes/"+rc.ID+":report", `{"status":"completed"}`, hardeningDuties, "operator-1")
	var list struct{ Items []store.PIExecution }
	rec = e.send(http.MethodGet, "/v1/shopfloor/pi-executions?recipeId="+rc.ID, "", hardeningDuties, "operator-1")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	var d store.PIExecutionDetail
	rec = e.send(http.MethodGet, "/v1/shopfloor/pi-executions/"+list.Items[0].ID, "", hardeningDuties, "operator-1")
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if d.Steps[2].Status != domain.ExecSkipped || d.Steps[2].Deviation || d.Steps[3].Status != domain.ExecAcknowledged {
		t.Fatalf("steps: %s", rec.Body)
	}
}
