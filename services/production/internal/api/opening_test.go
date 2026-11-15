package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/openingimport"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

type fakeOpening struct {
	imported []store.OpeningImport
	err      error
}

func (f *fakeOpening) ImportOpeningWIP(_ context.Context, in store.OpeningImport) (openingimport.Batch, bool, error) {
	if f.err != nil {
		return openingimport.Batch{}, false, f.err
	}
	f.imported = append(f.imported, in)
	return openingimport.Batch{ID: ids.New(), LegalEntityID: in.LegalEntityID, BatchKey: in.BatchKey, LineCount: len(in.Lines), Totals: in.Totals, ImportedAt: time.Now()}, true, nil
}

func (f *fakeOpening) ListOpeningBatches(context.Context, string, string, int, time.Time, string) ([]openingimport.Batch, error) {
	return []openingimport.Batch{}, nil
}

func openingHandler(f *fakeOpening) http.Handler {
	r := httpx.NewRouter()
	New(&fakeStore{}, Config{}).WithOpeningWIP(f).Register(r)
	return r
}

func wipBody(line, totals string) string {
	return `{"legalEntityId":"` + entityID + `","batchKey":"b1","openingDate":"2027-01-01","controlTotals":` + totals + `,"lines":[` + line + `]}`
}

const wipLine = `{"reference":"W1","orderNumber":"LEG-1","productId":"11111111-1111-4111-8111-111111111111","siteId":"22222222-2222-4222-8222-222222222222","warehouseId":"33333333-3333-4333-8333-333333333333",
	"currency":"USD","quantityOpen":"10","deliveryDate":"2027-02-01","material":"100.50","labor":"20","overhead":"5","wipValue":"125.50"}`

const wipTotals = `{"material.USD":"100.5","labor.USD":"20","overhead.USD":"5","wipValue.USD":"125.5"}`

func TestOpeningWIP_ImportsAndRecomputesTotals(t *testing.T) {
	f := &fakeOpening{}
	h := openingHandler(f)
	rec := do(h, call{method: "POST", path: "/v1/production/opening-wip", body: wipBody(wipLine, wipTotals), duties: []string{"production.opening-wip.import"}})
	if rec.Code != http.StatusCreated || len(f.imported) != 1 || f.imported[0].Lines[0].WIP().String() != "125.5" {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	bad := do(h, call{method: "POST", path: "/v1/production/opening-wip", body: wipBody(wipLine, `{"material.USD":"1","labor.USD":"20","overhead.USD":"5","wipValue.USD":"125.5"}`), duties: []string{"production.opening-wip.import"}})
	if p := problemOf(t, bad); bad.Code != http.StatusUnprocessableEntity || p.Code != CodeOpeningControlTotal || p.Errors["controlTotals.material.USD"] == "" {
		t.Fatalf("mismatch: %d %+v", bad.Code, p)
	}
}

func TestOpeningWIP_RowErrorsAndRefusals(t *testing.T) {
	f := &fakeOpening{}
	h := openingHandler(f)
	duty := []string{"production.opening-wip.import"}
	line := strings.Replace(strings.Replace(wipLine, `"wipValue":"125.50"`, `"wipValue":"1"`, 1), `"quantityOpen":"10"`, `"quantityOpen":"0"`, 1)
	rec := do(h, call{method: "POST", path: "/v1/production/opening-wip", body: wipBody(line, wipTotals), duties: duty})
	p := problemOf(t, rec)
	fields := map[string]bool{}
	for _, e := range p.RowErrors {
		fields[e.Field] = true
	}
	if rec.Code != http.StatusUnprocessableEntity || p.Code != CodeOpeningInvalid || !fields["wipValue"] || !fields["quantityOpen"] || len(f.imported) != 0 {
		t.Fatalf("invalid: %d %s", rec.Code, rec.Body)
	}
	f.err = &store.OpeningProfileError{Detail: "no posting profile resolves for site x"}
	if rec := do(h, call{method: "POST", path: "/v1/production/opening-wip", body: wipBody(wipLine, wipTotals), duties: duty}); rec.Code != http.StatusUnprocessableEntity || problemOf(t, rec).Code != CodeOpeningProfile {
		t.Fatalf("profile: %d %s", rec.Code, rec.Body)
	}
	f.err = &openingimport.TakenError{References: map[string]string{"W1": "earlier"}, Keys: map[string]bool{"LEG-1": true}}
	rec = do(h, call{method: "POST", path: "/v1/production/opening-wip", body: wipBody(wipLine, wipTotals), duties: duty})
	if p := problemOf(t, rec); rec.Code != http.StatusUnprocessableEntity || p.Code != CodeOpeningReference || len(p.RowErrors) != 2 {
		t.Fatalf("taken: %d %s", rec.Code, rec.Body)
	}
	f.err = openingimport.ErrKeyConflict
	if rec := do(h, call{method: "POST", path: "/v1/production/opening-wip", body: wipBody(wipLine, wipTotals), duties: duty}); rec.Code != http.StatusConflict {
		t.Fatalf("conflict: %d", rec.Code)
	}
	if rec := do(h, call{method: "POST", path: "/v1/production/opening-wip", body: wipBody(wipLine, wipTotals), duties: []string{"production.order.read"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("without the duty: %d", rec.Code)
	}
	if rec := do(h, call{method: "GET", path: "/v1/production/opening-wip?legal_entity=" + entityID, duties: []string{"production.opening-wip.read"}}); rec.Code != http.StatusOK {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
}
