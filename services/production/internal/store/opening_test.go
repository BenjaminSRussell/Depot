package store

import (
	"errors"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/openingimport"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

func openingWIPLines(site string) []OpeningLine {
	product, wh := ids.New(), ids.New()
	mk := func(ref, order, qty, material, labor, overhead string) OpeningLine {
		return OpeningLine{Reference: ref, OrderNumber: order, ProductID: product, SiteID: site, WarehouseID: wh, Currency: "USD", QuantityOpen: d(qty), DeliveryDate: "2027-02-01",
			Material: d(material), Labor: d(labor), Overhead: d(overhead)}
	}
	return []OpeningLine{mk("W1", "LEG-P1", "10", "100.50", "20", "5"), mk("W2", "LEG-P2", "4", "7", "0", "0")}
}

func TestOpeningWIP_ImportIsIdempotentBooksTheJournalAndReconciles(t *testing.T) {
	pool, s := migrated(t)
	s.WithOpeningClearingAccount("3900")
	ctx := tenantCtx(testkit.TenantID(9200))
	le, site, ledgerID, fake := ids.New(), ids.New(), ids.New(), ledgerclient.NewFake()
	in := OpeningImport{LegalEntityID: le, BatchKey: "b1", ContentHash: "h1", ImportedBy: "loader", OpeningDate: "2027-01-01", Journal: OpeningJournalPost,
		Totals: map[string]string{"material.USD": "107.5", "labor.USD": "20", "overhead.USD": "5", "wipValue.USD": "132.5"}, Lines: openingWIPLines(site)}
	// Without a posting profile the WIP cannot be booked and nothing is stored.
	var noProfile *OpeningProfileError
	if _, _, err := s.ImportOpeningWIP(ctx, in); !errors.As(err, &noProfile) {
		t.Fatalf("no profile: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from production_order where legal_entity_id = $1::uuid`, le).Scan(&n); err != nil || n != 0 {
		t.Fatalf("stored without a profile: %d %v", n, err)
	}
	if _, _, err := s.SavePostingProfile(ctx, ProfileSave{LegalEntityID: le, Profile: testProfile(domain.AllKey, domain.AllKey, "1450"), Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	got, created, err := s.ImportOpeningWIP(ctx, in)
	if err != nil || !created {
		t.Fatalf("import: %v %v", err, created)
	}
	if got.LineCount != 2 || got.Totals["wipValue.USD"] != "132.5" || got.Totals["material.USD"] != "107.5" {
		t.Fatalf("batch: %+v", got)
	}
	if again, created, err := s.ImportOpeningWIP(ctx, in); err != nil || created || again.ID != got.ID {
		t.Fatalf("replay: %v %v", err, created)
	}
	changed := in
	changed.ContentHash = "other"
	if _, _, err := s.ImportOpeningWIP(ctx, changed); !errors.Is(err, openingimport.ErrKeyConflict) {
		t.Fatalf("same key other content: %v", err)
	}
	other := in
	other.BatchKey, other.ContentHash = "b2", "h2"
	var taken *openingimport.TakenError
	if _, _, err := s.ImportOpeningWIP(ctx, other); !errors.As(err, &taken) || taken.References["W1"] != "b1" || !taken.Keys["LEG-P1"] {
		t.Fatalf("taken: %v", err)
	}
	if list, err := s.ListOpeningBatches(ctx, le, "", 10, time.Time{}, ""); err != nil || len(list) != 1 || list[0].Totals["labor.USD"] != "20" {
		t.Fatalf("list: %+v %v", list, err)
	}
	// The orders are Started with their open quantity and carry their WIP in the subledger: 132.50 on the WIP account.
	var status, qty string
	if err := pool.QueryRow(ctx, `select status, quantity::text from production_order where order_number = 'LEG-P1'`).Scan(&status, &qty); err != nil || status != "Started" || qty != "10.000000" {
		t.Fatalf("order: %s %s %v", status, qty, err)
	}
	// Booking it posts one entry per order journal; the GL WIP account matches the subledger.
	res, err := s.PostToGL(ctx, fake, GLPostRequest{LegalEntityID: le, LedgerID: ledgerID, OpeningOnly: true})
	if err != nil || len(res.Failed) != 0 || len(res.Booked) != 2 {
		t.Fatalf("post: %+v %v", res, err)
	}
	recon, err := s.ReconcileWIP(ctx, fake, GLReconcileRequest{LegalEntityID: le, LedgerID: ledgerID, Currency: "USD"})
	if err != nil || !recon.Reconciled || len(recon.Accounts) != 1 || !recon.Accounts[0].GL.Equal(d("132.5")) {
		t.Fatalf("reconcile: %+v %v", recon, err)
	}
	for acct, want := range map[string]string{acctWIP: "132.5", "3900": "-132.5", acctInv: "0", acctLabor: "0", acctOverhead: "0"} {
		if got := glNet(t, ctx, fake, ledgerID, acct); !got.Equal(d(want)) {
			t.Fatalf("GL %s = %s, want %s", acct, got, want)
		}
	}
	// The WIP balance report lists both orders with their opening WIP.
	rep, err := s.WIPReport(ctx, WIPFilter{LegalEntityID: le})
	if err != nil || len(rep.Items) != 2 || len(rep.Totals) != 1 || !rep.Totals[0].Balance.Equal(d("132.5")) || rep.Totals[0].Orders != 2 {
		t.Fatalf("wip report: %+v %v", rep, err)
	}
	// A second booking has nothing left.
	if res, err := s.PostToGL(ctx, fake, GLPostRequest{LegalEntityID: le, LedgerID: ledgerID}); err != nil || len(res.Booked) != 0 {
		t.Fatalf("second post: %+v %v", res, err)
	}
}

// linked marks the rows booked with no entry (the ledger already holds the WIP); the opening-only booking leaves ordinary postings alone; no
// clearing account refuses the import.
func TestOpeningWIP_LinkedBooksNothingAndTheClearingAccountIsRequired(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9201))
	le, site, ledgerID, fake := ids.New(), ids.New(), ids.New(), ledgerclient.NewFake()
	if _, _, err := s.SavePostingProfile(ctx, ProfileSave{LegalEntityID: le, Profile: testProfile(domain.AllKey, domain.AllKey, "1450"), Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	in := OpeningImport{LegalEntityID: le, BatchKey: "b1", ContentHash: "h1", ImportedBy: "loader", OpeningDate: "2027-01-01", Journal: OpeningJournalLinked,
		Totals: map[string]string{"material.USD": "107.5", "labor.USD": "20", "overhead.USD": "5", "wipValue.USD": "132.5"}, Lines: openingWIPLines(site)}
	var refused *OpeningProfileError
	if _, _, err := s.ImportOpeningWIP(ctx, in); !errors.As(err, &refused) {
		t.Fatalf("without a clearing account: %v", err)
	}
	s.WithOpeningClearingAccount("3900")
	if _, created, err := s.ImportOpeningWIP(ctx, in); err != nil || !created {
		t.Fatalf("import: %v %v", err, created)
	}
	res, err := s.PostToGL(ctx, fake, GLPostRequest{LegalEntityID: le, LedgerID: ledgerID, OpeningOnly: true})
	if err != nil || len(res.Booked) != 0 || len(res.Failed) != 0 {
		t.Fatalf("linked must book nothing: %+v %v", res, err)
	}
	rep, err := s.WIPReport(ctx, WIPFilter{LegalEntityID: le})
	if err != nil || len(rep.Totals) != 1 || !rep.Totals[0].Balance.Equal(d("132.5")) {
		t.Fatalf("the subledger still carries the WIP: %+v %v", rep, err)
	}
}
