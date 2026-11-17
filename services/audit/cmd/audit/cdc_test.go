package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
	"github.com/BenjaminSRussell/depot/services/audit/internal/store"
)

type fakeAnchorer struct {
	broken  map[string]bool
	tenants []string
	cutoffs []time.Time
}

func (f *fakeAnchorer) AnchorDay(ctx context.Context, day time.Time) (store.Anchor, error) {
	p, _ := tenant.FromContext(ctx)
	f.tenants = append(f.tenants, p.TenantID+"@"+day.Format(time.DateOnly)+"/"+p.UserID)
	chain := store.ChangeChain{Verified: !f.broken[p.TenantID]}
	if !chain.Verified {
		chain.Breaks, chain.FirstBreak = 1, &domain.Break{Seq: 9, Kind: domain.BreakHash}
	}
	return store.Anchor{Date: day.Format(time.DateOnly), Created: true, Chain: chain}, nil
}

func (f *fakeAnchorer) PurgeTxContexts(_ context.Context, cutoff time.Time) (int64, error) {
	f.cutoffs = append(f.cutoffs, cutoff)
	return 0, nil
}

func TestAnchorTenantsAnchorsEachTenantAndFailsOnABreak(t *testing.T) {
	now := time.Date(2026, 11, 3, 1, 17, 0, 0, time.UTC)
	t1, t2 := testkit.TenantID(1), testkit.TenantID(2)
	f := &fakeAnchorer{broken: map[string]bool{t2: true}}
	err := anchorTenants(context.Background(), f, []string{t1, t2}, now)
	if err == nil || !strings.Contains(err.Error(), t2) || strings.Contains(err.Error(), t1) {
		t.Fatalf("err = %v; want the broken tenant named", err)
	}
	if len(f.tenants) != 2 || f.tenants[0] != t1+"@2026-11-03/service:audit-anchor" {
		t.Fatalf("anchored %v", f.tenants)
	}
	if f.cutoffs[0] != now.Add(-contextRetention) {
		t.Fatalf("purge cutoff %v", f.cutoffs[0])
	}
	if err := anchorTenants(context.Background(), &fakeAnchorer{}, []string{"not-a-uuid"}, now); err == nil {
		t.Fatal("a tenant that is not a UUID must be refused")
	}
}

func TestSubcommandGates(t *testing.T) {
	env := func(map[string]string) func(string) string { return func(string) string { return "" } }
	if err := chainNightly(context.Background(), env(nil), nil); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("no tenants: %v", err)
	}
	if err := chainNightly(context.Background(), env(nil), []string{testkit.TenantID(1)}); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("no database: %v", err)
	}
	if err := runCDCConsumer(context.Background(), env(nil)); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("no database: %v", err)
	}
	old := servicePrincipals
	t.Cleanup(func() { servicePrincipals = old })
	servicePrincipals = func() map[string][]string { return map[string][]string{} }
	if err := runCDCConsumer(context.Background(), env(nil)); err == nil || !strings.Contains(err.Error(), "not seeded") {
		t.Fatalf("unseeded consumer: %v", err)
	}
	if err := chainNightly(context.Background(), env(nil), []string{testkit.TenantID(1)}); err == nil || !strings.Contains(err.Error(), "not seeded") {
		t.Fatalf("unseeded job: %v", err)
	}
}
