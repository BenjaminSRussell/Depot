package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/eventing"
	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/reimbursement"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/intercompany/internal/chargefeed"
)

func feedFor(e *registryEnv) *chargefeed.Feed {
	return &chargefeed.Feed{Submit: e.api, Entities: e.st, Config: chargefeed.Config{CostAccount: "6200-IC-COST", RecoveryAccount: "4900-IC-RECOVERY", MarginAccount: "4910-IC-MARGIN"}}
}

func feedCtx(t *testing.T) context.Context {
	t.Helper()
	p, err := tenant.ServicePrincipal(chargefeed.ConsumerName, tenantID, "")
	if err != nil {
		t.Fatal(err)
	}
	return tenant.With(context.Background(), p)
}

func feedEvent(t *testing.T, typ, source string, data any) events.Event {
	t.Helper()
	evt, err := events.New(typ, source, ids.New(), tenantID, data)
	if err != nil {
		t.Fatal(err)
	}
	return evt
}

func chargeCount(t *testing.T, e *registryEnv, source string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `select count(*) from ic_charge_request where source_id = $1`, source).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// #40, AC-40.11 through the event feed: 8 h at 60.00 worked by an employee of entity A on a project of entity B, priced Cost plus 10 %,
// is charged once: A is owed 528.00 (480.00 recovery and 48.00 margin) and B bears 528.00, and the same event delivered again, or
// a second event of the same transaction, changes no balance and stores no second charge.
func TestChargeFeed_ProjectHoursOfAnotherEntityBecomeOneCharge(t *testing.T) {
	e := newRegistryEnv(t)
	e.setup(t, true)
	e.put(t, http.MethodPost, "/v1/interco/transfer-prices", `{"lendingEntity":"A","borrowingEntity":"B","category":"Hour","method":"CostPlus","markupPct":"10","currency":"USD","effectiveFrom":"2026-01-01"}`, http.StatusCreated)
	f := feedFor(e)

	txn := ids.New()
	hours := map[string]any{"transactionId": txn, "version": 1, "projectId": "P-1", "projectLegalEntityId": leB, "workerId": ids.New(), "workerLegalEntityId": leA,
		"categoryId": "LABOR", "sourceDocType": "TIMESHEET", "transactionDate": "2026-11-05", "quantity": "8.000", "costPrice": "60.000000", "currency": "USD"}
	evt := feedEvent(t, chargefeed.TransactionPostedType, chargefeed.ProjectsSource, hours)
	if err := f.HandleHours(feedCtx(t), evt); err != nil {
		t.Fatal(err)
	}
	want := map[[2]string]string{{"L-A", "1500-DUE-FROM-B"}: "528", {"L-A", "4900-IC-RECOVERY"}: "-480", {"L-A", "4910-IC-MARGIN"}: "-48",
		{"L-B", "6200-IC-COST"}: "528", {"L-B", "2500-DUE-TO-A"}: "-528"}
	check := func() {
		t.Helper()
		for k, v := range want {
			if got := netOf(t, e.fake, k[0], k[1]); !got.Equal(decimal.RequireFromString(v)) {
				t.Errorf("%s %s = %s, want %s", k[0], k[1], got, v)
			}
		}
	}
	check()
	entries := len(e.fake.Entries())
	if err := f.HandleHours(feedCtx(t), evt); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if err := f.HandleHours(feedCtx(t), feedEvent(t, chargefeed.TransactionPostedType, chargefeed.ProjectsSource, hours)); err != nil {
		t.Fatalf("the same transaction in a new event: %v", err)
	}
	check()
	if n := len(e.fake.Entries()); n != entries {
		t.Fatalf("ledger entries %d -> %d after redelivery", entries, n)
	}
	if n := chargeCount(t, e, "projects:"+txn); n != 1 {
		t.Fatalf("charges stored for the transaction = %d, want 1", n)
	}

	// Hours that are the project entity's own, or with no employing entity named, raise nothing.
	own := map[string]any{}
	for k, v := range hours {
		own[k] = v
	}
	own["transactionId"], own["workerLegalEntityId"] = ids.New(), leB
	delete(hours, "workerLegalEntityId")
	hours["transactionId"] = ids.New()
	for _, d := range []map[string]any{own, hours} {
		if err := f.HandleHours(feedCtx(t), feedEvent(t, chargefeed.TransactionPostedType, chargefeed.ProjectsSource, d)); err != nil {
			t.Fatal(err)
		}
		if n := chargeCount(t, e, "projects:"+d["transactionId"].(string)); n != 0 {
			t.Fatalf("charge stored for hours that cross no entity")
		}
	}
}

// An approved report's line that another entity bears is charged once from the paying entity: a 300.00 hotel line at cost.
func TestChargeFeed_ExpenseLineBorneByAnotherEntityBecomesOneCharge(t *testing.T) {
	e := newRegistryEnv(t)
	e.setup(t, true)
	e.put(t, http.MethodPost, "/v1/interco/transfer-prices", `{"lendingEntity":"A","borrowingEntity":"B","category":"Expense","method":"Cost","currency":"USD","effectiveFrom":"2026-01-01"}`, http.StatusCreated)
	f := feedFor(e)
	line := ids.New()
	approved := reimbursement.ReportApproved{ReportID: ids.New(), LegalEntityID: leA, WorkerID: ids.New(), Currency: "USD", Amount: "345.50", ApprovedOn: "2026-11-06",
		ChargedLines: []reimbursement.ChargedLine{{LineID: line, LineNo: 1, Category: "TRAVEL", Amount: "300.00", Currency: "USD", TransactionDate: "2026-11-05", ChargeToLegalEntityID: leB}}}
	raw, _ := json.Marshal(approved)
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	evt := feedEvent(t, reimbursement.ReportApprovedType, chargefeed.ExpenseSource, data)
	for i := 0; i < 2; i++ {
		if err := f.HandleExpense(feedCtx(t), evt); err != nil {
			t.Fatalf("delivery %d: %v", i+1, err)
		}
	}
	for k, v := range map[[2]string]string{{"L-A", "1500-DUE-FROM-B"}: "300", {"L-A", "4900-IC-RECOVERY"}: "-300", {"L-B", "6200-IC-COST"}: "300", {"L-B", "2500-DUE-TO-A"}: "-300"} {
		if got := netOf(t, e.fake, k[0], k[1]); !got.Equal(decimal.RequireFromString(v)) {
			t.Errorf("%s %s = %s, want %s", k[0], k[1], got, v)
		}
	}
	if n := chargeCount(t, e, "expense:"+line); n != 1 {
		t.Fatalf("charges stored for the line = %d, want 1", n)
	}
}

func TestChargeFeed_RefusesWhatIsNotAnEventOfTheProducer(t *testing.T) {
	e := newRegistryEnv(t)
	f := feedFor(e)
	d := map[string]any{"transactionId": ids.New(), "version": 1}
	if err := f.HandleHours(feedCtx(t), feedEvent(t, chargefeed.TransactionPostedType, "/depot/sales", d)); err == nil {
		t.Error("another source is refused")
	}
	if err := f.HandleHours(context.Background(), feedEvent(t, chargefeed.TransactionPostedType, chargefeed.ProjectsSource, d)); err == nil {
		t.Error("no principal is refused")
	}
}

// L2: changing the configured accounts does not turn a redelivered event into an idempotency-key reuse, and a revised expense report
// is the charge's next version.
func TestChargeFeed_AccountSettingsAreNotPartOfTheReplayHash(t *testing.T) {
	e := newRegistryEnv(t)
	e.setup(t, true)
	e.put(t, http.MethodPost, "/v1/interco/transfer-prices", `{"lendingEntity":"A","borrowingEntity":"B","category":"Expense","method":"Cost","currency":"USD","effectiveFrom":"2026-01-01"}`, http.StatusCreated)
	f := feedFor(e)
	line := ids.New()
	approved := func(revision int, amount string) events.Event {
		raw, _ := json.Marshal(reimbursement.ReportApproved{ReportID: ids.New(), LegalEntityID: leA, WorkerID: ids.New(), Currency: "USD", Amount: "0.00", ApprovedOn: "2026-11-06", Revision: revision,
			ChargedLines: []reimbursement.ChargedLine{{LineID: line, LineNo: 1, Category: "TRAVEL", Amount: amount, Currency: "USD", TransactionDate: "2026-11-05", ChargeToLegalEntityID: leB}}})
		var data map[string]any
		_ = json.Unmarshal(raw, &data)
		return feedEvent(t, reimbursement.ReportApprovedType, chargefeed.ExpenseSource, data)
	}
	if err := f.HandleExpense(feedCtx(t), approved(1, "300.00")); err != nil {
		t.Fatal(err)
	}
	f.Config.CostAccount = "6300-OTHER-COST"
	if err := f.HandleExpense(feedCtx(t), approved(1, "300.00")); err != nil {
		t.Fatalf("redelivery after an account setting changed: %v", err)
	}
	if n := chargeCount(t, e, "expense:"+line); n != 1 {
		t.Fatalf("charges = %d, want 1", n)
	}
	if err := f.HandleExpense(feedCtx(t), approved(2, "280.00")); err != nil {
		t.Fatalf("revision 2: %v", err)
	}
	if n := chargeCount(t, e, "expense:"+line); n != 2 {
		t.Fatalf("charges after a revision = %d, want 2 (one per version)", n)
	}
}

// M4: an entity the registry does not know is dead-lettered, not skipped.
func TestChargeFeed_AnUnknownEntityIsDeadLettered(t *testing.T) {
	e := newRegistryEnv(t)
	e.setup(t, true)
	f := feedFor(e)
	hours := map[string]any{"transactionId": ids.New(), "version": 1, "projectId": "P-1", "projectLegalEntityId": leB, "workerId": ids.New(), "workerLegalEntityId": ids.New(),
		"categoryId": "LABOR", "sourceDocType": "TIMESHEET", "transactionDate": "2026-11-05", "quantity": "8.000", "costPrice": "60.000000", "currency": "USD"}
	err := f.HandleHours(feedCtx(t), feedEvent(t, chargefeed.TransactionPostedType, chargefeed.ProjectsSource, hours))
	if err == nil || !eventing.IsPermanent(err) {
		t.Fatalf("unknown employing entity = %v; want a permanent failure", err)
	}
}
