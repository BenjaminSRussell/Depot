package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/costing/internal/domain"
)

const reconcileAsOf = "2099-12-31"

func (f glFixture) stock(t *testing.T, key, le, product, site, qty, value string) {
	t.Helper()
	f.record(t, costRow(key, le, product, site, domain.KindPhysical, qty, value, "2026-01-01"))
}

func (f glFixture) postInventory(t *testing.T, amount string) {
	t.Helper()
	_, err := f.ledger.Post(f.ctx, ledgerclient.Entry{LegalEntityID: f.le, LedgerID: f.glID, DocumentID: "stock", PostingDate: "2026-01-01",
		Currency: "EUR", IdempotencyKey: "stock", Lines: []ledgerclient.Line{{Account: "1400", Amount: d(amount)}, {Account: "2100", Credit: true, Amount: d(amount)}}})
	if err != nil {
		t.Fatal(err)
	}
}

func (f glFixture) draft(t *testing.T, code, product, site, cost string) CostingVersion {
	t.Helper()
	v := mustVersion(t, f.s, f.ctx, CostingVersion{LegalEntityID: f.le, Code: code, Type: "STANDARD", Currency: "EUR", EffectiveFrom: "2026-01-01"})
	if _, err := f.s.ImportItemCosts(f.ctx, v.ID, []ItemCost{{ProductID: product, SiteID: site, UnitCost: cost}}); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.GetVersion(f.ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPostgres_ActivationPostsRevaluationVoucher(t *testing.T) {
	f := newGLFixture(t, 41)
	product, site := ids.New(), ids.New()
	f.stock(t, "stock-1", f.le, product, site, "100", "950.00")
	f.postInventory(t, "950.00")
	v := f.draft(t, "STD-GL", product, site, "10")

	res, err := f.s.ActivateVersion(f.ctx, v.ID, v.Version, f.ledger)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Plan.Total.Equal(d("50")) || res.VouchersPosted != 1 || res.VouchersPending != 0 {
		t.Fatalf("total %s posted %d pending %d, want 50.00 posted as one voucher", res.Plan.Total, res.VouchersPosted, res.VouchersPending)
	}
	if got := netOf(f, "1400"); !got.Equal(d("1000")) {
		t.Errorf("inventory account net %s, want 950 + 100 x (10 - 9.50) = 1000", got)
	}
	if got := netOf(f, "5000"); !got.Equal(d("-50")) {
		t.Errorf("adjustment account net %s, want -50", got)
	}
	rec, err := f.s.ReconcileInventory(f.ctx, f.ledger, f.le, reconcileAsOf)
	if err != nil || !rec.Difference.IsZero() || !rec.Ties {
		t.Fatalf("reconciliation %+v, %v; want the ledger and the inventory value to agree after activation", rec, err)
	}
}

func TestPostgres_ActivationReadsOnHandOfItsOwnEntityOnly(t *testing.T) {
	f := newGLFixture(t, 42)
	product, site := ids.New(), ids.New()
	f.stock(t, "own", f.le, product, site, "100", "950.00")
	f.stock(t, "other", ids.New(), product, site, "999", "9999.00")
	f.record(t, f.ppvRow("ppv:RECEIPT:"+f.le+":GR-9:1", "500", "5.00", "5.00", "2026-01-02"))
	v := f.draft(t, "STD-OWN", product, site, "10")

	prev, err := f.s.PreviewActivation(f.ctx, v.ID)
	if err != nil || len(prev.Plan.Lines) != 1 || !prev.Plan.Lines[0].OnHandQty.Equal(d("100")) || !prev.Plan.Total.Equal(d("50")) {
		t.Fatalf("preview %+v, %v; want 100 on hand and 50.00 from this entity's own rows only", prev.Plan, err)
	}
}

func TestPostgres_ActivationRefusesAnEntityWithoutAProfile(t *testing.T) {
	s, _ := setup(t)
	ctx := scopedCtx(testkit.TenantID(43))
	le, product, site := ids.New(), ids.New(), ids.New()
	if _, err := s.RecordCostTransactions(ctx, []CostTransaction{costRow("stock", le, product, site, domain.KindPhysical, "100", "950.00", "2026-01-01")}); err != nil {
		t.Fatal(err)
	}
	v := mustVersion(t, s, ctx, CostingVersion{LegalEntityID: le, Code: "STD-NP", Type: "STANDARD", Currency: "EUR", EffectiveFrom: "2026-01-01"})
	if _, err := s.ImportItemCosts(ctx, v.ID, []ItemCost{{ProductID: product, SiteID: site, UnitCost: "10"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateVersion(ctx, v.ID, 2, ledgerclient.NewFake()); problemCode(err) != CodeNoProfile {
		t.Fatalf("activation without a profile: %v, want %s", err, CodeNoProfile)
	}
	got, err := s.GetVersion(ctx, v.ID)
	recorded, rerr := s.CostTransactionRecorded(ctx, "reval:"+v.ID+":"+product+":"+site)
	if err != nil || rerr != nil || got.Status != "DRAFT" || recorded {
		t.Errorf("status %s revaluation recorded %v (%v, %v); want DRAFT and nothing written", got.Status, recorded, err, rerr)
	}
}

func TestPostgres_ActivationWithTheLedgerDownLeavesTheVoucherPendingUntilRetried(t *testing.T) {
	f := newGLFixture(t, 44)
	product, site := ids.New(), ids.New()
	f.stock(t, "stock-1", f.le, product, site, "100", "950.00")
	v := f.draft(t, "STD-DOWN", product, site, "10")
	f.ledger.FailPosts(errors.New("ledger down"))

	res, err := f.s.ActivateVersion(f.ctx, v.ID, v.Version, f.ledger)
	if err != nil || res.Version.Status != "ACTIVE" || res.VouchersPosted != 0 || res.VouchersPending != 1 {
		t.Fatalf("result %+v, %v; want an ACTIVE version with its voucher pending", res, err)
	}
	f.ledger.FailPosts(nil)
	f.dueNow(t)
	retry, err := f.s.PostCostVouchers(f.ctx, f.ledger, GLPostRequest{LegalEntityID: f.le})
	if err != nil || len(retry.Booked) != 1 || !strings.HasPrefix(retry.Booked[0].VoucherKey, "reval:"+v.ID) {
		t.Fatalf("retry %+v, %v; want the revaluation voucher booked", retry, err)
	}
	again, err := f.s.PostCostVouchers(f.ctx, f.ledger, GLPostRequest{LegalEntityID: f.le})
	if err != nil || len(again.Booked) != 0 || len(f.ledger.Entries()) != 1 {
		t.Errorf("second retry %+v entries %d, want nothing more booked", again, len(f.ledger.Entries()))
	}
}

func TestPostgres_ActivationWithNoRevaluationPostsNothing(t *testing.T) {
	f := newGLFixture(t, 45)
	product, site := ids.New(), ids.New()
	f.stock(t, "stock-1", f.le, product, site, "10", "100.00")
	v := f.draft(t, "STD-ZERO", product, site, "10")

	res, err := f.s.ActivateVersion(f.ctx, v.ID, v.Version, f.ledger)
	recorded, rerr := f.s.CostTransactionRecorded(f.ctx, "reval:"+v.ID+":"+product+":"+site)
	if err != nil || rerr != nil || res.VouchersPosted != 0 || res.VouchersPending != 0 || len(f.ledger.Entries()) != 0 || recorded {
		t.Errorf("result %+v entries %d revaluation row %v (%v, %v); want no voucher and no REVALUATION row", res, len(f.ledger.Entries()), recorded, err, rerr)
	}
}
