package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/openingimport"
	"github.com/BenjaminSRussell/depot/services/assets/internal/api"
	"github.com/BenjaminSRussell/depot/services/assets/internal/store"
)

type fakeOpening struct {
	imported []store.OpeningImport
	err      error
}

func (f *fakeOpening) ImportOpeningAssets(_ context.Context, in store.OpeningImport) (openingimport.Batch, bool, error) {
	if f.err != nil {
		return openingimport.Batch{}, false, f.err
	}
	f.imported = append(f.imported, in)
	return openingimport.Batch{ID: ids.New(), LegalEntityID: in.LegalEntityID, BatchKey: in.BatchKey, LineCount: len(in.Lines), Totals: in.Totals, ImportedAt: time.Now()}, true, nil
}

func (f *fakeOpening) ListOpeningBatches(context.Context, string, string, int, time.Time, string) ([]openingimport.Batch, error) {
	return []openingimport.Batch{}, nil
}

const openingDuty = "assets.opening-asset.import"

func openingServer(f *fakeOpening) http.Handler {
	r := httpx.NewRouter()
	api.New(newFake()).WithOpeningAssets(f).Register(r)
	var _ chi.Router = r
	return r
}

func openingBody(line, totals string) string {
	return `{"legalEntityId":"` + testLE + `","batchKey":"b1","openingDate":"2027-01-01","journal":"post","controlTotals":` + totals + `,"lines":[` + line + `]}`
}

const goodLine = `{"reference":"L1","assetNumber":"FA-1","name":"Press","classCode":"MACHINERY","bookCode":"IFRS","currency":"USD","method":"StraightLine","acquisitionDate":"2023-03-01",
	"usefulLifePeriods":60,"remainingLifePeriods":40,"acquisitionCost":"12000","accumulatedDepreciation":"4000","netBookValue":"8000","salvageValue":"0","depreciatedThrough":"2026-12"}`

const goodTotals = `{"cost.USD":"12000","accumulatedDepreciation.USD":"4000","nbv.USD":"8000"}`

func TestOpeningAssets_ImportsAndRecomputesTotals(t *testing.T) {
	f := &fakeOpening{}
	rec := call(openingServer(f), http.MethodPost, "/v1/assets/opening-assets", openingBody(goodLine, goodTotals), openingDuty, false)
	if rec.Code != http.StatusCreated || len(f.imported) != 1 || f.imported[0].Lines[0].NBV().String() != "8000" || f.imported[0].ContentHash == "" {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	bad := call(openingServer(f), http.MethodPost, "/v1/assets/opening-assets", openingBody(goodLine, `{"cost.USD":"12001","accumulatedDepreciation.USD":"4000","nbv.USD":"8000"}`), openingDuty, false)
	p := problem(t, bad)
	if bad.Code != http.StatusUnprocessableEntity || p.Code != api.CodeOpeningControlTotal || p.Errors["controlTotals.cost.USD"] == "" {
		t.Fatalf("mismatch: %d %+v", bad.Code, p)
	}
}

func TestOpeningAssets_RowErrorsAreStructured(t *testing.T) {
	f := &fakeOpening{}
	line := strings.Replace(strings.Replace(goodLine, `"netBookValue":"8000"`, `"netBookValue":"7000"`, 1), `"remainingLifePeriods":40`, `"remainingLifePeriods":70`, 1)
	rec := call(openingServer(f), http.MethodPost, "/v1/assets/opening-assets", openingBody(line, goodTotals), openingDuty, false)
	p := problem(t, rec)
	if rec.Code != http.StatusUnprocessableEntity || p.Code != api.CodeOpeningInvalid || len(f.imported) != 0 {
		t.Fatalf("invalid: %d %s", rec.Code, rec.Body)
	}
	fields := map[string]bool{}
	for _, e := range p.RowErrors {
		if e.Row != 1 || e.Code != httpx.RowInvalid {
			t.Fatalf("row error %+v", e)
		}
		fields[e.Field] = true
	}
	if !fields["netBookValue"] || !fields["remainingLifePeriods"] {
		t.Fatalf("row errors %+v", p.RowErrors)
	}
}

func TestOpeningAssets_TakenAndConflictAreRefused(t *testing.T) {
	f := &fakeOpening{err: &openingimport.TakenError{References: map[string]string{"L1": "earlier"}, Keys: map[string]bool{"FA-1/IFRS": true}}}
	rec := call(openingServer(f), http.MethodPost, "/v1/assets/opening-assets", openingBody(goodLine, goodTotals), openingDuty, false)
	p := problem(t, rec)
	if rec.Code != http.StatusUnprocessableEntity || p.Code != api.CodeOpeningReference || len(p.RowErrors) != 2 || p.RowErrors[0].TakenByBatch != "earlier" {
		t.Fatalf("taken: %d %s", rec.Code, rec.Body)
	}
	f.err = openingimport.ErrKeyConflict
	if rec := call(openingServer(f), http.MethodPost, "/v1/assets/opening-assets", openingBody(goodLine, goodTotals), openingDuty, false); rec.Code != http.StatusConflict {
		t.Fatalf("conflict: %d", rec.Code)
	}
}

func TestOpeningAssets_EnvelopeAndDuties(t *testing.T) {
	h := openingServer(&fakeOpening{})
	if rec := call(h, http.MethodPost, "/v1/assets/opening-assets", openingBody(goodLine, goodTotals), "assets.asset.view", false); rec.Code != http.StatusForbidden {
		t.Fatalf("without the duty: %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/assets/opening-assets", `{"legalEntityId":"`+testLE+`","batchKey":"","journal":"x","lines":[],"controlTotals":{}}`, openingDuty, false); rec.Code != http.StatusBadRequest {
		t.Fatalf("envelope: %d", rec.Code)
	} else {
		var p httpx.Problem
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		for _, k := range []string{"batchKey", "lines", "controlTotals", "openingDate", "journal"} {
			if p.Errors[k] == "" {
				t.Fatalf("missing %s in %+v", k, p.Errors)
			}
		}
	}
	if rec := call(h, http.MethodGet, "/v1/assets/opening-assets?legal_entity="+testLE, "", "assets.opening-asset.read", false); rec.Code != http.StatusOK {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
}

func TestOpeningAssets_ABookWithNoLifeLeftStandsAtSalvageAndDeclineFieldsAreChecked(t *testing.T) {
	h := openingServer(&fakeOpening{})
	post := func(line string) (int, string) {
		rec := call(h, http.MethodPost, "/v1/assets/opening-assets", openingBody(line, goodTotals), openingDuty, false)
		return rec.Code, rec.Body.String()
	}
	if code, body := post(strings.Replace(goodLine, `"remainingLifePeriods":40`, `"remainingLifePeriods":0`, 1)); code != http.StatusUnprocessableEntity || !strings.Contains(body, "remainingLifePeriods") {
		t.Fatalf("no life left: %d %s", code, body)
	}
	if code, body := post(strings.Replace(goodLine, `"depreciatedThrough"`, `"declineFactor":"1.5","depreciatedThrough"`, 1)); code != http.StatusUnprocessableEntity || !strings.Contains(body, "declineFactor") {
		t.Fatalf("a factor on a straight-line book: %d %s", code, body)
	}
	db := strings.Replace(strings.Replace(goodLine, `"StraightLine"`, `"DecliningBalance"`, 1), `"depreciatedThrough":"2026-12"`, `"depreciatedThrough":"2026","declineFactor":"1.5","switchToStraightLine":true`, 1)
	db = strings.Replace(db, `"usefulLifePeriods":60,"remainingLifePeriods":40`, `"usefulLifePeriods":10,"remainingLifePeriods":8`, 1)
	if code, body := post(db); code != http.StatusCreated {
		t.Fatalf("a 150 percent book with a switch: %d %s", code, body)
	}
	if code, _ := post(strings.Replace(db, `"1.5"`, `"4"`, 1)); code != http.StatusUnprocessableEntity {
		t.Fatalf("factor 4: %d", code)
	}
}
