package domain_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/services/close/internal/domain"
)

func lockRow(m domain.Module, s domain.Status, expires *time.Time) domain.LockRow {
	return domain.LockRow{Row: domain.Row{Module: m, Status: s, AllowedGroupID: "controllers"}, ReopenExpiresAt: expires}
}

func sources(rows ...domain.LockRow) map[string]domain.LockSource {
	out := map[string]domain.LockSource{}
	for _, s := range domain.LockSources(rows) {
		out[s.SourceSystem] = s
	}
	return out
}

// Each source meets what the ledger's close check (Resolve, BR-14.1) answers for it: the lock is the
// ledger's copy of close's answer, not a second rule.
func TestLockSources_FollowTheDecisionPerSource(t *testing.T) {
	got := sources(lockRow(domain.ModuleAll, domain.StatusOnHold, nil), lockRow(domain.ModuleAP, domain.StatusClosed, nil), lockRow(domain.ModuleTax, domain.StatusOpen, nil))
	for source, want := range map[string]domain.Status{
		"ap": domain.StatusClosed, "tax": domain.StatusOpen, "manual": domain.StatusOnHold, "gl": domain.StatusOnHold, "ar": domain.StatusOnHold,
		domain.LockSourceOther: domain.StatusOnHold,
	} {
		if got[source].Status != want {
			t.Errorf("source %s: %s, want %s", source, got[source].Status, want)
		}
	}
	if got["inv"].Module != domain.ModuleInventory || got["manual"].Module != domain.ModuleGL || got[domain.LockSourceOther].Module != domain.ModuleAll {
		t.Fatalf("modules %+v", got)
	}
	for source := range got {
		if source != domain.LockSourceOther && domain.ModuleForSource(source) == domain.ModuleAll {
			t.Errorf("lock source %s is not a mapped source system", source)
		}
	}
}

func TestLockSources_NoRowsIsNeverOpened(t *testing.T) {
	for source, s := range sources() {
		if s.Status != domain.StatusNeverOpened {
			t.Errorf("source %s: %s, want never_opened", source, s.Status)
		}
	}
}

func TestLockSources_CarryTheGoverningReopenExpiry(t *testing.T) {
	soon, later := time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)
	got := sources(lockRow(domain.ModuleAll, domain.StatusOpen, &later), lockRow(domain.ModuleAP, domain.StatusOpen, &soon), lockRow(domain.ModuleAR, domain.StatusClosed, nil))
	if e := got["ap"].OpenUntil; e == nil || !e.Equal(soon) {
		t.Fatalf("ap is governed by its own row's expiry: %v", e)
	}
	if e := got["manual"].OpenUntil; e == nil || !e.Equal(later) {
		t.Fatalf("manual is governed by all's expiry: %v", e)
	}
	if e := got["ar"].OpenUntil; e != nil {
		t.Fatalf("a closed row carries no expiry: %v", e)
	}
	if e := got[domain.LockSourceOther].OpenUntil; e == nil || !e.Equal(later) {
		t.Fatalf("an unmapped source is governed by all's expiry: %v", e)
	}
}

// Review of B-08c: with ALL open and AP closed (the FR-14.2 order: subledgers close first), a source close
// maps to no module (billing, inventory, costing, ...) resolves to the ALL row and stays open, as
// GET /v1/close/periods:resolve answers it; only AP's own postings are refused.
func TestLockSources_UnmappedSourceFollowsTheAllRowNotTheMostRestrictive(t *testing.T) {
	got := sources(lockRow(domain.ModuleAll, domain.StatusOpen, nil), lockRow(domain.ModuleAP, domain.StatusClosed, nil))
	if s := got[domain.LockSourceOther]; s.Status != domain.StatusOpen || s.Module != domain.ModuleAll {
		t.Fatalf("unmapped source with all open and ap closed: %+v", s)
	}
	if got["ap"].Status != domain.StatusClosed || got["manual"].Status != domain.StatusOpen {
		t.Fatalf("ap %+v, manual %+v", got["ap"], got["manual"])
	}
	rows := []domain.Row{{Module: domain.ModuleAll, Status: domain.StatusOpen}, {Module: domain.ModuleAP, Status: domain.StatusClosed}}
	if want := domain.Resolve(rows, domain.ModuleForSource("billing")).Status; got[domain.LockSourceOther].Status != want {
		t.Fatalf("the lock answers %s for billing, resolve answers %s", got[domain.LockSourceOther].Status, want)
	}
}
