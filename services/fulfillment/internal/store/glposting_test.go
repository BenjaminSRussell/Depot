package store_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/issueclaim"
	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/services/fulfillment/internal/store"
)

type slipGL struct {
	s        *store.Postgres
	admin    *pgxpool.Pool
	ctx      context.Context
	f        fixture
	product  string
	claims   *issueclaim.Fake
	ledger   *ledgerclient.Fake
	ledgerID string
}

func newSlipGL(t *testing.T, tenantN int) slipGL {
	t.Helper()
	s, admin := setup(t)
	g := slipGL{s: s, admin: admin, ctx: ctxFor(tenantN, ""), product: ids.New(), claims: &issueclaim.Fake{}, ledger: ledgerclient.NewFake(), ledgerID: ids.New()}
	g.claims.SetCost(g.product, "USD", dq("12.5"))
	g.f = releasedOf(t, s, g.ctx, ids.New(), ids.New(), []string{g.product}, 3)
	g.ledger.AddLedger(g.ledgerID, g.f.legalEntity)
	s.WithHolds(g.claims).WithLedger(g.ledger, map[string]string{g.f.legalEntity: g.ledgerID})
	return g
}

func (g slipGL) post(t *testing.T, requestCost string) store.PackingSlip {
	t.Helper()
	packed := pickAndPack(t, g.s, g.ctx, g.f.shipment)
	in := store.PostInput{PostingDate: today(), Currency: "USD"}
	if requestCost != "" {
		in.Costs = costs(g.f.shipment, requestCost)
	}
	slip, err := g.s.PostPackingSlip(g.ctx, g.f.shipment.ID, packed.Shipment.Version, in, idem("post"))
	if err != nil {
		t.Fatalf("PostPackingSlip: %v", err)
	}
	return slip
}

func (g slipGL) net(t *testing.T, account string) decimal.Decimal {
	t.Helper()
	bal, err := g.ledger.Balance(g.ctx, ledgerclient.BalanceQuery{LegalEntityID: g.f.legalEntity, LedgerID: g.ledgerID, Account: account})
	if err != nil {
		t.Fatal(err)
	}
	return bal.Net()
}

func (g slipGL) row(t *testing.T, slipID string) (status string, attempts int, parked bool, lastError string) {
	t.Helper()
	if err := g.admin.QueryRow(context.Background(), `select status, attempts, parked, coalesce(last_error, '') from packing_slip_gl where slip_id = $1`, slipID).
		Scan(&status, &attempts, &parked, &lastError); err != nil {
		t.Fatal(err)
	}
	return
}

func (g slipGL) due(t *testing.T, slipID string) {
	t.Helper()
	if _, err := g.admin.Exec(context.Background(), `update packing_slip_gl set next_attempt_at = now() where slip_id = $1`, slipID); err != nil {
		t.Fatal(err)
	}
}

// AC-28.1 / AC-28.2: a slip for 3 units holds 3 at inventory, journals Dr 1405 /
// Cr 1400 at the valuation cost (the request's 99 is ignored), posts that journal
// to the ledger once, and the DNI report then equals the ledger's account 1405.
func TestSlipGL_PostsDrCogsCrInventoryAndTiesDNI(t *testing.T) {
	g := newSlipGL(t, 31)
	slip := g.post(t, "99")
	if !slip.TotalCost.Equal(dq("37.5")) || !g.claims.Claimed(g.product, issueclaim.Ship).Equal(dq("3")) {
		t.Fatalf("total cost %s, ship claims %s: want 37.5 (3 x the valuation 12.5) and 3 held", slip.TotalCost, g.claims.Claimed(g.product, issueclaim.Ship))
	}
	assertJournal(t, slip.Journal, []store.JournalEntry{
		{Account: "1405", Amount: dq("37.5"), Currency: "USD", OrderLineID: g.f.shipment.Lines[0].OrderLineID},
		{Account: "1400", Amount: dq("-37.5"), Currency: "USD", OrderLineID: g.f.shipment.Lines[0].OrderLineID},
	})
	if slip.Lines[0].CostSnapshotID != "inventory:"+g.product+":"+today().Format("2006-01-02") {
		t.Fatalf("cost snapshot %s", slip.Lines[0].CostSnapshotID)
	}

	before, err := g.s.ReconcileDNI(g.ctx, g.f.legalEntity, "USD", today())
	if err != nil || before.Reconciled || before.PendingSlips != 1 || !before.ReportNet.Equal(dq("37.5")) || !before.LedgerNet.IsZero() {
		t.Fatalf("before the post to the ledger: %+v, %v", before, err)
	}
	if n, err := g.s.PostSlipsToGL(context.Background()); err != nil || n != 1 {
		t.Fatalf("PostSlipsToGL = %d, %v; want 1", n, err)
	}
	entries := g.ledger.Entries()
	if len(entries) != 1 || entries[0].IdempotencyKey != "post:fulfillment:packingslip:"+slip.ID+":issue" || entries[0].LedgerID != g.ledgerID ||
		len(entries[0].Lines) != 2 || entries[0].Lines[0].Account != "1405" || entries[0].Lines[0].Credit || entries[0].Lines[1].Account != "1400" || !entries[0].Lines[1].Credit {
		t.Fatalf("ledger entries %+v", entries)
	}
	if status, _, _, _ := g.row(t, slip.ID); status != "POSTED" {
		t.Fatalf("queue row status %s", status)
	}
	after, err := g.s.ReconcileDNI(g.ctx, g.f.legalEntity, "USD", today())
	if err != nil || !after.Reconciled || !after.LedgerNet.Equal(dq("37.5")) || !after.ReportNet.Equal(after.LedgerNet) {
		t.Fatalf("after: %+v, %v; want the DNI report equal to the ledger's 1405", after, err)
	}
	if n, err := g.s.PostSlipsToGL(context.Background()); err != nil || n != 0 || len(g.ledger.Entries()) != 1 {
		t.Fatalf("a second pass = %d, %v with %d ledger entries; want nothing more posted", n, err, len(g.ledger.Entries()))
	}
}

func TestSlipGL_CancelPostsTheMirror(t *testing.T) {
	g := newSlipGL(t, 32)
	slip := g.post(t, "")
	rev, err := g.s.CancelPackingSlip(g.ctx, slip.ID, idem("cancel"))
	if err != nil {
		t.Fatal(err)
	}
	for pass := 1; pass <= 2; pass++ {
		if n, err := g.s.PostSlipsToGL(context.Background()); err != nil || n != 1 {
			t.Fatalf("pass %d: PostSlipsToGL = %d, %v; want the slip, then its mirror", pass, n, err)
		}
	}
	keys := map[string]bool{}
	for _, e := range g.ledger.Entries() {
		keys[e.IdempotencyKey] = true
	}
	if !keys["post:fulfillment:packingslip:"+slip.ID+":issue"] || !keys["post:fulfillment:packingslip:"+rev.ID+":cancel"] {
		t.Fatalf("ledger keys %v", keys)
	}
	if !g.net(t, "1405").IsZero() || !g.net(t, "1400").IsZero() {
		t.Fatalf("after the cancel 1405 nets %s and 1400 nets %s, both want 0", g.net(t, "1405"), g.net(t, "1400"))
	}
	if rec, err := g.s.ReconcileDNI(g.ctx, g.f.legalEntity, "USD", today()); err != nil || !rec.Reconciled || !rec.ReportNet.IsZero() {
		t.Fatalf("reconcile after the cancel: %+v, %v", rec, err)
	}
	if n := g.claims.Claimed(g.product, issueclaim.Ship); !n.Equal(dq("3")) {
		t.Fatalf("the cancel does not release the ship claim; inventory settles it with the delivery: %s", n)
	}
}

func TestSlipGL_LedgerOutageKeepsSlipPendingAndARefusalParksIt(t *testing.T) {
	g := newSlipGL(t, 33)
	slip := g.post(t, "")
	g.ledger.FailPosts(errors.New("ledger down"))
	if n, err := g.s.PostSlipsToGL(context.Background()); err == nil || n != 0 {
		t.Fatalf("outage: %d, %v; want an error and nothing posted", n, err)
	}
	status, attempts, parked, lastError := g.row(t, slip.ID)
	if status != "PENDING" || attempts != 1 || parked || lastError == "" {
		t.Fatalf("after an outage: %s attempts %d parked %t error %q", status, attempts, parked, lastError)
	}
	if got, err := g.s.GetPackingSlip(g.ctx, slip.ID); err != nil || got.Status != "POSTED" {
		t.Fatalf("the slip must stay POSTED: %v, %v", got.Status, err)
	}
	if n, err := g.s.PostSlipsToGL(context.Background()); err != nil || n != 0 {
		t.Fatalf("inside the back-off: %d, %v; want the row left alone", n, err)
	}

	g.due(t, slip.ID)
	g.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	if n, err := g.s.PostSlipsToGL(context.Background()); err == nil || n != 0 {
		t.Fatalf("refusal: %d, %v", n, err)
	}
	if _, _, parked, lastError = g.row(t, slip.ID); !parked || lastError == "" {
		t.Fatalf("a closed period must park the row with its error: parked %t error %q", parked, lastError)
	}
	g.ledger.FailPosts(nil)
	if n, err := g.s.PostSlipsToGL(context.Background()); err != nil || n != 0 || len(g.ledger.Entries()) != 0 {
		t.Fatalf("a parked row is not retried: %d, %v, %d entries", n, err, len(g.ledger.Entries()))
	}
}

func TestSlipGL_AReplayedPostQueuesOneJournal(t *testing.T) {
	g := newSlipGL(t, 34)
	packed := pickAndPack(t, g.s, g.ctx, g.f.shipment)
	key := idem("post")
	in := store.PostInput{PostingDate: today(), Currency: "USD"}
	for range 2 {
		if _, err := g.s.PostPackingSlip(g.ctx, g.f.shipment.ID, packed.Shipment.Version, in, key); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := g.s.PostSlipsToGL(context.Background()); err != nil || n != 1 || len(g.ledger.Entries()) != 1 {
		t.Fatalf("PostSlipsToGL = %d, %v with %d entries; want one", n, err, len(g.ledger.Entries()))
	}
}

func TestPostPackingSlip_PricesFromInventoryValuation(t *testing.T) {
	g := newSlipGL(t, 35)
	packed := pickAndPack(t, g.s, g.ctx, g.f.shipment)
	in := store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(g.f.shipment, "99")}
	g.claims.FailCosts(errors.New("inventory down"))
	if _, err := g.s.PostPackingSlip(g.ctx, g.f.shipment.ID, packed.Shipment.Version, in, idem("down")); problemCode(err) != "COM-0503" {
		t.Fatalf("valuation outage: %v, want COM-0503", err)
	}
	if slips, err := g.s.ListPackingSlips(g.ctx, store.PackingSlipFilter{ShipmentID: g.f.shipment.ID, Limit: 10}); err != nil || len(slips) != 0 {
		t.Fatalf("a refused post left %d slips, %v", len(slips), err)
	}
	g.claims.FailCosts(nil)
	g.claims.SetCost(g.product, "EUR", dq("1"))
	if _, err := g.s.PostPackingSlip(g.ctx, g.f.shipment.ID, packed.Shipment.Version, in, idem("eur")); problemCode(err) != store.CodeCostCurrency {
		t.Fatalf("stock valued in EUR for a USD slip: %v, want %s", err, store.CodeCostCurrency)
	}
	g.claims.SetCost(g.product, "USD", dq("12.5"))
	slip, err := g.s.PostPackingSlip(g.ctx, g.f.shipment.ID, packed.Shipment.Version, in, idem("ok"))
	if err != nil || !slip.Lines[0].CostAmount.Equal(dq("37.5")) {
		t.Fatalf("slip %+v, %v; want the line costed at the valuation, not the request's 99", slip.Lines, err)
	}
}

func TestSlipGL_ACancelOfAnInvoicedSlipQueuesNothing(t *testing.T) {
	g := newSlipGL(t, 36)
	slip := g.post(t, "")
	if _, err := g.admin.Exec(context.Background(), `update packing_slip set status = 'INVOICED' where id = $1`, slip.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := g.s.CancelPackingSlip(g.ctx, slip.ID, idem("cancel")); problemCode(err) != store.CodeSlipInvoiced {
		t.Fatalf("cancel invoiced: %v, want %s", err, store.CodeSlipInvoiced)
	}
	var n int
	if err := g.admin.QueryRow(context.Background(), `select count(*) from packing_slip_gl`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d queued journals, %v; want only the original's", n, err)
	}
}

func TestSlipGL_ReconcileCutsTheReportAtTheMonthTheLedgerIsReadTo(t *testing.T) {
	g := newSlipGL(t, 37)
	g.post(t, "")
	if n, err := g.s.PostSlipsToGL(context.Background()); err != nil || n != 1 {
		t.Fatalf("PostSlipsToGL = %d, %v; want 1", n, err)
	}
	monthStart := time.Date(today().Year(), today().Month(), 1, 0, 0, 0, 0, time.UTC)
	rec, err := g.s.ReconcileDNI(g.ctx, g.f.legalEntity, "USD", monthStart)
	if err != nil || !rec.Reconciled || !rec.ReportNet.Equal(dq("37.5")) || !rec.LedgerNet.Equal(dq("37.5")) {
		t.Fatalf("asOf the first of the month: %+v, %v; want the report and the ledger cut at the month's end", rec, err)
	}
}

func (g slipGL) park(t *testing.T) {
	t.Helper()
	g.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	if _, err := g.s.PostSlipsToGL(context.Background()); err == nil {
		t.Fatal("a closed period must refuse the post")
	}
	g.ledger.FailPosts(nil)
}

func (g slipGL) gauges(t *testing.T) string {
	t.Helper()
	var out bytes.Buffer
	g.s.WriteSlipGLMetrics(&out)
	return out.String()
}

func TestSlipGL_ReleaseReschedulesAParkedRow(t *testing.T) {
	g := newSlipGL(t, 38)
	slip := g.post(t, "")
	g.park(t)
	key := idem("release")
	released, err := g.s.ReleaseSlipGL(g.ctx, slip.ID, key)
	if err != nil || released.Parked || released.Attempts != 0 || released.Status != "PENDING" || released.LastError == "" {
		t.Fatalf("release: %+v, %v; want unparked, attempts 0, the last error kept", released, err)
	}
	if again, err := g.s.ReleaseSlipGL(g.ctx, slip.ID, key); err != nil || again.SlipID != slip.ID {
		t.Fatalf("a replay of the release: %+v, %v", again, err)
	}
	if _, err := g.s.ReleaseSlipGL(g.ctx, slip.ID, store.Idem{Key: key.Key, Hash: "other"}); problemCode(err) != "idempotency_key_reuse" {
		t.Fatalf("the key with another request: %v", err)
	}
	if n, err := g.s.PostSlipsToGL(context.Background()); err != nil || n != 1 {
		t.Fatalf("after the release PostSlipsToGL = %d, %v; want the journal posted", n, err)
	}
	if status, _, parked, _ := g.row(t, slip.ID); status != "POSTED" || parked {
		t.Fatalf("row %s parked %t", status, parked)
	}
}

func TestSlipGL_ReleaseRefusesWhatIsNotParked(t *testing.T) {
	g := newSlipGL(t, 39)
	slip := g.post(t, "")
	if _, err := g.s.ReleaseSlipGL(g.ctx, slip.ID, idem("pending")); problemCode(err) != store.CodeSlipGLState {
		t.Fatalf("release of a pending row: %v, want %s", err, store.CodeSlipGLState)
	}
	if _, err := g.s.PostSlipsToGL(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := g.s.ReleaseSlipGL(g.ctx, slip.ID, idem("posted")); problemCode(err) != store.CodeSlipGLState {
		t.Fatalf("release of a posted row: %v, want %s", err, store.CodeSlipGLState)
	}
	if status, attempts, _, _ := g.row(t, slip.ID); status != "POSTED" || attempts != 1 {
		t.Fatalf("the refused release changed the row: %s attempts %d", status, attempts)
	}
	if _, err := g.s.ReleaseSlipGL(g.ctx, ids.New(), idem("none")); problemCode(err) != store.CodeSlipGLNotFound {
		t.Fatalf("release of a slip with no journal: %v, want %s", err, store.CodeSlipGLNotFound)
	}
	if _, err := g.s.ReleaseSlipGL(ctxFor(40, ""), slip.ID, idem("other-tenant")); problemCode(err) != store.CodeSlipGLNotFound {
		t.Fatalf("release across tenants: %v, want %s", err, store.CodeSlipGLNotFound)
	}
}

func TestSlipGL_CancelWaitsForAParkedOrPendingOriginal(t *testing.T) {
	g := newSlipGL(t, 41)
	slip := g.post(t, "")
	g.park(t)
	rev, err := g.s.CancelPackingSlip(g.ctx, slip.ID, idem("cancel"))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := g.s.PostSlipsToGL(context.Background()); err != nil || n != 0 || len(g.ledger.Entries()) != 0 {
		t.Fatalf("original parked: PostSlipsToGL = %d, %v with %d entries; want neither posted", n, err, len(g.ledger.Entries()))
	}
	if status, attempts, parked, _ := g.row(t, rev.ID); status != "PENDING" || attempts != 0 || parked {
		t.Fatalf("the held cancel: %s attempts %d parked %t; want PENDING, untouched", status, attempts, parked)
	}
	if _, err := g.s.ReleaseSlipGL(g.ctx, slip.ID, idem("release")); err != nil {
		t.Fatal(err)
	}
	for pass := 1; pass <= 2; pass++ {
		if n, err := g.s.PostSlipsToGL(context.Background()); err != nil || n != 1 {
			t.Fatalf("pass %d after the release: %d, %v; want one journal", pass, n, err)
		}
	}
	entries := g.ledger.Entries()
	if len(entries) != 2 || !strings.HasSuffix(entries[0].IdempotencyKey, slip.ID+":issue") || !strings.HasSuffix(entries[1].IdempotencyKey, rev.ID+":cancel") {
		t.Fatalf("ledger entries %+v; want the original, then its mirror", entries)
	}
}

func TestSlipGL_ACancelWhoseOriginalQueuedNothingStillPosts(t *testing.T) {
	g := newSlipGL(t, 42)
	slip := g.post(t, "")
	rev, err := g.s.CancelPackingSlip(g.ctx, slip.ID, idem("cancel"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.admin.Exec(context.Background(), `delete from packing_slip_gl where slip_id = $1`, slip.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := g.s.PostSlipsToGL(context.Background()); err != nil || n != 1 {
		t.Fatalf("PostSlipsToGL = %d, %v; want the cancel posted", n, err)
	}
	if status, _, _, _ := g.row(t, rev.ID); status != "POSTED" {
		t.Fatalf("cancel row %s", status)
	}
}

func TestSlipGL_GaugesCountPendingAndParked(t *testing.T) {
	g := newSlipGL(t, 43)
	slip := g.post(t, "")
	if _, err := g.s.CancelPackingSlip(g.ctx, slip.ID, idem("cancel")); err != nil {
		t.Fatal(err)
	}
	if got := g.gauges(t); !strings.Contains(got, "depot_fulfillment_slip_gl_pending 2\n") || !strings.Contains(got, "depot_fulfillment_slip_gl_parked 0\n") {
		t.Fatalf("two pending: %s", got)
	}
	g.park(t)
	if got := g.gauges(t); !strings.Contains(got, "depot_fulfillment_slip_gl_pending 1\n") || !strings.Contains(got, "depot_fulfillment_slip_gl_parked 1\n") {
		t.Fatalf("one parked, the held cancel pending: %s", got)
	}
	if _, err := g.s.ReleaseSlipGL(g.ctx, slip.ID, idem("release")); err != nil {
		t.Fatal(err)
	}
	if got := g.gauges(t); !strings.Contains(got, "depot_fulfillment_slip_gl_pending 2\n") || !strings.Contains(got, "depot_fulfillment_slip_gl_parked 0\n") {
		t.Fatalf("released: %s", got)
	}
}

func TestSlipGL_GaugesWriteNothingWhenTheCountsAreUnreadable(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	var out bytes.Buffer
	store.New(pool, "", 0).WriteSlipGLMetrics(&out)
	if out.Len() != 0 {
		t.Fatalf("wrote %q for an unreadable count; a missing series is the honest answer", out.String())
	}
}
