package ledgerclient_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/ids"

	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
)

func TestFake_PostIsIdempotentPerKeyAndRejectsAChangedPayload(t *testing.T) {
	f := ledgerclient.NewFake()
	first, err := f.Post(principalCtx(), entry())
	again, err2 := f.Post(principalCtx(), entry())
	if err != nil || err2 != nil || first != again || len(f.Entries()) != 1 {
		t.Fatalf("first %q again %q errs %v %v entries %d", first, again, err, err2, len(f.Entries()))
	}
	changed := entry()
	changed.Lines[0].Amount, changed.Lines[1].Amount = decimal.NewFromInt(7), decimal.NewFromInt(7)
	if _, err := f.Post(principalCtx(), changed); ledgerclient.CodeOf(err) != ledgerclient.CodeIdempotencyConflict {
		t.Fatalf("err %v", err)
	}
}

func TestFake_RefusesAnUnbalancedEntry(t *testing.T) {
	bad := entry()
	bad.Lines[0].Amount = decimal.NewFromInt(1)
	if _, err := ledgerclient.NewFake().Post(principalCtx(), bad); !errors.Is(err, ledgerclient.ErrUnbalanced) {
		t.Fatalf("err %v", err)
	}
}

func TestFake_ReverseFlipsTheSidesOnceAndNetsTheBalanceToZero(t *testing.T) {
	f := ledgerclient.NewFake()
	id, _ := f.Post(principalCtx(), entry())
	q := ledgerclient.BalanceQuery{LedgerID: ledgerID, Account: "1400"}
	if b, _ := f.Balance(principalCtx(), q); !b.Net().Equal(decimal.RequireFromString("12.5")) {
		t.Fatalf("before %+v", b)
	}
	r := ledgerclient.Reversal{LegalEntityID: entityID, EntryID: id, IdempotencyKey: "rev-1"}
	rev, err := f.Reverse(principalCtx(), r)
	replay, err2 := f.Reverse(principalCtx(), r)
	if err != nil || err2 != nil || rev != replay {
		t.Fatalf("rev %q replay %q errs %v %v", rev, replay, err, err2)
	}
	if b, _ := f.Balance(principalCtx(), q); !b.Net().IsZero() {
		t.Fatalf("after %+v", b)
	}
	other := r
	other.IdempotencyKey = "rev-2"
	if _, err := f.Reverse(principalCtx(), other); ledgerclient.CodeOf(err) != ledgerclient.CodeAlreadyReversed {
		t.Fatalf("second reversal: %v", err)
	}
	if _, err := f.Reverse(principalCtx(), ledgerclient.Reversal{EntryID: "missing", IdempotencyKey: "x"}); ledgerclient.CodeOf(err) != ledgerclient.CodeEntryNotFound {
		t.Fatalf("missing: %v", err)
	}
}

func TestFake_BalanceHonoursLedgerAndPeriod(t *testing.T) {
	f := ledgerclient.NewFake()
	_, _ = f.Post(principalCtx(), entry())
	late := entry()
	late.IdempotencyKey, late.PostingDate = "key-2", "2026-12-01"
	_, _ = f.Post(principalCtx(), late)
	if b, _ := f.Balance(principalCtx(), ledgerclient.BalanceQuery{LedgerID: ledgerID, Account: "1400", PeriodTo: "2026-10"}); !b.Net().Equal(decimal.RequireFromString("12.5")) {
		t.Fatalf("period %+v", b)
	}
	if b, _ := f.Balance(principalCtx(), ledgerclient.BalanceQuery{LedgerID: "other", Account: "1400"}); !b.Net().IsZero() {
		t.Fatalf("ledger %+v", b)
	}
}

func TestFake_FailPostsInjectsTheLedgerAnswer(t *testing.T) {
	f := ledgerclient.NewFake()
	f.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	if _, err := f.Post(principalCtx(), entry()); ledgerclient.CodeOf(err) != ledgerclient.CodePeriodClosed || len(f.Entries()) != 0 {
		t.Fatalf("err %v", err)
	}
	f.FailPosts(nil)
	if _, err := f.Post(principalCtx(), entry()); err != nil {
		t.Fatal(err)
	}
}

func TestFake_PartnerBalancesPerCounterparty(t *testing.T) {
	f := ledgerclient.NewFake()
	owner, a, b := ids.New(), ids.New(), ids.New()
	post := func(key, partner string, amount int64) {
		t.Helper()
		_, err := f.Post(context.Background(), ledgerclient.Entry{LegalEntityID: owner, LedgerID: "L1", DocumentID: key, PostingDate: "2026-03-10", Currency: "USD", IdempotencyKey: key,
			Lines: []ledgerclient.Line{{Account: "1300", Amount: decimal.NewFromInt(amount), TradingPartnerID: partner}, {Account: "4900", Credit: true, Amount: decimal.NewFromInt(amount)}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	post("k1", a, 100)
	post("k2", b, 40)
	post("k3", a, 5)
	got, err := f.PartnerBalances(context.Background(), ledgerclient.PartnerBalanceQuery{LedgerID: "L1"})
	if err != nil || len(got.Rows) != 2 || got.Rows[0].Account != "1300" {
		t.Fatalf("rows: %+v %v", got, err)
	}
	by := map[string]decimal.Decimal{}
	for _, r := range got.Rows {
		by[r.TradingPartnerID] = r.Net()
	}
	if !by[a].Equal(decimal.NewFromInt(105)) || !by[b].Equal(decimal.NewFromInt(40)) {
		t.Fatalf("by partner: %v", by)
	}
	if _, err := f.Post(context.Background(), ledgerclient.Entry{LegalEntityID: owner, LedgerID: "L1", DocumentID: "k4", PostingDate: "2026-03-10", Currency: "USD", IdempotencyKey: "k4",
		Lines: []ledgerclient.Line{{Account: "1300", Amount: decimal.NewFromInt(1), TradingPartnerID: owner}, {Account: "4900", Credit: true, Amount: decimal.NewFromInt(1)}}}); ledgerclient.CodeOf(err) != "LEDGER_TRADING_PARTNER_INVALID" {
		t.Fatalf("a line naming its own entity as partner must be refused: %v", err)
	}
}
