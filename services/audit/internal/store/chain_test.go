package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/audit/internal/cdc"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
	"github.com/BenjaminSRussell/depot/services/audit/internal/store"
	"github.com/BenjaminSRussell/depot/services/audit/migrations"
)

const (
	leA = "2f6c3e0a-8d6b-4c1e-9a43-0c5f3b1d7e21"
	leB = "7a1d9e44-3b2c-4f5a-8e61-9c0b2d3e4f50"
)

func migratedWithApp(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool, *store.Postgres) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(tenant.With(context.Background(), principal(1)), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return admin, app, store.New(app)
}

func scoped(n int, le string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: le, UserID: "auditor"})
}

func placed(record, le string, at time.Time) domain.Change {
	return domain.Change{Entity: "vendor_bank_account", RecordID: record, Field: "accountLast4", OldValue: "1", NewValue: "2",
		UserID: "ana", ActorKind: domain.ActorPerson, TraceID: "t", OccurredAt: at, LegalEntityID: le, Source: "party"}
}

// TestVerifyReportsLegacyPrecisionRowsAsKnownBreaks (B-06 note, AUD-API-4): a row written before AUD-API-1 whose
// occurredAt had a sub-microsecond part does not recompute from the database, and :verify reports it as a known
// legacy-precision break, not tampering; the chain still verifies.
func TestVerifyReportsLegacyPrecisionRowsAsKnownBreaks(t *testing.T) {
	admin, _, s := migratedWithApp(t)
	ctx := scoped(1, "")
	given := time.Date(2026, 10, 1, 8, 0, 0, 123456789, time.UTC)
	legacy := domain.Change{Entity: "document", RecordID: "d-1", Field: "signature", NewValue: "ana", UserID: "service:engineering", TraceID: "t-0", OccurredAt: given}
	hash := domain.Hash(domain.Genesis, legacy)
	if err := dbx.WithTenantTx(ctx, admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into audit_entry (tenant_id, id, entity, record_id, field, old_value, new_value, user_id, trace_id, occurred_at, prev_hash, hash)
			values ($1,$2,$3,$4,$5,'',$6,$7,$8,$9,$10,$11)`, testkit.TenantID(1), ids.New(), legacy.Entity, legacy.RecordID, legacy.Field,
			legacy.NewValue, legacy.UserID, legacy.TraceID, legacy.OccurredAt, domain.Genesis, hash)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(ctx, placed("b-1", leA, given.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	chain, err := s.VerifyChanges(ctx, 0, 0)
	if err != nil || !chain.Verified || chain.Entries != 2 || chain.KnownBreaks.LegacyPrecision != 1 || chain.Breaks != 0 {
		t.Fatalf("verify %+v %v", chain, err)
	}
}

// TestAnchorDayWritesOneHeadPerTenantPerDay: the nightly job anchors each tenant's head once a day; a rerun keeps
// the first anchor; an anchored head the chain no longer holds (the chain cut short after it) fails :verify.
func TestAnchorDayWritesOneHeadPerTenantPerDay(t *testing.T) {
	admin, _, s := migratedWithApp(t)
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24*time.Hour + 77*time.Minute) // yesterday 01:17 UTC
	t1, t2 := scoped(1, ""), scoped(2, "")
	for i := range 3 {
		if err := s.Apply(t1, placed("b-1", leA, day.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Apply(t2, placed("b-9", leB, day)); err != nil {
		t.Fatal(err)
	}
	first, err := s.AnchorDay(t1, day)
	if err != nil || !first.Created || first.Entries != 3 || !first.Verified || first.HeadSeq == 0 || first.Date != day.Format(time.DateOnly) {
		t.Fatalf("first anchor %+v %v", first, err)
	}
	if err := s.Apply(t1, placed("b-1", leA, day.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	again, err := s.AnchorDay(t1, day.Add(3*time.Hour))
	if err != nil || again.Created || again.HeadSeq != first.HeadSeq || again.Chain.Entries != 4 {
		t.Fatalf("a rerun on the same day keeps the first anchor: %+v %v", again, err)
	}
	other, err := s.AnchorDay(t2, day)
	if err != nil || !other.Created || other.Entries != 1 {
		t.Fatalf("tenant 2 anchor %+v %v", other, err)
	}
	next, err := s.AnchorDay(t1, day.Add(24*time.Hour))
	if err != nil || !next.Created || next.Entries != 4 {
		t.Fatalf("next day %+v %v", next, err)
	}
	var anchors int
	if err := admin.QueryRow(t1, `select count(*) from audit_anchor where tenant_id = $1`, testkit.TenantID(1)).Scan(&anchors); err != nil || anchors != 2 {
		t.Fatalf("tenant 1 anchors = %d, %v", anchors, err)
	}
	if chain, err := s.VerifyChanges(t1, 0, 0); err != nil || !chain.Verified || chain.Anchors.Checked != 2 {
		t.Fatalf("verify with anchors %+v %v", chain, err)
	}
	// Cut the chain short after the latest anchor: every remaining row still links, only the anchor tells.
	if err := pgx.BeginFunc(t1, admin, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t1, `alter table audit_entry disable trigger audit_entry_no_mutation`); err != nil {
			return err
		}
		if _, err := tx.Exec(t1, `delete from audit_entry where tenant_id = $1 and seq = $2`, testkit.TenantID(1), next.HeadSeq); err != nil {
			return err
		}
		_, err := tx.Exec(t1, `alter table audit_entry enable trigger audit_entry_no_mutation`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	chain, err := s.VerifyChanges(t1, 0, 0)
	if err != nil || chain.Verified || chain.FirstBreak == nil || chain.FirstBreak.Kind != domain.BreakAnchor || chain.FirstBreak.Seq != next.HeadSeq || chain.Anchors.Mismatched != 1 {
		t.Fatalf("truncated chain %+v %+v %v", chain, chain.FirstBreak, err)
	}
}

// TestLegalEntityPolicyScopesTheTrail (D17 for audit): a reader scoped to one legal entity sees that entity's
// entries and the tenant-level ones (NULL legal entity), never another entity's; the app role cannot write an entry
// outside its scope; verification spans the tenant's whole chain whatever the caller's scope.
func TestLegalEntityPolicyScopesTheTrail(t *testing.T) {
	_, app, s := migratedWithApp(t)
	at := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	writer := scoped(1, "")
	for _, c := range []domain.Change{placed("b-1", leA, at), placed("b-1", leB, at), placed("b-1", "", at)} {
		if err := s.Apply(writer, c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Query(scoped(1, leA), "vendor_bank_account", "b-1")
	if err != nil || len(got) != 2 || got[0].LegalEntityID != leA || got[1].LegalEntityID != "" {
		t.Fatalf("scoped to A: %+v %v", got, err)
	}
	if got, err := s.Query(scoped(2, leA), "vendor_bank_account", "b-1"); err != nil || len(got) != 0 {
		t.Fatalf("another tenant: %+v %v", got, err)
	}
	all := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "admin", Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	if got, err := s.Query(all, "vendor_bank_account", "b-1"); err != nil || len(got) != 3 {
		t.Fatalf("all scope: %d %v", len(got), err)
	}
	if chain, err := s.VerifyChanges(scoped(1, leA), 0, 0); err != nil || !chain.Verified || chain.Entries != 3 {
		t.Fatalf("verification spans every legal entity: %+v %v", chain, err)
	}
	ctx := scoped(1, leA)
	err = dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into audit_entry (tenant_id, id, entity, record_id, field, user_id, trace_id, occurred_at, prev_hash, hash, legal_entity_id)
			values ($1,$2,'e','r','f','u','t',now(),$3,$4,$5)`, testkit.TenantID(1), ids.New(), domain.Genesis, domain.Genesis, leB)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("an entry outside the writer's scope = %v; want an RLS violation", err)
	}
}

// TestAnchorsCannotBePlanted (review finding 6): depot_app reads anchors but cannot insert, rewrite or delete them; an
// anchor comes only through audit_anchor_record, which takes the head hash from the chain and refuses a future date,
// a date before yesterday and a head the chain does not hold.
func TestAnchorsCannotBePlanted(t *testing.T) {
	_, app, s := migratedWithApp(t)
	ctx := scoped(1, "")
	if err := s.Apply(ctx, placed("b-1", leA, time.Now())); err != nil {
		t.Fatal(err)
	}
	a, err := s.AnchorDay(ctx, time.Now())
	if err != nil || !a.Created || a.HeadSeq == 0 {
		t.Fatalf("anchor %+v %v", a, err)
	}
	asApp := func(stmt string, args ...any) error {
		return dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `select set_config('depot.legal_entity_scopes', '*', true)`); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, stmt, args...)
			return err
		})
	}
	code := func(err error) string {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return pgErr.Code
		}
		return ""
	}
	tomorrow := time.Now().UTC().Add(24 * time.Hour).Format(time.DateOnly)
	for stmt, want := range map[string]string{
		`update audit_anchor set verified = false`: "42501",
		`delete from audit_anchor`:                 "42501",
		`insert into audit_anchor (tenant_id, anchor_date, head_seq, head_hash, entries, verified, breaks, legacy_breaks)
			values (current_setting('depot.tenant_id')::uuid, '` + tomorrow + `', 1, repeat('a', 64), 1, true, 0, 0)`: "42501",
		`select audit_anchor_record('` + tomorrow + `'::date, 0, 0, true, 0, null, 0)`:                     "22023",
		`select audit_anchor_record((now() at time zone 'utc')::date - 2, 0, 0, true, 0, null, 0)`:         "22023",
		`select audit_anchor_record((now() at time zone 'utc')::date - 1, 999999999, 1, true, 0, null, 0)`: "22023",
	} {
		if got := code(asApp(stmt)); got != want {
			t.Errorf("%s = %s, want %s", stmt, got, want)
		}
	}
	// What the function writes for yesterday is the chain's own hash at the named seq, whatever the caller believes.
	if err := asApp(`select audit_anchor_record((now() at time zone 'utc')::date - 1, $1, 1, false, 0, null, 0)`, a.HeadSeq); err != nil {
		t.Fatal(err)
	}
	if chain, err := s.VerifyChanges(ctx, 0, 0); err != nil || !chain.Verified || chain.Anchors.Checked != 1 {
		t.Fatalf("anchors written by the function verify: %+v %v", chain, err)
	}
}

// TestContextsAreTenantScopedWindowedAndPurged (review finding 3): a depot.ctx message is found at or after its
// position, within cdc.ContextWindow of the change's commit, for its own tenant only, and purged by age.
func TestContextsAreTenantScopedWindowedAndPurged(t *testing.T) {
	_, app, s := migratedWithApp(t)
	ctx := scoped(1, "")
	committed := time.Date(2026, 11, 3, 9, 15, 0, 0, time.UTC)
	msg := cdc.TxContext{Source: "party", TxID: 7, LSN: 70, Verified: true, CommittedAt: committed}
	msg.Actor, msg.ActorKind, msg.Reason = "ana", "person", "why"
	if err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error { return s.RecordTxContext(ctx, tx, msg) }); err != nil {
		t.Fatal(err)
	}
	find := func(ctx context.Context, lsn int64, at time.Time) bool {
		var found bool
		if err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			var err error
			var got cdc.TxContext
			got, found, err = s.FindTxContext(ctx, tx, "party", 7, lsn, at)
			if found && (!got.Verified || got.Actor != "ana") {
				t.Errorf("found %+v", got)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return found
	}
	if !find(ctx, 80, committed) || find(ctx, 60, committed) || find(scoped(2, ""), 80, committed) {
		t.Fatal("a message is found at or after its position, for its own tenant only")
	}
	if find(ctx, 80, committed.Add(cdc.ContextWindow+time.Minute)) || find(ctx, 80, committed.Add(-cdc.ContextWindow-time.Minute)) {
		t.Fatal("a message committed outside the window of the change (a reused transaction id) is not found")
	}
	if n, err := s.PurgeTxContexts(ctx, time.Now().Add(time.Hour)); err != nil || n != 1 || find(ctx, 80, committed) {
		t.Fatalf("purge %d %v", n, err)
	}
}
