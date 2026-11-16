package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
)

func fl(f float64) *float64 { return &f }

func kioskDoc() domain.RecipeDoc {
	ins := []domain.Instruction{
		{Seq: 1, Category: domain.InstDataRequest, Text: "Temp.", Var: "temp", Char: &domain.InstructionCharacteristic{Name: "Temp", Type: "numeric", Min: fl(10), Max: fl(100)}},
		{Seq: 2, Category: domain.InstReadout, Text: "Oven.", Node: "ns=2;s=OvenS", Value: fl(180), Tolerance: fl(5), Char: &domain.InstructionCharacteristic{Name: "Oven", Type: "numeric", Min: fl(150), Max: fl(210)}},
		{Seq: 3, Category: domain.InstSignature, Text: "Sign.", Meaning: "verified"},
	}
	return domain.RecipeDoc{Procedure: domain.Procedure{Name: "B", UnitProcedures: []domain.UnitProcedure{{Name: "U", Operations: []domain.Operation{{Name: "O", Phases: []domain.Phase{{Name: "P", Instructions: ins}}}}}}}}
}

// Node readings and readout claims through Postgres: only the bound connector reports, a newer reading replaces an older one,
// freshness is by when the service received it, the lookup is limited to the edge nodes at the sites of the recipe's
// resource, and once a step has a message its claim no longer moves.
func TestNodeReadingsAndReadoutClaims(t *testing.T) {
	s, _, admin := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(5911))
	site1, site2 := ids.New(), ids.New()
	subj1, subj2 := "edge-s1", "edge-s2"
	n1, err := s.CreateEdgeNode(ctx, EdgeNodeInput{SiteID: &site1, Name: "Line 1", Platform: "k3s", Ring: "all", ConnectorSubject: &subj1, Active: true}, "eng", Idem{Key: "e1", Fingerprint: "e1"})
	if err != nil {
		t.Fatal(err)
	}
	n2, err := s.CreateEdgeNode(ctx, EdgeNodeInput{SiteID: &site2, Name: "Line 2", Platform: "k3s", Ring: "all", ConnectorSubject: &subj2, Active: true}, "eng", Idem{Key: "e2", Fingerprint: "e2"})
	if err != nil {
		t.Fatal(err)
	}
	resource := ids.New()
	if _, err := s.RegisterMachine(ctx, Machine{Code: "M-5911", ResourceID: resource, SiteID: &site1, IdealCycleSeconds: decimal.NewFromInt(60)}, Idem{Key: "m1", Fingerprint: "m1"}); err != nil {
		t.Fatal(err)
	}
	rc, err := s.CreateControlRecipe(ctx, ControlRecipeInput{OrderID: ids.New(), ResourceID: resource, Destination: domain.DestKiosk, Recipe: kioskDoc()}, "planner", Idem{Key: "r1", Fingerprint: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.ReportNodeReadings(ctx, n1.ID, "someone-else", []NodeReadingIn{{Node: "ns=2;s=OvenS", Value: decimal.NewFromInt(1), At: now}}); problemCode(err) != CodeEdgeNotConnector {
		t.Fatalf("a caller that is not the bound connector = %v", err)
	}
	if _, err := s.ClaimReadout(ctx, rc.ID, "U/O/P/2", 5*time.Minute, now); problemCode(err) != CodeNoReading {
		t.Fatalf("no reading yet = %v", err)
	}
	if _, err := s.ClaimReadout(ctx, rc.ID, "U/O/P/1", 5*time.Minute, now); problemCode(err) != domain.CodeStepMessage {
		t.Fatalf("a claim for a step that is not a readout = %v", err)
	}
	// Line 2 reports later and higher, but the recipe's resource is at Line 1's site.
	if err := s.ReportNodeReadings(ctx, n1.ID, subj1, []NodeReadingIn{{Node: "ns=2;s=OvenS", Value: decimal.RequireFromString("181.5"), NodeType: "Double", At: now.Add(-20 * time.Second)}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReportNodeReadings(ctx, n2.ID, subj2, []NodeReadingIn{{Node: "ns=2;s=OvenS", Value: decimal.RequireFromString("999"), NodeType: "Double", At: now}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimReadout(ctx, rc.ID, "U/O/P/2", 5*time.Minute, now)
	if err != nil || !got.Value.Equal(decimal.RequireFromString("181.5")) || got.EdgeID != n1.ID || got.ClaimID == "" {
		t.Fatalf("claim = %+v, %v", got, err)
	}
	// An older reading does not replace a newer one for the same edge node; a newer one does, and the claim follows until
	// a message is recorded for the step.
	if err := s.ReportNodeReadings(ctx, n1.ID, subj1, []NodeReadingIn{{Node: "ns=2;s=OvenS", Value: decimal.NewFromInt(5), At: now.Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.ClaimReadout(ctx, rc.ID, "U/O/P/2", 5*time.Minute, now); !g.Value.Equal(decimal.RequireFromString("181.5")) {
		t.Fatalf("an older reading replaced a newer one: %+v", g)
	}
	if err := s.ReportNodeReadings(ctx, n1.ID, subj1, []NodeReadingIn{{Node: "ns=2;s=OvenS", Value: decimal.RequireFromString("182"), At: now.Add(-5 * time.Second)}}); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.ClaimReadout(ctx, rc.ID, "U/O/P/2", 5*time.Minute, now); !g.Value.Equal(decimal.NewFromInt(182)) {
		t.Fatalf("a newer reading did not replace: %+v", g)
	}
	// Freshness is by receipt: nothing is fresh when the window is behind the receipt time.
	if _, err := s.ClaimReadout(ctx, rc.ID, "U/O/P/2", 5*time.Minute, now.Add(time.Hour)); problemCode(err) != CodeNoReading {
		t.Fatalf("a stale reading = %v", err)
	}
	// With a message stored for the step the claim is fixed.
	cat, _ := domain.CategoryByCode("process-data")
	if _, err := s.IngestProcessMessage(ctx, domain.NewMessage{Source: "kiosk", SourceMessageID: rc.ID + ":U/O/P/2", CategoryCode: "process-data", OrderID: rc.OrderID, OccurredAt: now,
		Values: []domain.Characteristic{{Name: "Oven", Type: "numeric", Value: "182"}}}, cat, now); err != nil {
		t.Fatal(err)
	}
	if err := s.ReportNodeReadings(ctx, n1.ID, subj1, []NodeReadingIn{{Node: "ns=2;s=OvenS", Value: decimal.NewFromInt(190), At: now}}); err != nil {
		t.Fatal(err)
	}
	if g, err := s.ClaimReadout(ctx, rc.ID, "U/O/P/2", 5*time.Minute, now); err != nil || !g.Value.Equal(decimal.NewFromInt(182)) {
		t.Fatalf("the claim moved after the step was recorded: %+v, %v", g, err)
	}
	var claims int
	if err := admin.QueryRow(context.Background(), `select count(*) from pi_readout_claim where recipe_id = $1`, rc.ID).Scan(&claims); err != nil || claims != 1 {
		t.Fatalf("claims = %d, %v", claims, err)
	}
	// Another tenant sees none of it.
	if _, err := s.ClaimReadout(testkit.Ctx(testkit.TenantID(5912)), rc.ID, "U/O/P/2", 5*time.Minute, now); problemCode(err) != CodeRecipeNotFound {
		t.Fatalf("another tenant claims = %v", err)
	}
}

// Sealing through Postgres: the completion seals the record from the recipe and the stored messages (not the client's view),
// a signature step with no signer is missing, the record verifies, the append-only triggers refuse every change, and a
// change made with the guard switched off is found.
func TestSealingAndAppendOnlyTriggers(t *testing.T) {
	s, _, admin := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(5913))
	resource, order := ids.New(), ids.New()
	rc, err := s.CreateControlRecipe(ctx, ControlRecipeInput{OrderID: order, ResourceID: resource, Destination: domain.DestKiosk, Recipe: kioskDoc()}, "planner", Idem{Key: "r1", Fingerprint: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"received", "in-execution"} {
		if _, err := s.ReportControlRecipe(ctx, rc.ID, to, nil, "operator-1"); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	cat, _ := domain.CategoryByCode("process-data")
	put := func(key string, vals ...domain.Characteristic) {
		t.Helper()
		if _, err := s.IngestProcessMessage(ctx, domain.NewMessage{Source: "kiosk", SourceMessageID: rc.ID + ":" + key, CategoryCode: "process-data", OrderID: order, OccurredAt: now, Values: vals}, cat, now); err != nil {
			t.Fatal(err)
		}
	}
	// Stored messages as an older client could have left them: the limits it claims are wide, the readout is "machine" with no
	// claim, and the signature step has a message with no signer.
	put("U/O/P/1", domain.Characteristic{Name: "Temp", Type: "numeric", Value: "150", Min: strp("0"), Max: strp("1000")})
	put("U/O/P/2", domain.Characteristic{Name: "Oven", Type: "numeric", Value: "181"}, domain.Characteristic{Name: "readout-source", Type: "text", Value: "machine"})
	put("U/O/P/3", domain.Characteristic{Name: "signature-meaning", Type: "text", Value: "verified"})
	if _, err := s.ReportControlRecipe(ctx, rc.ID, "completed", nil, "operator-1"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListPIExecutions(ctx, PIExecutionFilter{RecipeID: rc.ID, Limit: 10})
	if err != nil || len(list) != 1 {
		t.Fatalf("records = %+v, %v", list, err)
	}
	x := list[0]
	d, err := s.GetPIExecution(ctx, x.ID)
	if err != nil || len(d.Steps) != 3 {
		t.Fatalf("detail = %+v, %v", d, err)
	}
	why := func(i int) string { return strings.Join(d.Steps[i].DeviationReasons, "; ") }
	if !strings.Contains(why(0), "outside its limits") || !strings.Contains(why(1), "no machine reading is on record") || d.Steps[2].Status != domain.ExecMissing || !strings.Contains(why(2), "not signed") {
		t.Fatalf("deviations: %q | %q | %q (%s)", why(0), why(1), why(2), d.Steps[2].Status)
	}
	if x.DeviationCount != 3 || x.ExecutedBy != "operator-1" {
		t.Fatalf("header: %+v", x)
	}
	if r, err := s.VerifyPIExecution(ctx, x.ID); err != nil || !r.Valid || r.CheckedSteps != 3 {
		t.Fatalf("verify = %+v, %v", r, err)
	}
	// A second completion makes no second record.
	if _, err := s.ReportControlRecipe(ctx, rc.ID, "completed", nil, "operator-1"); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.ListPIExecutions(ctx, PIExecutionFilter{RecipeID: rc.ID, Limit: 10}); len(again) != 1 {
		t.Fatalf("records after a repeated completion: %d", len(again))
	}
	bg := context.Background()
	for _, stmt := range []string{
		`update pi_execution set deviation_count = 0 where id = '` + x.ID + `'`,
		`update pi_execution_step set deviation = false where execution_id = '` + x.ID + `'`,
		`delete from pi_execution_step where execution_id = '` + x.ID + `'`,
		`delete from pi_execution where id = '` + x.ID + `'`,
		`truncate pi_execution_step`,
	} {
		if _, err := admin.Exec(bg, stmt); err == nil || !strings.Contains(err.Error(), "append only") {
			t.Errorf("%s: not refused as append only: %v", stmt, err)
		}
	}
	// The deviation count and the sheet are covered by the record hash and recounted from the steps.
	for _, stmt := range []string{
		`alter table pi_execution disable trigger pi_execution_append_only`,
		`update pi_execution set deviation_count = 0 where id = '` + x.ID + `'`,
		`alter table pi_execution enable trigger pi_execution_append_only`,
	} {
		if _, err := admin.Exec(bg, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if r, _ := s.VerifyPIExecution(ctx, x.ID); r.Valid || r.BrokenAtSeq == nil || *r.BrokenAtSeq != 0 || !strings.Contains(r.Reason, "deviations") {
		t.Fatalf("a hidden deviation count was not found: %+v", r)
	}
}

func strp(s string) *string { return &s }

// The template trigger: a published version becomes superseded only with a successor, and its authorship and rev stay.
func TestStepTemplateTriggerKeepsAuthorshipOnSupersede(t *testing.T) {
	s, _, admin := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(5914))
	spec := domain.Instruction{Category: domain.InstText, Text: "Check."}
	v1, err := s.CreatePIStep(ctx, PIStepInput{Code: "TRG", Name: "Trigger", Spec: spec}, "author")
	if err != nil {
		t.Fatal(err)
	}
	if v1, err = s.PublishPIStep(ctx, v1.ID, v1.Rev, "author"); err != nil {
		t.Fatal(err)
	}
	bg := context.Background()
	for name, stmt := range map[string]string{
		"superseded with no successor": `update pi_step_template set status = 'superseded' where id = '` + v1.ID + `'`,
		"authorship rewritten":         `update pi_step_template set created_by = 'mallory' where id = '` + v1.ID + `'`,
		"spec rewritten":               `update pi_step_template set spec = '{"category":"instruction-text","text":"x"}' where id = '` + v1.ID + `'`,
	} {
		if _, err := admin.Exec(bg, stmt); err == nil || !strings.Contains(err.Error(), "cannot be changed") {
			t.Errorf("%s was not refused: %v", name, err)
		}
	}
	v2, err := s.NewPIStepVersion(ctx, v1.ID, "author")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(bg, `update pi_step_template set status = 'superseded', created_by = 'mallory', rev = 99 where id = '`+v1.ID+`'`); err == nil {
		t.Fatal("authorship and rev were rewritten on supersede")
	}
	if _, err := s.PublishPIStep(ctx, v2.ID, v2.Rev, "author"); err != nil {
		t.Fatal(err)
	}
	old, err := s.GetPIStep(ctx, v1.ID)
	if err != nil || old.Status != "superseded" || old.CreatedBy != "author" || old.Rev != v1.Rev {
		t.Fatalf("superseded = %+v, %v", old, err)
	}
}
