package reconcile

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/search/internal/index"
	"github.com/BenjaminSRussell/depot/services/search/internal/store"
)

const (
	tenantID = "00000000-0000-0000-0000-0000000000f1"
	entityA  = "00000000-0000-0000-0000-0000000000a1"
	entityB  = "00000000-0000-0000-0000-0000000000b1"
)

// fakeIndex answers Counts as row-level security would: tenant-wide rows always, scoped rows of the principal's entity.
type fakeIndex struct {
	rows       []store.Count
	principals []tenant.Principal
	err        error
}

func (f *fakeIndex) Counts(ctx context.Context) ([]store.Count, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return nil, errors.New("no principal")
	}
	f.principals = append(f.principals, p)
	if f.err != nil {
		return nil, f.err
	}
	var out []store.Count
	for _, r := range f.rows {
		if !store.LegalEntityScoped(r.Type) || r.LegalEntityID == p.LegalEntityID {
			out = append(out, r)
		}
	}
	return out, nil
}

func TestRunComparesEachTypeAndLegalEntityTheReportCounts(t *testing.T) {
	idx := &fakeIndex{rows: []store.Count{
		{Type: store.TypeParty, N: 40},
		{Type: store.TypeWorker, N: 7},
		{Type: store.TypePurchaseOrder, LegalEntityID: entityA, N: 12},
		{Type: store.TypePurchaseOrder, LegalEntityID: entityB, N: 3},
		{Type: store.TypeVendorInvoice, LegalEntityID: entityA, N: 2},
	}}
	src := Source{TenantID: tenantID, Counts: []store.Count{
		{Type: store.TypeParty, N: 40},
		{Type: store.TypePurchaseOrder, LegalEntityID: entityA, N: 12},
		{Type: store.TypePurchaseOrder, LegalEntityID: entityB, N: 5},
		// The source has vendor invoices in entity B that never reached the index (a topic no connector relays, say).
		{Type: store.TypeVendorInvoice, LegalEntityID: entityB, N: 4},
	}}
	res, err := Run(context.Background(), idx, src)
	if err != nil {
		t.Fatal(err)
	}
	want := []Diff{
		{Type: store.TypePurchaseOrder, LegalEntityID: entityB, Index: 3, Source: 5},
		{Type: store.TypeVendorInvoice, LegalEntityID: entityB, Index: 0, Source: 4},
	}
	if res.TenantID != tenantID || res.Compared != 4 || !reflect.DeepEqual(res.Diffs, want) {
		t.Fatalf("result = %+v, want 4 compared and diffs %+v", res, want)
	}
	// One pass for the tenant-wide types and one per entity, each as the consumer's service principal of that entity alone.
	var scopes []string
	for _, p := range idx.principals {
		if p.TenantID != tenantID || p.UserID != tenant.ServiceActor(index.ConsumerName) {
			t.Fatalf("counted as %+v, want the %s service principal of the tenant", p, index.ConsumerName)
		}
		scopes = append(scopes, p.LegalEntityID)
	}
	if !reflect.DeepEqual(scopes, []string{"", entityA, entityB}) {
		t.Fatalf("legal entity scopes = %q", scopes)
	}
}

func TestRunReportsNoDiffWhenTheIndexMatches(t *testing.T) {
	idx := &fakeIndex{rows: []store.Count{{Type: store.TypeProduct, N: 9}, {Type: store.TypeReceipt, LegalEntityID: entityA, N: 1}}}
	res, err := Run(context.Background(), idx, Source{TenantID: tenantID, Counts: []store.Count{
		{Type: store.TypeProduct, N: 9}, {Type: store.TypeReceipt, LegalEntityID: entityA, N: 1}}})
	if err != nil || res.Compared != 2 || len(res.Diffs) != 0 {
		t.Fatalf("Run = %+v, %v; want 2 compared, no diff", res, err)
	}
}

func TestRunFailsWhenTheIndexCannotBeCounted(t *testing.T) {
	idx := &fakeIndex{err: errors.New("down")}
	if _, err := Run(context.Background(), idx, Source{TenantID: tenantID, Counts: []store.Count{{Type: store.TypeParty, N: 1}}}); err == nil {
		t.Fatal("Run succeeded although the index could not be counted")
	}
}

func TestReadSourceChecksTheReport(t *testing.T) {
	src, err := ReadSource(strings.NewReader(`{"tenantId": "` + tenantID + `", "counts": [
		{"type": "party", "count": 3},
		{"type": "sales_order", "legalEntityId": "00000000-0000-0000-0000-0000000000A1", "count": 2}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if src.TenantID != tenantID || len(src.Counts) != 2 || src.Counts[1].LegalEntityID != entityA {
		t.Fatalf("source = %+v", src)
	}
	for name, body := range map[string]string{
		"tenant":         `{"tenantId": "x", "counts": []}`,
		"unknown type":   `{"tenantId": "` + tenantID + `", "counts": [{"type": "invoice", "count": 1}]}`,
		"scoped no le":   `{"tenantId": "` + tenantID + `", "counts": [{"type": "receipt", "count": 1}]}`,
		"tenant-wide le": `{"tenantId": "` + tenantID + `", "counts": [{"type": "worker", "legalEntityId": "` + entityA + `", "count": 1}]}`,
		"negative":       `{"tenantId": "` + tenantID + `", "counts": [{"type": "party", "count": -1}]}`,
		"twice":          `{"tenantId": "` + tenantID + `", "counts": [{"type": "party", "count": 1}, {"type": "party", "count": 2}]}`,
		"unknown field":  `{"tenantId": "` + tenantID + `", "counts": [], "extra": 1}`,
	} {
		if _, err := ReadSource(strings.NewReader(body)); err == nil {
			t.Errorf("%s: ReadSource accepted %s", name, body)
		}
	}
}
