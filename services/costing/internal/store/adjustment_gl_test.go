package store

import (
	"errors"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/costing/internal/domain"
)

func TestPostgres_AdjustmentPostsVoucherEqualToTheDeltaAndTiesToTheLedger(t *testing.T) {
	f := newGLFixture(t, 51)
	product, site := ids.New(), ids.New()
	f.stock(t, "stock-1", f.le, product, site, "100", "950.00")
	f.postInventory(t, "950.00")

	up, _, err := f.s.CreateAdjustment(f.ctx, onHand(f.le, product, site, domain.ModeAmount, "50"), "gl-up", hashOf("up"), f.ledger)
	if err != nil || up.Voucher != voucherPosted || up.Delta != "50.00" {
		t.Fatalf("adjustment %+v, %v; want a posted voucher for the 50.00 delta", up, err)
	}
	if got := netOf(f, "1400"); !got.Equal(d("1000")) {
		t.Errorf("inventory account net %s, want 950 + 50", got)
	}
	if got := netOf(f, "5000"); !got.Equal(d("-50")) {
		t.Errorf("adjustment account net %s, want -50", got)
	}
	down, _, err := f.s.CreateAdjustment(f.ctx, onHand(f.le, product, site, domain.ModeAmount, "-20"), "gl-down", hashOf("down"), f.ledger)
	if err != nil || down.Voucher != voucherPosted {
		t.Fatalf("write-down %+v, %v", down, err)
	}
	if got := netOf(f, "1400"); !got.Equal(d("980")) {
		t.Errorf("inventory account net %s after the write-down, want 980", got)
	}
	rec, err := f.s.ReconcileInventory(f.ctx, f.ledger, f.le, reconcileAsOf)
	if err != nil || !rec.Difference.IsZero() || !rec.Ties {
		t.Fatalf("reconciliation %+v, %v; want the ledger and the inventory value to agree after the adjustments", rec, err)
	}
}

func TestPostgres_AdjustmentReplayPostsNothingTwice(t *testing.T) {
	f := newGLFixture(t, 52)
	product, site := ids.New(), ids.New()
	f.stock(t, "stock-1", f.le, product, site, "100", "950.00")
	in := onHand(f.le, product, site, domain.ModeAmount, "50")

	first, created, err := f.s.CreateAdjustment(f.ctx, in, "gl-1", hashOf("1"), f.ledger)
	if err != nil || !created {
		t.Fatalf("first %+v created %v, %v", first, created, err)
	}
	again, created, err := f.s.CreateAdjustment(f.ctx, in, "gl-1", hashOf("1"), f.ledger)
	if err != nil || created || again.ID != first.ID || len(f.ledger.Entries()) != 1 {
		t.Errorf("replay %+v created %v entries %d (%v); want the stored adjustment and one ledger entry", again, created, len(f.ledger.Entries()), err)
	}
}

func TestPostgres_AdjustmentRefusesAnEntityWithoutAProfile(t *testing.T) {
	s, _ := setup(t)
	ctx := scopedCtx(testkit.TenantID(53))
	le, product, site := ids.New(), ids.New(), ids.New()
	if _, err := s.RecordCostTransactions(ctx, []CostTransaction{costRow("stock", le, product, site, domain.KindPhysical, "100", "950.00", "2026-01-01")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateAdjustment(ctx, onHand(le, product, site, domain.ModeAmount, "50"), "np-1", hashOf("np"), ledgerclient.NewFake()); problemCode(err) != CodeNoProfile {
		t.Fatalf("adjustment without a profile: %v, want %s", err, CodeNoProfile)
	}
	page, err := s.ListCostTransactions(ctx, CostTxnFilter{LegalEntityID: le, Kind: domain.KindManualAdj}, "", 10)
	adjs, aerr := s.ListAdjustments(ctx, AdjustmentFilter{LegalEntityID: le}, "", 10)
	if err != nil || aerr != nil || len(page.Items) != 0 || len(adjs.Items) != 0 {
		t.Errorf("MANUAL_ADJ rows %d adjustments %d (%v, %v); want nothing written", len(page.Items), len(adjs.Items), err, aerr)
	}
}

func TestPostgres_AdjustmentWithTheLedgerDownStaysPendingUntilRetried(t *testing.T) {
	f := newGLFixture(t, 54)
	product, site := ids.New(), ids.New()
	f.stock(t, "stock-1", f.le, product, site, "100", "950.00")
	f.ledger.FailPosts(errors.New("ledger down"))

	adj, created, err := f.s.CreateAdjustment(f.ctx, onHand(f.le, product, site, domain.ModeAmount, "50"), "down-1", hashOf("d"), f.ledger)
	if err != nil || !created || adj.Voucher != voucherPending || len(f.ledger.Entries()) != 0 {
		t.Fatalf("adjustment %+v created %v entries %d (%v); want it stored with its voucher PENDING", adj, created, len(f.ledger.Entries()), err)
	}
	f.ledger.FailPosts(nil)
	f.dueNow(t)
	retry, err := f.s.PostCostVouchers(f.ctx, f.ledger, GLPostRequest{LegalEntityID: f.le})
	if err != nil || len(retry.Booked) != 1 || retry.Booked[0].VoucherKey != adjustmentKeyPrefix+adj.ID {
		t.Fatalf("retry %+v, %v; want the adjustment voucher booked", retry, err)
	}
	again, err := f.s.PostCostVouchers(f.ctx, f.ledger, GLPostRequest{LegalEntityID: f.le})
	if err != nil || len(again.Booked) != 0 || len(f.ledger.Entries()) != 1 {
		t.Errorf("second retry %+v entries %d, want nothing more booked", again, len(f.ledger.Entries()))
	}
}

func TestPostgres_AdjustmentIntoAClosedLedgerPeriodStaysPendingNotParked(t *testing.T) {
	f := newGLFixture(t, 55)
	product, site := ids.New(), ids.New()
	f.stock(t, "stock-1", f.le, product, site, "100", "950.00")
	f.ledger.RefuseClosedThrough("2026-12-31")

	adj, _, err := f.s.CreateAdjustment(f.ctx, onHand(f.le, product, site, domain.ModeAmount, "50"), "closed-1", hashOf("c"), f.ledger)
	if err != nil || adj.Voucher != voucherPending {
		t.Fatalf("adjustment %+v, %v; want the voucher PENDING", adj, err)
	}
	f.dueNow(t)
	res, err := f.s.PostCostVouchers(f.ctx, f.ledger, GLPostRequest{LegalEntityID: f.le})
	if err != nil || len(res.Booked) != 0 || res.Parked != 0 || len(res.Failed) != 1 || res.Failed[0].Code != ledgerclient.CodePeriodClosed {
		t.Errorf("result %+v, %v; want the closed-period refusal reported and nothing parked", res, err)
	}
}

func hashOf(name string) string { return (name + pad + pad)[:64] }
