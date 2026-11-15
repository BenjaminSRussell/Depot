package statements_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/print/internal/delivery"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
	"github.com/BenjaminSRussell/passage/services/print/internal/statements"
	"github.com/BenjaminSRussell/passage/services/print/internal/store"
	"github.com/BenjaminSRussell/passage/services/print/migrations"
)

const (
	le    = "0190a5f2-7b1c-7d3e-8a4f-123456789a01"
	custA = "0190a5f2-7b1c-7d3e-8a4f-123456789b01"
	custB = "0190a5f2-7b1c-7d3e-8a4f-123456789b02"
	custC = "0190a5f2-7b1c-7d3e-8a4f-123456789b03"
)

type fakeSource struct {
	mu       sync.Mutex
	failFor  map[string]error
	empty    map[string]bool
	statUsed map[string]int
	custErr  error
}

func (f *fakeSource) Customers(context.Context, string) ([]string, error) {
	return []string{custA, custB, custC}, f.custErr
}

func (f *fakeSource) Statement(_ context.Context, _, customer string, asOf time.Time) ([]domain.StatementSection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statUsed[customer]++
	if err := f.failFor[customer]; err != nil {
		return nil, err
	}
	if f.empty[customer] {
		return nil, nil
	}
	due := asOf.Format(time.DateOnly)
	return []domain.StatementSection{{Currency: "USD", ClosingBalance: "100.00", Lines: []domain.StatementLine{
		{SourceType: "INVOICE", DocumentNumber: "INV-" + customer[len(customer)-2:], DocumentDate: due, DueDate: &due, Amount: "100.00", OpenAmount: "100.00", RunningBalance: "100.00"}}}}, nil
}

func (f *fakeSource) CustomerName(_ context.Context, _, customer string) string {
	return map[string]string{custA: "Bluewater Utilities", custB: "Lakeside Pump Supply"}[customer]
}

type mailbox struct {
	mu   sync.Mutex
	sent []delivery.Message
}

func (m *mailbox) Send(_ context.Context, msg delivery.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

type fixture struct {
	st    *store.Postgres
	ctx   context.Context
	mail  *mailbox
	src   *fakeSource
	sched domain.StatementSchedule
	now   time.Time
}

func (f *fixture) runner(owner string) *statements.Runner {
	return &statements.Runner{Schedules: f.st, Docs: f.st, Source: f.src, Owner: owner, Now: func() time.Time { return f.now },
		Deliver: &delivery.Deliverer{Docs: f.st, Log: f.st, Mailer: f.mail}}
}

func setup(t *testing.T, rules ...domain.OutputRule) *fixture {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "admin-1", Duties: testkit.ScopeDuties("")})
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := store.New(app)
	for i, r := range rules {
		r.DocumentType, r.Copies, r.Sequence = domain.DocPeriodicStatement, 1, i+1
		if r.CopyKind == "" {
			r.CopyKind = domain.CopyOriginal
		}
		if _, _, err := st.CreateOutputRule(ctx, r, "rule-"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	sc, _, err := st.CreateSchedule(ctx, domain.StatementSchedule{Name: "Monthly", LegalEntityID: le, SenderName: "Cedar Ridge Pumps, Inc.", Cadence: domain.CadenceMonthly, RunDay: 3, Language: "en"}, "k1")
	if err != nil {
		t.Fatal(err)
	}
	due, _ := time.Parse(time.DateOnly, sc.NextRunOn)
	return &fixture{st: st, ctx: ctx, mail: &mailbox{}, sched: sc, now: due.Add(2 * time.Hour),
		src: &fakeSource{failFor: map[string]error{}, empty: map[string]bool{}, statUsed: map[string]int{}}}
}

func emailAndPortal() []domain.OutputRule {
	return []domain.OutputRule{
		{Channel: domain.ChannelEmail, Destination: "ar@cedar.example"},
		{Channel: domain.ChannelPortal},
	}
}

func total(sums []statements.Summary) (delivered, skipped, failed int) {
	for _, s := range sums {
		delivered, skipped, failed = delivered+s.Delivered, skipped+s.Skipped, failed+s.Failed
	}
	return
}

// Several replicas run at once: every customer's statement goes out once, whichever replica gets there.
func TestRunner_ReplicasProduceEachStatementOnce(t *testing.T) {
	f := setup(t, emailAndPortal()...)
	f.src.empty[custC] = true
	var wg sync.WaitGroup
	var mu sync.Mutex
	var all []statements.Summary
	for _, owner := range []string{"replica-1", "replica-2", "replica-3"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sums, err := f.runner(owner).RunTenant(context.Background(), testkit.TenantID(1))
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			all = append(all, sums...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	delivered, _, failed := total(all)
	if len(all) != 1 || delivered != 2 || failed != 0 {
		t.Fatalf("summaries: %+v", all)
	}
	if len(f.mail.sent) != 2 || f.src.statUsed[custA] != 1 {
		t.Fatalf("mails %d, statement reads of A %d", len(f.mail.sent), f.src.statUsed[custA])
	}
	msg := f.mail.sent[0]
	if msg.To != "ar@cedar.example" || !strings.HasPrefix(msg.Subject, "Statement of account as of ") || !strings.HasSuffix(msg.Filename, ".pdf") || !strings.HasPrefix(string(msg.Attachment), "%PDF-") {
		t.Fatalf("message: %+v", msg)
	}
	docs, err := f.st.ListPortalDocuments(f.ctx, custA, 0)
	if err != nil || len(docs) != 1 || docs[0].DocumentType != domain.DocPeriodicStatement {
		t.Fatalf("portal documents of A: %+v %v", docs, err)
	}
	// The schedule moved on: another pass the same day finds nothing due, and the next month's date runs again.
	if sums, err := f.runner("replica-1").RunTenant(context.Background(), testkit.TenantID(1)); err != nil || len(sums) != 0 {
		t.Fatalf("second pass: %+v %v", sums, err)
	}
	list, _ := f.st.ListSchedules(f.ctx)
	if len(list) != 1 || list[0].LastRunOn != f.now.Format(time.DateOnly) || list[0].NextRunOn <= f.now.Format(time.DateOnly) {
		t.Fatalf("schedule after the run: %+v", list)
	}
	runs, _ := f.st.ListStatementRuns(f.ctx, f.sched.ID, 0)
	if len(runs) != 3 {
		t.Fatalf("runs: %+v", runs)
	}
}

func TestRunner_AFailedStatementIsRetriedByRunNowAndNothingIsSentTwice(t *testing.T) {
	f := setup(t, emailAndPortal()...)
	f.src.failFor[custB] = errors.New("receivables answered 503")
	f.src.empty[custC] = true
	sums, err := f.runner("r1").RunTenant(context.Background(), testkit.TenantID(1))
	if err != nil {
		t.Fatal(err)
	}
	if d, s, fl := total(sums); d != 1 || s != 1 || fl != 1 {
		t.Fatalf("first run: delivered %d skipped %d failed %d", d, s, fl)
	}
	runs, _ := f.st.ListStatementRuns(f.ctx, f.sched.ID, 0)
	var failed store.StatementRun
	for _, r := range runs {
		if r.Status == "FAILED" {
			failed = r
		}
	}
	if failed.CustomerID != custB || !strings.Contains(failed.Detail, "503") {
		t.Fatalf("failed run: %+v", runs)
	}
	delete(f.src.failFor, custB)
	sum, err := f.runner("r1").RunNow(context.Background(), testkit.TenantID(1), f.sched)
	if err != nil || sum.Delivered != 1 || sum.Skipped != 2 || sum.Failed != 0 {
		t.Fatalf("run now: %+v %v", sum, err)
	}
	if len(f.mail.sent) != 2 {
		t.Fatalf("mails: %d, want one per customer with open items", len(f.mail.sent))
	}
}

func TestRunner_SourceDownLeavesTheScheduleDue(t *testing.T) {
	f := setup(t, emailAndPortal()...)
	f.src.custErr = errors.New("receivables answered 503")
	if _, err := f.runner("r1").RunTenant(context.Background(), testkit.TenantID(1)); err == nil {
		t.Fatal("a source failure was not reported")
	}
	list, _ := f.st.ListSchedules(f.ctx)
	if list[0].LastRunOn != "" {
		t.Fatalf("the schedule moved on without running: %+v", list[0])
	}
}

func TestRunner_NoRuleMeansNothingIsSentAndSaysSo(t *testing.T) {
	f := setup(t) // no output rules
	sums, err := f.runner("r1").RunTenant(context.Background(), testkit.TenantID(1))
	if err != nil {
		t.Fatal(err)
	}
	if d, s, _ := total(sums); d != 0 || s != 3 || len(f.mail.sent) != 0 {
		t.Fatalf("summaries: %+v", sums)
	}
}

func TestRunner_UsesTheTemplateOfStatementsAndItsTextBlock(t *testing.T) {
	f := setup(t, domain.OutputRule{Channel: domain.ChannelEmail, Destination: "ar@cedar.example"})
	if _, err := f.st.PutTextBlock(f.ctx, domain.TextBlock{Code: "STMT-COVER", Language: "en", Body: "Dear {{counterpartyName}}, your balance as of {{asOf}} follows."}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.PutTemplate(f.ctx, domain.DocumentTemplate{Code: "STMT-MONTHLY", Name: "Monthly", DocumentType: domain.DocPeriodicStatement, Default: true,
		Config: domain.TemplateConfig{BodyBlock: "STMT-COVER", FooterText: "Cedar Ridge Pumps"}}, 0, "admin-1"); err != nil {
		t.Fatal(err)
	}
	if sums, err := f.runner("r1").RunTenant(context.Background(), testkit.TenantID(1)); err != nil {
		t.Fatal(err)
	} else if d, _, fl := total(sums); d != 3 || fl != 0 {
		t.Fatalf("summaries: %+v", sums)
	}
	if len(f.mail.sent) == 0 {
		t.Fatal("no statement was mailed")
	}
	// A template that needs a value the runner does not know fails that statement with the reason, and sends nothing.
	if _, err := f.st.PutTextBlock(f.ctx, domain.TextBlock{Code: "STMT-COVER", Language: "en", Body: "Dear {{counterpartyName}}, ref {{customerReference}}."}); err != nil {
		t.Fatal(err)
	}
	before := len(f.mail.sent)
	f.now = f.now.AddDate(0, 0, 40)
	sums, _ := f.runner("r1").RunTenant(context.Background(), testkit.TenantID(1))
	if _, _, fl := total(sums); fl == 0 || len(f.mail.sent) != before {
		t.Fatalf("a template with an unknown placeholder: %+v, mails %d -> %d", sums, before, len(f.mail.sent))
	}
}

func TestHTTPSource_ReadsReceivablesAndPartyWithOnlyTheirDuties(t *testing.T) {
	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.RequestURI())
		if r.Header.Get(tenant.HeaderServiceToken) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/receivables/aging-snapshots"):
			_, _ = w.Write([]byte(`{"items":[{"customerId":"` + custA + `","totalOpen":"10.00"},{"customerId":"` + custA + `","totalOpen":"5.00"},{"customerId":"` + custB + `","totalOpen":"0.00"},{"customerId":"` + custC + `","totalOpen":"-3.50"}]}`))
		case strings.HasPrefix(r.URL.Path, "/v1/receivables/statements"):
			_, _ = w.Write([]byte(`{"sections":[{"currency":"USD","closingBalance":"10.00","lines":[{"sourceType":"INVOICE","documentNumber":"INV-1","documentDate":"2026-10-01","dueDate":null,"amount":"10.00","openAmount":"10.00","runningBalance":"10.00"}]}]}`))
		case strings.HasPrefix(r.URL.Path, "/v1/party/customers/"):
			_, _ = w.Write([]byte(`{"partyId":"p-1"}`))
		case r.URL.Path == "/v1/party/parties/p-1":
			_, _ = w.Write([]byte(`{"name":"Bluewater Utilities"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	signer := testSigner(t)
	src, err := statements.NewHTTPSource(srv.URL, srv.URL, signer, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tenant.ServicePrincipal(statements.Principal, testkit.TenantID(1), le)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), p)
	custs, err := src.Customers(ctx, le)
	if err != nil || len(custs) != 2 || custs[0] != custA || custs[1] != custC {
		t.Fatalf("customers: %v %v", custs, err)
	}
	secs, err := src.Statement(ctx, le, custA, time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC))
	if err != nil || len(secs) != 1 || secs[0].Lines[0].DueDate != nil || secs[0].ClosingBalance != "10.00" {
		t.Fatalf("statement: %+v %v", secs, err)
	}
	if name := src.CustomerName(ctx, le, custA); name != "Bluewater Utilities" {
		t.Fatalf("name %q", name)
	}
	if !strings.Contains(strings.Join(gotPaths, " "), "asOf=2026-11-15") || !strings.Contains(strings.Join(gotPaths, " "), "customer="+custA) {
		t.Fatalf("paths: %v", gotPaths)
	}
	if _, err := statements.NewHTTPSource("", srv.URL, signer, nil); err == nil {
		t.Fatal("a source without a receivables URL")
	}
}

// A run with a failed customer is not completed: the schedule keeps its run date, a later pass retries only the failed customer for the
// same date (nobody gets a second statement), and after statements.MaxAttempts the schedule moves on.
func TestRunner_AFailedCustomerKeepsTheRunOpenAndIsRetriedForTheSameDate(t *testing.T) {
	f := setup(t, emailAndPortal()...)
	f.src.failFor[custB] = errors.New("receivables answered 503")
	f.src.empty[custC] = true
	r := f.runner("r1")
	sums, err := r.RunTenant(context.Background(), testkit.TenantID(1))
	if err != nil || len(sums) != 1 || !sums[0].Retry || sums[0].Failed != 1 || sums[0].Delivered != 1 {
		t.Fatalf("first pass: %+v %v", sums, err)
	}
	list, _ := f.st.ListSchedules(f.ctx)
	first := list[0]
	if first.LastRunOn != "" || first.RunAsOf != sums[0].AsOf || first.Attempts != 1 || first.NextRunOn > sums[0].AsOf {
		t.Fatalf("the run was completed or lost its date: %+v", first)
	}
	// Before the retry wait nothing is due.
	if sums, _ = r.RunTenant(context.Background(), testkit.TenantID(1)); len(sums) != 0 {
		t.Fatalf("retried before the wait: %+v", sums)
	}
	// Later, on another day, the failed customer is retried for the original date and the one already sent is left alone.
	delete(f.src.failFor, custB)
	f.now = f.now.Add(statements.RetryWait + 36*time.Hour)
	sums, err = r.RunTenant(context.Background(), testkit.TenantID(1))
	if err != nil || len(sums) != 1 || sums[0].AsOf != first.RunAsOf || sums[0].Delivered != 1 || sums[0].Skipped != 2 || sums[0].Failed != 0 || sums[0].Retry {
		t.Fatalf("retry pass: %+v %v", sums, err)
	}
	if len(f.mail.sent) != 2 || f.src.statUsed[custA] != 1 {
		t.Fatalf("mails %d, reads of A %d: a customer got a second statement", len(f.mail.sent), f.src.statUsed[custA])
	}
	list, _ = f.st.ListSchedules(f.ctx)
	if list[0].RunAsOf != "" || list[0].Attempts != 0 || list[0].LastRunOn != first.RunAsOf {
		t.Fatalf("after the retry: %+v", list[0])
	}
}

func TestRunner_GivesUpAfterTheAttemptCap(t *testing.T) {
	f := setup(t, emailAndPortal()...)
	f.src.failFor[custB] = errors.New("receivables answered 503")
	f.src.empty[custC] = true
	r := f.runner("r1")
	for i := 0; i < statements.MaxAttempts; i++ {
		sums, err := r.RunTenant(context.Background(), testkit.TenantID(1))
		if err != nil || len(sums) != 1 {
			t.Fatalf("pass %d: %+v %v", i, sums, err)
		}
		if want := i < statements.MaxAttempts-1; sums[0].Retry != want {
			t.Fatalf("pass %d retry=%v, want %v", i, sums[0].Retry, want)
		}
		f.now = f.now.Add(statements.RetryWait * 10)
	}
	list, _ := f.st.ListSchedules(f.ctx)
	if list[0].RunAsOf != "" || list[0].LastRunOn == "" {
		t.Fatalf("the schedule did not move on: %+v", list[0])
	}
}

func TestHTTPSource_PagesThroughEverySnapshotRow(t *testing.T) {
	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		off := r.URL.Query().Get("offset")
		offsets = append(offsets, off)
		var rows []string
		n := 500
		if off != "0" {
			n = 3
		}
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("0190a5f2-7b1c-7d3e-8a4f-%012d", i)
			if off != "0" {
				id = fmt.Sprintf("0190a5f2-7b1c-7d3e-8a4f-%012d", i) // the same customers again, in another currency
			}
			rows = append(rows, `{"customerId":"`+id+`","totalOpen":"1.00"}`)
		}
		_, _ = w.Write([]byte(`{"items":[` + strings.Join(rows, ",") + `]}`))
	}))
	defer srv.Close()
	src, _ := statements.NewHTTPSource(srv.URL, srv.URL, testSigner(t), nil)
	p, _ := tenant.ServicePrincipal(statements.Principal, testkit.TenantID(1), le)
	got, err := src.Customers(tenant.With(context.Background(), p), le)
	if err != nil || len(got) != 500 || len(offsets) != 2 || offsets[1] != "500" {
		t.Fatalf("customers %d offsets %v %v", len(got), offsets, err)
	}
}
