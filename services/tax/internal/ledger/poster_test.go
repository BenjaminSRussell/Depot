package ledger_test

import (
	"context"
	"errors"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/services/tax/internal/domain"
	"github.com/BenjaminSRussell/depot/services/tax/internal/ledger"
)

func settlementOf(net string, lines ...domain.CodeSettlement) domain.Settlement {
	return domain.Settlement{PeriodFrom: january.From, PeriodTo: january.To, NetAmount: domain.D(net), Lines: lines, TransactionIDs: []string{"t1"}}
}

func posterOn(fake *ledgerclient.Fake) ledger.Poster {
	fake.AddLedger(ledgerID, entityID)
	return ledger.Poster{Client: fake, RunID: "run-1", LegalEntityID: entityID, LedgerID: ledgerID, Currency: "EUR", Accounts: accounts, SettlementAccount: "2900"}
}

func sides(e ledgerclient.Stored) map[string]string {
	out := map[string]string{}
	for _, l := range e.Lines {
		side := "D"
		if l.Credit {
			side = "C"
		}
		out[l.Account] = side + l.Amount.String()
	}
	return out
}

func TestPoster_Post(t *testing.T) {
	line := func(direction, amount string) domain.CodeSettlement {
		return domain.CodeSettlement{Code: "VAT19", Direction: direction, Amount: domain.D(amount)}
	}
	tests := []struct {
		name string
		s    domain.Settlement
		want map[string]string
	}{
		{"net payable credits the settlement account", settlementOf("14", line(domain.DirOutput, "19"), line(domain.DirInput, "5")), map[string]string{"2200": "D19", "1400": "C5", "2900": "C14"}},
		{"net refund debits the settlement account", settlementOf("-14", line(domain.DirOutput, "5"), line(domain.DirInput, "19")), map[string]string{"2200": "D5", "1400": "C19", "2900": "D14"}},
		{"input only run", settlementOf("-5", line(domain.DirInput, "5")), map[string]string{"1400": "C5", "2900": "D5"}},
		{"negative line flips its side", settlementOf("-4", line(domain.DirOutput, "-4")), map[string]string{"2200": "C4", "2900": "D4"}},
		{"zero net omits the settlement line", settlementOf("0", line(domain.DirOutput, "5"), line(domain.DirInput, "5")), map[string]string{"2200": "D5", "1400": "C5"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := ledgerclient.NewFake()
			id, err := posterOn(fake).Post(context.Background(), domain.PostingKey("run-1"), tc.s)
			if err != nil || id == "" {
				t.Fatalf("post: %q %v", id, err)
			}
			entries := fake.Entries()
			if len(entries) != 1 || entries[0].IdempotencyKey != "post:tax:settlement:run-1:settle" || entries[0].PostingDate != "2026-01-31" {
				t.Fatalf("entries %+v", entries)
			}
			got := sides(entries[0])
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for account, want := range tc.want {
				if got[account] != want {
					t.Errorf("account %s: got %s, want %s", account, got[account], want)
				}
			}
		})
	}
}

func TestPoster_ReplayPostsOneEntry(t *testing.T) {
	fake := ledgerclient.NewFake()
	poster := posterOn(fake)
	s := settlementOf("14", domain.CodeSettlement{Code: "VAT19", Direction: domain.DirOutput, Amount: domain.D("14")})
	first, err := poster.Post(context.Background(), domain.PostingKey("run-1"), s)
	if err != nil {
		t.Fatal(err)
	}
	second, err := poster.Post(context.Background(), domain.PostingKey("run-1"), s)
	if err != nil || second != first || len(fake.Entries()) != 1 {
		t.Fatalf("replay: %q %v entries %d", second, err, len(fake.Entries()))
	}
}

func TestPoster_RefusesBeforeTheLedgerIsCalled(t *testing.T) {
	fake := ledgerclient.NewFake()
	poster := posterOn(fake)
	unmapped := settlementOf("3", domain.CodeSettlement{Code: "VAT7", Direction: domain.DirOutput, Amount: domain.D("3")})
	if _, err := poster.Post(context.Background(), domain.PostingKey("run-1"), unmapped); err == nil {
		t.Fatal("a code without an account was posted")
	}
	if _, err := poster.Post(context.Background(), domain.PostingKey("run-1"), settlementOf("0")); !errors.Is(err, ledgerclient.ErrEmptyEntry) {
		t.Fatalf("empty settlement: %v", err)
	}
	if len(fake.Entries()) != 0 {
		t.Fatal("a refused settlement reached the ledger")
	}
}

func TestPoster_SurfacesLedgerRejection(t *testing.T) {
	fake := ledgerclient.NewFake()
	fake.RefuseClosedThrough("2026-01-31")
	s := settlementOf("14", domain.CodeSettlement{Code: "VAT19", Direction: domain.DirOutput, Amount: domain.D("14")})
	_, err := posterOn(fake).Post(context.Background(), domain.PostingKey("run-1"), s)
	if !ledgerclient.Rejected(err) || ledgerclient.CodeOf(err) != ledgerclient.CodePeriodClosed {
		t.Fatalf("got %v", err)
	}
}

func TestAccountingCurrency(t *testing.T) {
	fake := ledgerclient.NewFake()
	fake.AddLedger(ledgerID, entityID)
	post(t, fake, "2026-01-15", "k1", "1200", "2200", "1.00")
	if cur, err := ledger.AccountingCurrency(context.Background(), fake, entityID, ledgerID); err != nil || cur != "EUR" {
		t.Fatalf("got %q %v", cur, err)
	}
	if _, err := ledger.AccountingCurrency(context.Background(), fake, entityID, "00000000-0000-0000-0000-0000000000ff"); !errors.Is(err, ledger.ErrRead) {
		t.Fatalf("unknown ledger: %v", err)
	}
	if _, err := ledger.AccountingCurrency(context.Background(), noReader{}, entityID, ledgerID); !errors.Is(err, ledger.ErrNotConfigured) {
		t.Fatalf("ledger without a reader: %v", err)
	}
}

type noReader struct{ ledgerclient.Ledger }
