package domain_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
)

// TestV3HashKnownAnswer pins the B-07 payload: the v2 array with "audit-change-v3" first and legalEntityId, source and
// flag appended. The expected values are Python's
// sha256(json.dumps(values, ensure_ascii=False, separators=(",", ":"))).
func TestV3HashKnownAnswer(t *testing.T) {
	person := domain.Change{Entity: "vendor_bank_account", RecordID: "b-1", Field: "accountLast4", OldValue: "4821", NewValue: "7710",
		UserID: "ana", ActorKind: domain.ActorPerson, TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		OccurredAt: time.Date(2026, 11, 3, 9, 15, 0, 123456789, time.UTC), Reason: "vendor letter dated 3 Nov", ChangeSetID: "cs-1",
		LegalEntityID: "2F6C3E0A-8D6B-4C1E-9A43-0C5F3B1D7E21", Source: "party"}
	if got := domain.Hash(domain.Genesis, person.Normalize()); got != "2fad15afb4201db04499a488c81b878fb89ac921fce90a78e428501a24639b18" {
		t.Fatalf("v3 person hash = %s", got)
	}
	system := domain.Change{Entity: "vendor_bank_account", RecordID: "b-1", Field: "accountHolder", OldValue: "Acme <GmbH>",
		UserID: "system:party", ActorKind: domain.ActorSystem, TraceID: "cdc:party:812", ChangeSetID: "cdc:party:812",
		OccurredAt: time.Date(2026, 11, 3, 9, 15, 0, 0, time.UTC), Source: "party", Flag: domain.FlagUnattributed}
	if got := domain.Hash(domain.Genesis, system); got != "74096efa759597a5de77011b44d1ab5f3fbae0c2862f483128893a764ae07d81" {
		t.Fatalf("v3 system hash = %s", got)
	}
	// Each B-07 field is on the chain.
	base := domain.Hash(domain.Genesis, system)
	for name, mutate := range map[string]func(*domain.Change){
		"legalEntityId": func(c *domain.Change) { c.LegalEntityID = "2f6c3e0a-8d6b-4c1e-9a43-0c5f3b1d7e21" },
		"source":        func(c *domain.Change) { c.Source = "pricing" },
		"flag":          func(c *domain.Change) { c.Flag = "" },
	} {
		c := system
		mutate(&c)
		if domain.Hash(domain.Genesis, c) == base {
			t.Errorf("%s is not covered by the hash", name)
		}
	}
}

func TestValidateSystemActorAndPlacement(t *testing.T) {
	at := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	ok := domain.Change{Entity: "vendor_bank_account", RecordID: "b-1", Field: "bankCountry", NewValue: "DE", UserID: "system:party",
		ActorKind: domain.ActorSystem, Flag: domain.FlagUnattributed, TraceID: "cdc:party:1", OccurredAt: at, Source: "party",
		LegalEntityID: "2f6c3e0a-8d6b-4c1e-9a43-0c5f3b1d7e21"}
	if fields := domain.Validate(ok); len(fields) > 0 {
		t.Fatalf("valid unattributed change refused: %v", fields)
	}
	for name, tc := range map[string]struct {
		mutate func(*domain.Change)
		field  string
	}{
		"system without flag":       {func(c *domain.Change) { c.Flag = "" }, "flag"},
		"flag without system":       {func(c *domain.Change) { c.ActorKind, c.UserID = domain.ActorPerson, "ana" }, "flag"},
		"unknown flag":              {func(c *domain.Change) { c.Flag = "maybe" }, "flag"},
		"system as a person id":     {func(c *domain.Change) { c.UserID = "ana" }, "userId"},
		"system on behalf":          {func(c *domain.Change) { c.OnBehalfOf = "ana" }, "onBehalfOf"},
		"person with a system id":   {func(c *domain.Change) { c.ActorKind, c.Flag = domain.ActorPerson, "" }, "userId"},
		"legal entity not a uuid":   {func(c *domain.Change) { c.LegalEntityID = "acme" }, "legalEntityId"},
		"nil legal entity":          {func(c *domain.Change) { c.LegalEntityID = "00000000-0000-0000-0000-000000000000" }, "legalEntityId"},
		"source not a service name": {func(c *domain.Change) { c.Source = "Party DB" }, "source"},
	} {
		c := ok
		tc.mutate(&c)
		if fields := domain.Validate(c); fields[tc.field] == "" {
			t.Errorf("%s: want a %s error, got %v", name, tc.field, fields)
		}
	}
}

// chain links n v3 changes from Genesis, seq 10, 11, ...
func chain(t *testing.T, n int) []domain.ChainRow {
	t.Helper()
	prev := domain.Genesis
	var rows []domain.ChainRow
	for i := range n {
		e, err := domain.Link(prev, domain.Change{Entity: "vendor_bank_account", RecordID: "b-1", Field: "accountLast4",
			OldValue: fmt.Sprint(i), NewValue: fmt.Sprint(i + 1), UserID: "ana", ActorKind: domain.ActorPerson, TraceID: "t",
			OccurredAt: time.Date(2026, 11, 3, 9, i, 0, 0, time.UTC), Source: "party"})
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, domain.ChainRow{Seq: int64(10 + i), ID: fmt.Sprintf("e%d", i), Entry: e})
		prev = e.Hash
	}
	return rows
}

func walk(rows []domain.ChainRow, fromStart bool, anchors map[int64]string) domain.ChangeChainStatus {
	w := domain.NewChangeWalker(fromStart, anchors)
	for _, r := range rows {
		w.Next(r)
	}
	return w.Status()
}

func TestChangeWalkerFindsTheFirstBreak(t *testing.T) {
	rows := chain(t, 5)
	if s := walk(rows, true, map[int64]string{12: rows[2].Hash}); !s.Verified || s.Entries != 5 || s.Head != rows[4].Hash || s.AnchorsChecked != 1 || s.FromSeq != 10 || s.ToSeq != 14 {
		t.Fatalf("intact chain %+v", s)
	}

	tampered := append([]domain.ChainRow(nil), rows...)
	tampered[2].NewValue = "9999"
	s := walk(tampered, true, nil)
	if s.Verified || s.Breaks != 1 || s.FirstBreak == nil || *s.FirstBreak != (domain.Break{Seq: 12, ID: "e2", Kind: domain.BreakHash}) {
		t.Fatalf("a changed value is one hash break at its row: %+v %+v", s, s.FirstBreak)
	}

	removed := append(append([]domain.ChainRow(nil), rows[:2]...), rows[3:]...)
	if s := walk(removed, true, nil); s.Verified || s.FirstBreak.Kind != domain.BreakLink || s.FirstBreak.Seq != 13 {
		t.Fatalf("a removed row is a link break at the next row: %+v", s.FirstBreak)
	}

	if s := walk(rows[2:], true, nil); s.Verified || s.FirstBreak.Kind != domain.BreakLink {
		t.Fatalf("a walk from the start must link to genesis: %+v", s)
	}
	if s := walk(rows[2:], false, nil); !s.Verified || s.Entries != 3 {
		t.Fatalf("a range trusts its first row's prevHash: %+v", s)
	}

	// Cut short after the anchor of seq 14: every remaining row links, only the anchor tells.
	if s := walk(rows[:3], true, map[int64]string{14: rows[4].Hash}); s.Verified || s.FirstBreak.Kind != domain.BreakAnchor || s.FirstBreak.Seq != 14 || s.AnchorsMismatched != 1 {
		t.Fatalf("an anchored head the chain no longer holds is a break: %+v", s)
	}
	if s := walk(rows, true, map[int64]string{13: rows[1].Hash}); s.Verified || s.FirstBreak.Kind != domain.BreakAnchor || s.FirstBreak.Seq != 13 {
		t.Fatalf("an anchor whose hash differs is a break: %+v", s)
	}
}

// Legacy rows were hashed over the occurredAt they were given, up to nanoseconds; Postgres keeps microseconds. The
// walker recovers the lost part and reports the row as a known legacy-precision break, not tampering, and still
// reports a changed legacy row as a hash break.
func TestChangeWalkerReportsLegacyPrecisionApart(t *testing.T) {
	given := time.Date(2026, 10, 1, 8, 0, 0, 123456789, time.UTC)
	legacy := domain.Change{Entity: "document", RecordID: "d-1", Field: "signature", NewValue: "ana", UserID: "service:engineering", TraceID: "t-1", OccurredAt: given}
	hash := domain.Hash(domain.Genesis, legacy)
	stored := legacy
	stored.OccurredAt = given.Truncate(time.Microsecond)
	row := domain.ChainRow{Seq: 1, ID: "l1", Entry: domain.Entry{Change: stored, PrevHash: domain.Genesis, Hash: hash}}
	next, err := domain.Link(hash, domain.Change{Entity: "document", RecordID: "d-1", Field: "status", NewValue: "released", UserID: "ana",
		ActorKind: domain.ActorPerson, TraceID: "t-2", OccurredAt: given.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	rows := []domain.ChainRow{row, {Seq: 2, ID: "n1", Entry: next}}
	if s := walk(rows, true, nil); !s.Verified || s.LegacyPrecision != 1 || s.Breaks != 0 {
		t.Fatalf("legacy precision is a known break: %+v", s)
	}
	rows[0].NewValue = "mallory"
	if s := walk(rows, true, nil); s.Verified || s.LegacyPrecision != 0 || s.FirstBreak.Kind != domain.BreakHash || s.FirstBreak.Seq != 1 {
		t.Fatalf("a changed legacy row is tampering: %+v", s)
	}
}

// Before B-06, appends were not serialised, so two legacy rows could link to the same predecessor.
func TestChangeWalkerReportsALegacyForkApart(t *testing.T) {
	at := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	a := domain.Change{Entity: "document", RecordID: "d-1", Field: "signature", NewValue: "ana", UserID: "u", TraceID: "t-1", OccurredAt: at}
	b := a
	b.NewValue = "bob"
	ea := domain.Entry{Change: a, PrevHash: domain.Genesis, Hash: domain.Hash(domain.Genesis, a)}
	eb := domain.Entry{Change: b, PrevHash: domain.Genesis, Hash: domain.Hash(domain.Genesis, b)}
	rows := []domain.ChainRow{{Seq: 1, ID: "a", Entry: ea}, {Seq: 2, ID: "b", Entry: eb}}
	if s := walk(rows, true, nil); !s.Verified || s.LegacyFork != 1 {
		t.Fatalf("legacy fork is a known break: %+v", s)
	}
}
