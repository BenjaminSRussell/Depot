package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/tax/internal/api"
	"github.com/BenjaminSRussell/depot/services/tax/internal/domain"
	"github.com/BenjaminSRussell/depot/services/tax/internal/saft"
	"github.com/BenjaminSRussell/depot/services/tax/internal/store"
	"github.com/BenjaminSRussell/depot/services/tax/migrations"
)

type fakeStore struct {
	queued     []string
	queueErr   error
	items      []store.Recommit
	processed  []json.RawMessage
	processErr []error
	totals     []store.Total
	recorded   []domain.Txn
	config     *store.LedgerConfig
}

func (f *fakeStore) RecordTransactions(_ context.Context, txns []domain.Txn) (int, error) {
	f.recorded = txns
	return len(txns), nil
}

func (f *fakeStore) SetLedgerConfig(_ context.Context, cfg store.LedgerConfig) error {
	f.config = &cfg
	return nil
}

func (f *fakeStore) LedgerConfig(context.Context) (store.LedgerConfig, error) {
	if f.config == nil {
		return store.LedgerConfig{}, store.ErrNoLedgerConfig
	}
	return *f.config, nil
}

func (f *fakeStore) SettleRun(context.Context, domain.Period, string, func(store.LedgerConfig) domain.GLMovement) (store.Run, error) {
	return store.Run{}, nil
}

func (f *fakeStore) UnpostedRuns(context.Context) ([]store.Run, error) { return nil, nil }

func (f *fakeStore) MarkRunPosted(context.Context, string, string) error { return nil }

func (f *fakeStore) Totals(context.Context, domain.Period) ([]store.Total, error) {
	return f.totals, nil
}

func (f *fakeStore) EnqueueRecommit(_ context.Context, provider, _ string, request json.RawMessage) (string, error) {
	if f.queueErr != nil {
		return "", f.queueErr
	}
	f.queued = append(f.queued, provider)
	f.items = append(f.items, store.Recommit{ID: ids.New(), Provider: provider, Request: request})
	return ids.New(), nil
}

func (f *fakeStore) Recommit(ctx context.Context, _ int, process func(context.Context, store.Recommit) (json.RawMessage, error)) (store.RecommitSummary, error) {
	var sum store.RecommitSummary
	for _, item := range f.items {
		sum.Processed++
		out, err := process(ctx, item)
		f.processed = append(f.processed, out)
		f.processErr = append(f.processErr, err)
		if err != nil {
			sum.Failed++
		} else {
			sum.Completed++
		}
	}
	return sum, nil
}

type recoveringProvider struct{ downProvider }

func (recoveringProvider) Calculate(_ context.Context, doc domain.Document) (domain.Result, error) {
	return domain.Result{Engine: "avalara", Lines: []domain.LineResult{{LineRef: doc.Lines[0].LineRef}}}, nil
}

func routerWith(s api.Store, providers map[string]domain.TaxProvider, l ledgerclient.Ledger) (http.Handler, *api.API) {
	a := api.New(fakeDB{}, providers, 100*time.Millisecond).WithStore(s)
	if l != nil {
		a.WithLedger(l)
	}
	r := httpx.NewRouter()
	a.Register(r)
	return r, a
}

func TestFallbackQueuesTheDocumentForRecommit(t *testing.T) {
	withProvider := mutated(t, `"party"`, `"provider":"avalara","party"`)
	fs := &fakeStore{}
	r, a := routerWith(fs, map[string]domain.TaxProvider{"avalara": downProvider{}}, nil)
	if rec := do(r, http.MethodPost, calculatePath, withProvider, testkit.TenantID(1), duty); rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if len(fs.queued) != 1 || fs.queued[0] != "avalara" {
		t.Fatalf("queued %v, want one avalara entry", fs.queued)
	}

	t.Run("recommit succeeds once the provider answers", func(t *testing.T) {
		healthy, _ := routerWith(fs, map[string]domain.TaxProvider{"avalara": recoveringProvider{}}, nil)
		rec := do(healthy, http.MethodPost, "/v1/tax/recommit:run", ``, testkit.TenantID(1), "tax.recommit.run")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"completed":1`) {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(string(fs.processed[len(fs.processed)-1]), `"engine":"avalara"`) {
			t.Fatalf("recommit result %s", fs.processed[len(fs.processed)-1])
		}
	})

	t.Run("recommit keeps the entry while the provider is still down", func(t *testing.T) {
		before := len(fs.processErr)
		if rec := do(r, http.MethodPost, "/v1/tax/recommit:run", ``, testkit.TenantID(1), "tax.recommit.run"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"failed":1`) {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
		if err := fs.processErr[before]; err == nil || !strings.Contains(err.Error(), "still unavailable") {
			t.Fatalf("process error %v", err)
		}
	})

	t.Run("recommit without the provider configured fails the entry", func(t *testing.T) {
		before := len(fs.processErr)
		bare, _ := routerWith(fs, nil, nil)
		do(bare, http.MethodPost, "/v1/tax/recommit:run", ``, testkit.TenantID(1), "tax.recommit.run")
		if err := fs.processErr[before]; err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("process error %v", err)
		}
	})
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "tax_provider_fallbacks_total 1") {
		t.Errorf("metrics %s", metrics.Body.String())
	}
}

func TestFallbackStillAnswersWhenTheQueueFails(t *testing.T) {
	fs := &fakeStore{queueErr: errors.New("db down")}
	r, a := routerWith(fs, map[string]domain.TaxProvider{"avalara": downProvider{}}, nil)
	rec := do(r, http.MethodPost, calculatePath, mutated(t, `"party"`, `"provider":"avalara","party"`), testkit.TenantID(1), duty)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"fallback":true`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "tax_recommit_queue_failures_total 1") {
		t.Errorf("queue failure not counted: %s", metrics.Body.String())
	}
}

func TestRecommitRejectsABadLimit(t *testing.T) {
	r, _ := routerWith(&fakeStore{}, nil, nil)
	for _, limit := range []string{"0", "51", "x"} {
		if rec := do(r, http.MethodPost, "/v1/tax/recommit:run?limit="+limit, ``, testkit.TenantID(1), "tax.recommit.run"); rec.Code != http.StatusBadRequest {
			t.Errorf("limit %s: got %d", limit, rec.Code)
		}
	}
}

func TestRecordTransactionsValidation(t *testing.T) {
	good := `{"id":"` + ids.New() + `","code":"VAT19","direction":"output","tax_date":"2026-01-10","amount":"19.00"}`
	tests := []struct {
		name string
		body string
		want int
	}{
		{"valid", `{"transactions":[` + good + `]}`, http.StatusCreated},
		{"empty list", `{"transactions":[]}`, http.StatusBadRequest},
		{"bad id", `{"transactions":[{"id":"x","code":"VAT19","direction":"output","tax_date":"2026-01-10","amount":"1"}]}`, http.StatusBadRequest},
		{"bad direction", `{"transactions":[{"id":"` + ids.New() + `","code":"VAT19","direction":"sideways","tax_date":"2026-01-10","amount":"1"}]}`, http.StatusBadRequest},
		{"code with separator", `{"transactions":[{"id":"` + ids.New() + `","code":"A|B","direction":"output","tax_date":"2026-01-10","amount":"1"}]}`, http.StatusBadRequest},
		{"missing date", `{"transactions":[{"id":"` + ids.New() + `","code":"VAT19","direction":"output","amount":"1"}]}`, http.StatusBadRequest},
		{"unknown field", `{"transactions":[],"x":1}`, http.StatusBadRequest},
		{"input tax with a recoverable amount", `{"transactions":[{"id":"` + ids.New() + `","code":"VAT19","direction":"input","tax_date":"2026-01-10","amount":"19.00","recoverable_amount":"9.50"}]}`, http.StatusCreated},
		{"recoverable above the tax", `{"transactions":[{"id":"` + ids.New() + `","code":"VAT19","direction":"input","tax_date":"2026-01-10","amount":"19.00","recoverable_amount":"20.00"}]}`, http.StatusBadRequest},
		{"recoverable on an output tax", `{"transactions":[{"id":"` + ids.New() + `","code":"VAT19","direction":"output","tax_date":"2026-01-10","amount":"19.00","recoverable_amount":"9.50"}]}`, http.StatusBadRequest},
		{"recoverable of the wrong sign", `{"transactions":[{"id":"` + ids.New() + `","code":"VAT19","direction":"input","tax_date":"2026-01-10","amount":"19.00","recoverable_amount":"-5.00"}]}`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := routerWith(&fakeStore{}, nil, nil)
			if rec := do(r, http.MethodPost, "/v1/tax/transactions", tc.body, testkit.TenantID(1), "tax.transaction.record"); rec.Code != tc.want {
				t.Fatalf("got %d %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}

func TestSettleRequestValidationAndUnconfiguredDependencies(t *testing.T) {
	valid := `{"period_from":"2026-01-01","period_to":"2026-01-31"}`
	tests := []struct {
		name   string
		body   string
		ledger ledgerclient.Ledger
		want   int
	}{
		{"ledger not configured", valid, nil, http.StatusServiceUnavailable},
		{"inverted period", strings.Replace(valid, "2026-01-31", "2025-12-31", 1), ledgerclient.NewFake(), http.StatusBadRequest},
		{"request-supplied ledger id", `{"period_from":"2026-01-01","period_to":"2026-01-31","ledger_id":"` + ids.New() + `"}`, ledgerclient.NewFake(), http.StatusBadRequest},
		{"malformed settlement account", `{"period_from":"2026-01-01","period_to":"2026-01-31","settlement_account":"29 00;"}`, ledgerclient.NewFake(), http.StatusBadRequest},
		{"request-supplied accounts", `{"period_from":"2026-01-01","period_to":"2026-01-31","accounts":{"VAT19|output":"2200"}}`, ledgerclient.NewFake(), http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := routerWith(&fakeStore{}, nil, tc.ledger)
			if rec := do(r, http.MethodPost, "/v1/tax/settlements:run", tc.body, testkit.TenantID(1), "tax.settlement.run"); rec.Code != tc.want {
				t.Fatalf("got %d %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
	t.Run("store not configured", func(t *testing.T) {
		r, _ := newRouter(fakeDB{}, nil)
		if rec := do(r, http.MethodPost, "/v1/tax/settlements:run", valid, testkit.TenantID(1), "tax.settlement.run"); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("got %d", rec.Code)
		}
	})
}

func TestLedgerConfigValidationAndStorage(t *testing.T) {
	valid := `{"ledger_id":"` + ids.New() + `","accounts":{"VAT19|output":"2200"}}`
	tests := []struct {
		name string
		body string
		want int
	}{
		{"valid", valid, http.StatusOK},
		{"bad ledger id", strings.Replace(valid, `"ledger_id":"`, `"ledger_id":"x`, 1), http.StatusBadRequest},
		{"no accounts", `{"ledger_id":"` + ids.New() + `","accounts":{}}`, http.StatusBadRequest},
		{"account key without direction", strings.Replace(valid, "VAT19|output", "VAT19", 1), http.StatusBadRequest},
		{"malformed settlement account", strings.Replace(valid, `"accounts"`, `"settlement_account":"29 00;","accounts"`, 1), http.StatusBadRequest},
		{"account number injection", strings.Replace(valid, `"2200"`, `"22 00;"`, 1), http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStore{}
			r, _ := routerWith(fs, nil, nil)
			rec := do(r, http.MethodPut, "/v1/tax/ledger-config", tc.body, testkit.TenantID(1), "tax.ledgerconfig.manage")
			if rec.Code != tc.want {
				t.Fatalf("got %d %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
			if stored := fs.config != nil; stored != (tc.want == http.StatusOK) {
				t.Fatalf("config stored %v for status %d", stored, tc.want)
			}
		})
	}
	r, _ := routerWith(&fakeStore{}, nil, nil)
	if rec := do(r, http.MethodPut, "/v1/tax/ledger-config", valid, testkit.TenantID(1), "tax.settlement.run"); rec.Code != http.StatusForbidden {
		t.Fatalf("settlement duty reached the ledger config: %d", rec.Code)
	}
}

func TestSAFTExport(t *testing.T) {
	fs := &fakeStore{totals: []store.Total{{Code: "VAT19", Direction: domain.DirOutput, Amount: decimal.RequireFromString("19"), Count: 1}}}
	r, _ := routerWith(fs, nil, nil)
	query := "?period_from=2026-01-01&period_to=2026-01-31&company_name=Acme&registration_number=DE1&country=DE&currency=EUR"
	rec := do(r, http.MethodGet, "/v1/tax/saft"+query, ``, testkit.TenantID(1), "tax.saft.export")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/xml") ||
		!strings.Contains(rec.Body.String(), "<TaxCode>VAT19</TaxCode>") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	for name, q := range map[string]string{
		"no period":       "?company_name=Acme&registration_number=DE1&country=DE&currency=EUR",
		"bad currency":    strings.Replace(query, "EUR", "eur", 1),
		"missing company": strings.Replace(query, "company_name=Acme", "company_name=", 1),
	} {
		if rec := do(r, http.MethodGet, "/v1/tax/saft"+q, ``, testkit.TenantID(1), "tax.saft.export"); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d", name, rec.Code)
		}
	}
}

func TestSettleThroughTheRouterReconcilesToTheLedger(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	entity, ledgerID := ids.New(), ids.New()
	fake := ledgerclient.NewFake()
	fake.AddLedger(ledgerID, entity)
	for key, lines := range map[string][]ledgerclient.Line{
		"sale":     {{Account: "1200", Amount: decimal.RequireFromString("19.00")}, {Account: "2200", Credit: true, Amount: decimal.RequireFromString("19.00")}},
		"purchase": {{Account: "1400", Amount: decimal.RequireFromString("5.00")}, {Account: "2100", Credit: true, Amount: decimal.RequireFromString("5.00")}},
	} {
		if _, err := fake.Post(context.Background(), ledgerclient.Entry{LegalEntityID: entity, LedgerID: ledgerID, DocumentID: key, PostingDate: "2026-01-15", Currency: "EUR", IdempotencyKey: key, Lines: lines}); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := routerWith(store.New(app), nil, fake)
	var callMethod func(method, path, body string) *httptest.ResponseRecorder
	call := func(path, body string) *httptest.ResponseRecorder { return callMethod(http.MethodPost, path, body) }
	callMethod = func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set(tenant.HeaderTenant, testkit.TenantID(7))
		req.Header.Set(tenant.HeaderLegalEntity, entity)
		req.Header.Set(tenant.HeaderDuties, "tax.transaction.record,tax.settlement.run,tax.ledgerconfig.manage")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	record := `{"transactions":[` +
		`{"id":"` + ids.New() + `","code":"VAT19","direction":"output","tax_date":"2026-01-15","amount":"19.00"},` +
		`{"id":"` + ids.New() + `","code":"VAT19","direction":"input","tax_date":"2026-01-15","amount":"5.00"}]}`
	if rec := call("/v1/tax/transactions", record); rec.Code != http.StatusCreated {
		t.Fatalf("record: %d %s", rec.Code, rec.Body.String())
	}
	settle := `{"period_from":"2026-01-01","period_to":"2026-01-31"}`

	if rec := call("/v1/tax/settlements:run", settle); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "COM-0503") {
		t.Fatalf("settle without stored config: %d %s", rec.Code, rec.Body.String())
	}
	config := `{"ledger_id":"` + ledgerID + `","accounts":{"VAT19|output":"2200","VAT19|input":"1400"}}`
	if rec := callMethod(http.MethodPut, "/v1/tax/ledger-config", config); rec.Code != http.StatusOK {
		t.Fatalf("config: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("/v1/tax/settlements:run", settle); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "COM-0503") {
		t.Fatalf("settle without a settlement account: %d %s", rec.Code, rec.Body.String())
	}
	config = strings.Replace(config, `"accounts"`, `"settlement_account":"2900","accounts"`, 1)
	if rec := callMethod(http.MethodPut, "/v1/tax/ledger-config", config); rec.Code != http.StatusOK {
		t.Fatalf("config: %d %s", rec.Code, rec.Body.String())
	}

	rec := call("/v1/tax/settlements:run", settle)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"net_amount":"14"`) {
		t.Fatalf("settle: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"posted_entry_id":"`) {
		t.Fatalf("run carries no ledger entry: %s", rec.Body.String())
	}
	if rec := call("/v1/tax/settlements:run", settle); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "TAX-0009") {
		t.Fatalf("second run: %d %s", rec.Code, rec.Body.String())
	}

	extra := `{"transactions":[{"id":"` + ids.New() + `","code":"VAT19","direction":"output","tax_date":"2026-01-20","amount":"0.01"}]}`
	if rec := call("/v1/tax/transactions", extra); rec.Code != http.StatusCreated {
		t.Fatalf("record extra: %d", rec.Code)
	}
	if rec := call("/v1/tax/settlements:run", settle); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "TAX-0008") {
		t.Fatalf("one cent off the GL: %d %s", rec.Code, rec.Body.String())
	} else if strings.Contains(rec.Body.String(), "19.0") {
		t.Fatalf("mismatch response leaks GL amounts: %s", rec.Body.String())
	}
}

type settlementHarness struct {
	t      *testing.T
	router http.Handler
	api    *api.API
	fake   *ledgerclient.Fake
	entity string
}

func newSettlementHarness(t *testing.T) settlementHarness {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := settlementHarness{t: t, fake: ledgerclient.NewFake(), entity: ids.New()}
	ledgerID := ids.New()
	h.fake.AddLedger(ledgerID, h.entity)
	for key, lines := range map[string][]ledgerclient.Line{
		"sale":     {{Account: "1200", Amount: decimal.RequireFromString("19.00")}, {Account: "2200", Credit: true, Amount: decimal.RequireFromString("19.00")}},
		"purchase": {{Account: "1400", Amount: decimal.RequireFromString("5.00")}, {Account: "2100", Credit: true, Amount: decimal.RequireFromString("5.00")}},
	} {
		if _, err := h.fake.Post(context.Background(), ledgerclient.Entry{LegalEntityID: h.entity, LedgerID: ledgerID, DocumentID: key, PostingDate: "2026-01-15",
			Currency: "EUR", IdempotencyKey: key, Lines: lines}); err != nil {
			t.Fatal(err)
		}
	}
	h.router, h.api = routerWith(store.New(app), nil, h.fake)
	config := `{"ledger_id":"` + ledgerID + `","settlement_account":"2900","accounts":{"VAT19|output":"2200","VAT19|input":"1400"}}`
	if rec := h.call(http.MethodPut, "/v1/tax/ledger-config", config); rec.Code != http.StatusOK {
		t.Fatalf("config: %d %s", rec.Code, rec.Body.String())
	}
	record := `{"transactions":[` +
		`{"id":"` + ids.New() + `","code":"VAT19","direction":"output","tax_date":"2026-01-15","amount":"19.00"},` +
		`{"id":"` + ids.New() + `","code":"VAT19","direction":"input","tax_date":"2026-01-15","amount":"5.00"}]}`
	if rec := h.call(http.MethodPost, "/v1/tax/transactions", record); rec.Code != http.StatusCreated {
		t.Fatalf("record: %d %s", rec.Code, rec.Body.String())
	}
	return h
}

func (h settlementHarness) call(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(tenant.HeaderTenant, testkit.TenantID(7))
	req.Header.Set(tenant.HeaderLegalEntity, h.entity)
	req.Header.Set(tenant.HeaderDuties, "tax.transaction.record,tax.settlement.run,tax.ledgerconfig.manage")
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func (h settlementHarness) settlementEntries() []ledgerclient.Stored {
	var out []ledgerclient.Stored
	for _, e := range h.fake.Entries() {
		if e.SourceType == "tax_settlement" {
			out = append(out, e)
		}
	}
	return out
}

const settleJanuary = `{"period_from":"2026-01-01","period_to":"2026-01-31"}`

func TestRunSettlement_PostsBalancedEntry(t *testing.T) {
	h := newSettlementHarness(t)
	if rec := h.call(http.MethodPost, "/v1/tax/settlements:run", settleJanuary); rec.Code != http.StatusCreated {
		t.Fatalf("settle: %d %s", rec.Code, rec.Body.String())
	}
	metrics := httptest.NewRecorder()
	h.api.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "tax_settlements_posted_total 1") {
		t.Fatalf("posted settlement not counted: %s", metrics.Body.String())
	}
	entries := h.settlementEntries()
	if len(entries) != 1 {
		t.Fatalf("got %d settlement entries, want 1", len(entries))
	}
	debit, credit := decimal.Zero, decimal.Zero
	byAccount := map[string]string{}
	for _, l := range entries[0].Lines {
		side := "D"
		if l.Credit {
			credit, side = credit.Add(l.Amount), "C"
		} else {
			debit = debit.Add(l.Amount)
		}
		byAccount[l.Account] = side + l.Amount.String()
	}
	if !debit.Equal(credit) || entries[0].PostingDate != "2026-01-31" {
		t.Fatalf("entry debit %s credit %s date %s", debit, credit, entries[0].PostingDate)
	}
	if byAccount["2200"] != "D19" || byAccount["1400"] != "C5" || byAccount["2900"] != "C14" {
		t.Fatalf("output minus input did not reach the settlement account: %v", byAccount)
	}
}

func TestRunSettlement_FailedPostStaysPendingAndIsReposted(t *testing.T) {
	h := newSettlementHarness(t)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	h.fake.FailPosts(errors.New("ledger down"))
	if rec := h.call(http.MethodPost, "/v1/tax/settlements:run", settleJanuary); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "COM-0502") {
		t.Fatalf("failed post: %d %s", rec.Code, rec.Body.String())
	}
	if out := logs.String(); !strings.Contains(out, "tax settlement post failed") || !strings.Contains(out, "runId=") || strings.Contains(out, "19.00") {
		t.Fatalf("failed post log %q", out)
	}
	h.fake.FailPosts(nil)
	if rec := h.call(http.MethodPost, "/v1/tax/settlements:run", settleJanuary); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "TAX-0009") {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(h.settlementEntries()); n != 1 {
		t.Fatalf("got %d settlement entries after the retry, want 1", n)
	}
}

func TestRunSettlement_ClosedPeriodRefusalIsSurfaced(t *testing.T) {
	h := newSettlementHarness(t)
	h.fake.RefuseClosedThrough("2026-01-31")
	rec := h.call(http.MethodPost, "/v1/tax/settlements:run", settleJanuary)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), ledgerclient.CodePeriodClosed) {
		t.Fatalf("closed period: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(h.settlementEntries()); n != 0 {
		t.Fatalf("closed period took %d settlement entries", n)
	}
}

func TestRunSettlement_StuckRunDoesNotBlockALaterPeriod(t *testing.T) {
	h := newSettlementHarness(t)
	h.fake.RefuseClosedThrough("2026-01-31")
	if rec := h.call(http.MethodPost, "/v1/tax/settlements:run", settleJanuary); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("january: %d %s", rec.Code, rec.Body.String())
	}
	record := `{"transactions":[{"id":"` + ids.New() + `","code":"VAT19","direction":"output","tax_date":"2026-02-10","amount":"10.00"}]}`
	if rec := h.call(http.MethodPost, "/v1/tax/transactions", record); rec.Code != http.StatusCreated {
		t.Fatalf("record: %d %s", rec.Code, rec.Body.String())
	}
	if rec := h.call(http.MethodPost, "/v1/tax/settlements:run", `{"period_from":"2026-02-01","period_to":"2026-02-28"}`); rec.Code != http.StatusCreated {
		t.Fatalf("february blocked by the stuck january run: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(h.settlementEntries()); n != 1 {
		t.Fatalf("got %d settlement entries, want only february", n)
	}
}

func TestCalculate_ReturnsPostingLines(t *testing.T) {
	type response struct {
		PostingLines []struct {
			Account      string `json:"account"`
			Credit       bool   `json:"credit"`
			Amount       string `json:"amount"`
			TaxCode      string `json:"tax_code"`
			Jurisdiction string `json:"jurisdiction"`
		} `json:"posting_lines"`
		Warnings []domain.Warning `json:"warnings"`
	}
	run := func(fs *fakeStore) response {
		r, _ := routerWith(fs, nil, nil)
		rec := do(r, http.MethodPost, calculatePath, workedBody, testkit.TenantID(1), duty)
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
		var out response
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	configured := run(&fakeStore{config: &store.LedgerConfig{Accounts: map[string]string{"VAT|output": "2200"}}})
	if len(configured.PostingLines) != 1 || configured.PostingLines[0].Account != "2200" || !configured.PostingLines[0].Credit ||
		configured.PostingLines[0].Amount != "19" || configured.PostingLines[0].TaxCode != "VAT" || configured.PostingLines[0].Jurisdiction != "DE" {
		t.Fatalf("posting lines %+v", configured.PostingLines)
	}
	unmapped := run(&fakeStore{config: &store.LedgerConfig{Accounts: map[string]string{"OTHER|output": "2200"}}})
	if len(unmapped.PostingLines) != 0 || len(unmapped.Warnings) != 1 || unmapped.Warnings[0].Code != domain.CodeNoPostingAccount {
		t.Fatalf("unmapped code: %+v", unmapped)
	}
	rec := do(mustRouter(&fakeStore{}), http.MethodPost, calculatePath, workedBody, testkit.TenantID(1), duty)
	if !strings.Contains(rec.Body.String(), `"posting_lines":[]`) {
		t.Fatalf("no ledger config: %s", rec.Body.String())
	}
}

func mustRouter(s api.Store) http.Handler {
	r, _ := routerWith(s, nil, nil)
	return r
}

const saftQuery = "?period_from=2026-01-01&period_to=2026-01-31&company_name=Acme&registration_number=DE1&country=DE&currency=EUR"

func saftCall(r http.Handler, entity, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/v1/tax/saft"+query, nil)
	req.Header.Set(tenant.HeaderTenant, testkit.TenantID(7))
	req.Header.Set(tenant.HeaderLegalEntity, entity)
	req.Header.Set(tenant.HeaderDuties, "tax.saft.export")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func postJournal(t *testing.T, fake *ledgerclient.Fake, entity, ledgerID, doc, date string, amount string) {
	t.Helper()
	v := decimal.RequireFromString(amount)
	e := ledgerclient.Entry{LegalEntityID: entity, LedgerID: ledgerID, DocumentID: doc, PostingDate: date, Currency: "EUR", IdempotencyKey: doc, SourceType: "ar_invoice",
		Lines: []ledgerclient.Line{{Account: "1200", Amount: v}, {Account: "8000", Credit: true, Amount: v}}}
	if _, err := fake.Post(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func TestSAFTExportEmitsTheLedgerJournalsOfTheEntityLedger(t *testing.T) {
	entity, other, ledgerID, foreignLedger := ids.New(), ids.New(), ids.New(), ids.New()
	fake := ledgerclient.NewFake()
	fake.AddLedger(ledgerID, entity)
	fake.AddLedger(foreignLedger, other)
	postJournal(t, fake, entity, ledgerID, "d1", "2026-01-10", "100.00")
	postJournal(t, fake, entity, ledgerID, "d2", "2026-01-20", "40.50")
	postJournal(t, fake, entity, ledgerID, "prior", "2025-12-20", "5.00")
	postJournal(t, fake, entity, ledgerID, "outside", "2026-02-03", "9.00")
	postJournal(t, fake, other, foreignLedger, "foreign", "2026-01-11", "7.00")
	r, _ := routerWith(&fakeStore{}, nil, fake)

	rec := saftCall(r, entity, saftQuery+"&ledger_id="+ledgerID)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"<NumberOfEntries>2</NumberOfEntries>", "<TotalDebit>140.5</TotalDebit>", "<TotalCredit>140.5</TotalCredit>", "<JournalID>ar_invoice</JournalID>", "<AccountID>8000</AccountID>", "<OpeningDebitBalance>5</OpeningDebitBalance>", "<ClosingDebitBalance>145.5</ClosingDebitBalance>", "<OpeningCreditBalance>5</OpeningCreditBalance>", "<ClosingCreditBalance>145.5</ClosingCreditBalance>"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, "<DebitAmount>9<") || strings.Contains(body, "<DebitAmount>7<") {
		t.Fatalf("lines outside the period or the entity leaked: %s", body)
	}
	if err := saft.CheckDocument([]byte(body)); err != nil {
		t.Fatal(err)
	}
	xmllint, err := exec.LookPath("xmllint")
	if err != nil {
		t.Skip("xmllint is not on PATH")
	}
	file := filepath.Join(t.TempDir(), "saft.xml")
	if err := os.WriteFile(file, rec.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(xmllint, "--noout", "--nonet", "--schema", "../../testdata/saft/saft-2.00-tax-extract.xsd", file).CombinedOutput(); err != nil {
		t.Fatalf("route output rejected by the schema: %v\n%s", err, out)
	}
}

func TestSAFTExportRefusesALedgerOfAnotherEntityAndBadInput(t *testing.T) {
	entity, other, foreignLedger, ledgerID := ids.New(), ids.New(), ids.New(), ids.New()
	fake := ledgerclient.NewFake()
	fake.AddLedger(foreignLedger, other)
	fake.AddLedger(ledgerID, entity)
	postJournal(t, fake, entity, ledgerID, "d1", "2026-01-10", "100.00")
	r, _ := routerWith(&fakeStore{}, nil, fake)
	for name, tc := range map[string]struct {
		query string
		want  int
	}{
		"ledger of another entity": {saftQuery + "&ledger_id=" + foreignLedger, http.StatusBadRequest},
		"unknown ledger":           {saftQuery + "&ledger_id=" + ids.New(), http.StatusBadRequest},
		"not a uuid":               {saftQuery + "&ledger_id=nope", http.StatusBadRequest},
		"partial month":            {strings.Replace(saftQuery, "2026-01-31", "2026-01-30", 1) + "&ledger_id=" + ledgerID, http.StatusBadRequest},
		"currency differs":         {strings.Replace(saftQuery, "EUR", "USD", 1) + "&ledger_id=" + ledgerID, http.StatusUnprocessableEntity},
	} {
		if rec := saftCall(r, entity, tc.query); rec.Code != tc.want {
			t.Errorf("%s: got %d %s", name, rec.Code, rec.Body.String())
		}
	}
	noLedger, _ := routerWith(&fakeStore{}, nil, nil)
	if rec := saftCall(noLedger, entity, saftQuery+"&ledger_id="+foreignLedger); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no ledger configured: got %d", rec.Code)
	}
}

type brokenLines struct{ *ledgerclient.Fake }

func (brokenLines) JournalLines(context.Context, ledgerclient.LineQuery) ([]ledgerclient.JournalLine, error) {
	return []ledgerclient.JournalLine{
		{EntryID: "e1", PostingDate: "2026-01-10", LineNumber: 1, Account: "1200", Amount: decimal.RequireFromString("10")},
		{EntryID: "e1", PostingDate: "2026-01-10", LineNumber: 2, Account: "8000", Credit: true, Amount: decimal.RequireFromString("9")},
	}, nil
}

type failingLines struct{ *ledgerclient.Fake }

func (failingLines) JournalLines(context.Context, ledgerclient.LineQuery) ([]ledgerclient.JournalLine, error) {
	return nil, errors.New("ledger down")
}

func TestSAFTExportRefusesAnUnbalancedLedgerAndSurfacesLedgerOutages(t *testing.T) {
	entity, ledgerID := ids.New(), ids.New()
	fake := ledgerclient.NewFake()
	fake.AddLedger(ledgerID, entity)
	query := saftQuery + "&ledger_id=" + ledgerID
	postJournal(t, fake, entity, ledgerID, "d1", "2026-01-10", "10.00")
	unbalanced, _ := routerWith(&fakeStore{}, nil, brokenLines{fake})
	rec := saftCall(unbalanced, entity, query)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "TAX-0012") || strings.Contains(rec.Body.String(), "<AuditFile") {
		t.Fatalf("unbalanced ledger: got %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "e1") || strings.Contains(rec.Body.String(), "1200") {
		t.Fatalf("problem body names ledger values: %s", rec.Body.String())
	}
	down, _ := routerWith(&fakeStore{}, nil, failingLines{fake})
	if rec := saftCall(down, entity, query); rec.Code != http.StatusBadGateway {
		t.Fatalf("ledger outage: got %d %s", rec.Code, rec.Body.String())
	}
}
