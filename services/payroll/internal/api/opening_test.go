package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/openingimport"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/payroll/internal/store"
)

type fakeOpening struct {
	imported []store.OpeningImport
	err      error
}

func (f *fakeOpening) ImportOpeningYTD(_ context.Context, in store.OpeningImport) (openingimport.Batch, bool, error) {
	if f.err != nil {
		return openingimport.Batch{}, false, f.err
	}
	f.imported = append(f.imported, in)
	return openingimport.Batch{ID: ids.New(), LegalEntityID: in.LegalEntityID, BatchKey: in.BatchKey, LineCount: len(in.Lines), Totals: in.Totals, ImportedAt: time.Now()}, true, nil
}

func (f *fakeOpening) ListOpeningBatches(context.Context, string, string, int, time.Time, string) ([]openingimport.Batch, error) {
	return []openingimport.Batch{}, nil
}

func openingRouter(f *fakeOpening) http.Handler {
	r := httpx.NewRouter()
	New(nil).WithOpeningYTD(f).Register(r)
	return r
}

func ytdBody(lines, totals string) string {
	return `{"legalEntityId":"` + legalEntity + `","batchKey":"b1","controlTotals":` + totals + `,"lines":[` + lines + `]}`
}

func ytdLine(ref, element, amount string) string {
	return `{"reference":"` + ref + `","workerId":"` + workerA + `","taxYear":"2026","element":"` + element + `","currency":"USD","amount":"` + amount + `"}`
}

var goodYear = strings.Join([]string{ytdLine("PY-1:GROSS", "GROSS", "1000"), ytdLine("PY-1:TAX", "TAX", "100"), ytdLine("PY-1:DEDUCTIONS", "DEDUCTIONS", "0"), ytdLine("PY-1:NET", "NET", "900")}, ",")

const goodTotals = `{"gross.USD":"1000","tax.USD":"100","deductions.USD":"0","net.USD":"900"}`

func TestOpeningYTD_ImportsRecomputesTotalsAndChecksTheNet(t *testing.T) {
	f := &fakeOpening{}
	h := openingRouter(f)
	if rec := postYTD(t, h, "/v1/payroll/opening-ytd", ytdBody(goodYear, goodTotals), "payroll.opening-ytd.import"); rec.Code != http.StatusCreated || len(f.imported) != 1 || len(f.imported[0].Lines) != 4 {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	rec := postYTD(t, h, "/v1/payroll/opening-ytd", ytdBody(goodYear, `{"gross.USD":"999","tax.USD":"100","deductions.USD":"0","net.USD":"900"}`), "payroll.opening-ytd.import")
	var p httpx.Problem
	decode(t, rec, &p)
	if rec.Code != http.StatusUnprocessableEntity || p.Code != CodeOpeningControlTotal || p.Errors["controlTotals.gross.USD"] == "" {
		t.Fatalf("mismatch: %d %s", rec.Code, rec.Body)
	}
	badNet := strings.Replace(goodYear, `"amount":"900"`, `"amount":"901"`, 1)
	rec = postYTD(t, h, "/v1/payroll/opening-ytd", ytdBody(badNet, `{"gross.USD":"1000","tax.USD":"100","deductions.USD":"0","net.USD":"901"}`), "payroll.opening-ytd.import")
	decode(t, rec, &p)
	if rec.Code != http.StatusUnprocessableEntity || p.Code != CodeOpeningInvalid || len(p.RowErrors) != 1 || p.RowErrors[0].Row != 4 || p.RowErrors[0].Field != "amount" {
		t.Fatalf("net rule: %d %s", rec.Code, rec.Body)
	}
}

func TestOpeningYTD_RowErrorsRefusalsAndDuties(t *testing.T) {
	f := &fakeOpening{}
	h := openingRouter(f)
	duty := "payroll.opening-ytd.import"
	line := `{"reference":"X","workerId":"nope","taxYear":"26","element":"BONUS","currency":"USD","amount":"1.234"}`
	rec := postYTD(t, h, "/v1/payroll/opening-ytd", ytdBody(line, `{"gross.USD":"1"}`), duty)
	var p httpx.Problem
	decode(t, rec, &p)
	fields := map[string]bool{}
	for _, e := range p.RowErrors {
		fields[e.Field] = true
	}
	if rec.Code != http.StatusUnprocessableEntity || !fields["workerId"] || !fields["taxYear"] || !fields["element"] || len(f.imported) != 0 {
		t.Fatalf("invalid: %d %s", rec.Code, rec.Body)
	}
	dup := ytdLine("A", "GROSS", "1") + "," + ytdLine("B", "GROSS", "1")
	if rec := postYTD(t, h, "/v1/payroll/opening-ytd", ytdBody(dup, `{"gross.USD":"2"}`), duty); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate element: %d %s", rec.Code, rec.Body)
	}
	f.err = &openingimport.TakenError{References: map[string]string{"PY-1:GROSS": "earlier"}, Keys: map[string]bool{workerA + "/2026/GROSS": true}}
	rec = postYTD(t, h, "/v1/payroll/opening-ytd", ytdBody(goodYear, goodTotals), duty)
	decode(t, rec, &p)
	if rec.Code != http.StatusUnprocessableEntity || p.Code != CodeOpeningReference || len(p.RowErrors) != 2 || p.RowErrors[0].TakenByBatch != "earlier" {
		t.Fatalf("taken: %d %s", rec.Code, rec.Body)
	}
	f.err = openingimport.ErrKeyConflict
	if rec := postYTD(t, h, "/v1/payroll/opening-ytd", ytdBody(goodYear, goodTotals), duty); rec.Code != http.StatusConflict {
		t.Fatalf("conflict: %d", rec.Code)
	}
	if rec := postYTD(t, h, "/v1/payroll/opening-ytd", ytdBody(goodYear, goodTotals), "payroll.run.read"); rec.Code != http.StatusForbidden {
		t.Fatalf("without the duty: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/payroll/opening-ytd?legal_entity="+legalEntity, nil)
	req.Header.Set(tenant.HeaderTenant, testTenant)
	req.Header.Set(tenant.HeaderLegalEntity, legalEntity)
	req.Header.Set(tenant.HeaderDuties, "payroll.opening-ytd.read")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("read: %d %s", rec2.Code, rec2.Body)
	}
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("not JSON (%d): %s", rec.Code, rec.Body.String())
	}
}

// postYTD posts as a caller scoped to the legal entity.
func postYTD(t *testing.T, h http.Handler, path, body, duties string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, testTenant)
	req.Header.Set(tenant.HeaderLegalEntity, legalEntity)
	req.Header.Set(tenant.HeaderDuties, duties)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
