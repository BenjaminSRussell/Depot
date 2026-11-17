package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
	"github.com/BenjaminSRussell/depot/services/audit/internal/store"
	"github.com/BenjaminSRussell/depot/services/audit/migrations"
)

// TestActorAndContextAreStoredAndChained (AUD-API-1, migration 0005): the actor kind, the person a service recorded
// the change for and the change context are stored, returned and covered by the hash; a row written before the
// migration (no actor kind) and a new row form one chain that verifies.
func TestActorAndContextAreStoredAndChained(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	legacy := domain.Change{Entity: "party.vendor", RecordID: "v-1", Field: "name", OldValue: "Acme", NewValue: "Acme Ltd",
		UserID: "user-1", TraceID: "trace-0", OccurredAt: at}
	legacyHash := domain.Hash(domain.Genesis, legacy)
	if err := dbx.WithTenantTx(ctx, admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into audit_entry (tenant_id, id, entity, record_id, field, old_value, new_value, user_id, trace_id, occurred_at, prev_hash, hash)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, testkit.TenantID(1), ids.New(), legacy.Entity, legacy.RecordID, legacy.Field,
			legacy.OldValue, legacy.NewValue, legacy.UserID, legacy.TraceID, legacy.OccurredAt, domain.Genesis, legacyHash)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	s := store.New(app)
	change := domain.Change{Entity: "party.vendor", RecordID: "v-1", Field: "bankAccount", OldValue: "DE01", NewValue: "DE02",
		UserID: "service:billing", ActorKind: domain.ActorService, OnBehalfOf: "ana", TraceID: "trace-1", OccurredAt: at.Add(time.Minute),
		Reason: "vendor letter 12", ChangeSetID: "cs-1", EventType: "dev.depot.party.vendor.updated.v1", EventID: "evt-1", WorkflowInstanceID: "wf-1"}
	if err := s.Apply(ctx, change); err != nil {
		t.Fatal(err)
	}
	got, err := s.Query(ctx, "party.vendor", "v-1")
	if err != nil || len(got) != 2 {
		t.Fatalf("query %+v %v", got, err)
	}
	if got[0].ActorKind != "" || got[0].Hash != legacyHash {
		t.Fatalf("legacy row %+v", got[0])
	}
	rec := got[1]
	if rec.UserID != "service:billing" || rec.ActorKind != domain.ActorService || rec.OnBehalfOf != "ana" || rec.Reason != "vendor letter 12" ||
		rec.ChangeSetID != "cs-1" || rec.EventType != "dev.depot.party.vendor.updated.v1" || rec.EventID != "evt-1" || rec.WorkflowInstanceID != "wf-1" {
		t.Fatalf("record %+v", rec)
	}
	if n := verifyStoredChain(t, ctx, app); n != 2 {
		t.Fatalf("chain across the migration has %d rows", n)
	}

	// The database refuses what the domain refuses, whoever writes the row.
	for name, stmt := range map[string]string{
		"unknown actor kind":      `insert into audit_entry (tenant_id, id, entity, record_id, field, user_id, trace_id, occurred_at, prev_hash, hash, actor_kind) values ($1, $2, 'e', 'r', 'f', 'u', 't', now(), repeat('0', 64), repeat('1', 64), 'robot')`,
		"person on behalf of one": `insert into audit_entry (tenant_id, id, entity, record_id, field, user_id, trace_id, occurred_at, prev_hash, hash, actor_kind, on_behalf_of) values ($1, $2, 'e', 'r', 'f', 'u', 't', now(), repeat('0', 64), repeat('2', 64), 'person', 'ana')`,
		"service as a person id":  `insert into audit_entry (tenant_id, id, entity, record_id, field, user_id, trace_id, occurred_at, prev_hash, hash, actor_kind) values ($1, $2, 'e', 'r', 'f', 'ana', 't', now(), repeat('0', 64), repeat('3', 64), 'service')`,
		"person as a service id":  `insert into audit_entry (tenant_id, id, entity, record_id, field, user_id, trace_id, occurred_at, prev_hash, hash, actor_kind) values ($1, $2, 'e', 'r', 'f', 'service:x', 't', now(), repeat('0', 64), repeat('4', 64), 'person')`,
		"reason too long":         `insert into audit_entry (tenant_id, id, entity, record_id, field, user_id, trace_id, occurred_at, prev_hash, hash, actor_kind, reason) values ($1, $2, 'e', 'r', 'f', 'service:x', 't', now(), repeat('0', 64), repeat('5', 64), 'service', repeat('é', 1001))`,
	} {
		err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt, testkit.TenantID(1), ids.New())
			return err
		})
		if err == nil {
			t.Errorf("%s: insert accepted", name)
		}
	}
}

// verifyStoredChain reads the tenant's chain from the database in seq order, prev_hash and all, checks that each row
// links to the one before and that every hash recomputes from the stored values, and returns the row count.
func verifyStoredChain(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var entries []domain.Entry
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select entity, record_id, field, old_value, new_value, user_id, actor_kind, on_behalf_of, trace_id, occurred_at,
			reason, change_set_id, event_type, event_id, workflow_instance_id, prev_hash, hash from audit_entry order by seq`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e domain.Entry
			if err := rows.Scan(&e.Entity, &e.RecordID, &e.Field, &e.OldValue, &e.NewValue, &e.UserID, &e.ActorKind, &e.OnBehalfOf, &e.TraceID, &e.OccurredAt,
				&e.Reason, &e.ChangeSetID, &e.EventType, &e.EventID, &e.WorkflowInstanceID, &e.PrevHash, &e.Hash); err != nil {
				return err
			}
			entries = append(entries, e)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := domain.Verify(entries); err != nil {
		t.Fatalf("stored chain: %v", err)
	}
	return len(entries)
}

// TestConcurrentAppliesDoNotFork: appends are serialised per tenant, so concurrent changes form one linear chain
// (under READ COMMITTED without the lock, two transactions read the same head and both link to it).
func TestConcurrentAppliesDoNotFork(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	const writers, each = 2, 10
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				errs <- s.Apply(ctx, domain.Change{Entity: "party.vendor", RecordID: "v-1", Field: "name", NewValue: fmt.Sprintf("w%d-%d", w, i),
					UserID: "service:billing", ActorKind: domain.ActorService, OnBehalfOf: "ana", TraceID: "trace", OccurredAt: time.Now()})
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := verifyStoredChain(t, ctx, app); n != writers*each {
		t.Fatalf("chain has %d rows; want %d", n, writers*each)
	}
	var heads int
	if err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from audit_entry a where not exists (select 1 from audit_entry b where b.prev_hash = a.hash)`).Scan(&heads)
	}); err != nil || heads != 1 {
		t.Fatalf("chain heads %d (%v); a linear chain has one", heads, err)
	}
}

// TestNanosecondTimesReverifyFromTheDatabase: timestamptz keeps microseconds, so a change sent with nanoseconds is
// hashed over the time the database stores, and its hash recomputes from the row.
func TestNanosecondTimesReverifyFromTheDatabase(t *testing.T) {
	s, ctx := migrated(t)
	at := time.Date(2026, 11, 1, 10, 0, 0, 123456789, time.FixedZone("CET", 3600))
	if err := s.Apply(ctx, domain.Change{Entity: "party.vendor", RecordID: "v-1", Field: "name", NewValue: "Acme",
		UserID: "service:billing", ActorKind: domain.ActorService, TraceID: "trace", OccurredAt: at}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Query(ctx, "party.vendor", "v-1")
	if err != nil || len(got) != 1 {
		t.Fatalf("query %+v %v", got, err)
	}
	r := got[0]
	stored := domain.Change{Entity: r.Entity, RecordID: r.RecordID, Field: r.Field, OldValue: r.OldValue, NewValue: r.NewValue, UserID: r.UserID,
		ActorKind: r.ActorKind, OnBehalfOf: r.OnBehalfOf, TraceID: r.TraceID, OccurredAt: r.OccurredAt}
	if !r.OccurredAt.Equal(at.Truncate(time.Microsecond)) || domain.Hash(domain.Genesis, stored) != r.Hash {
		t.Fatalf("stored %s hash %s; recomputed %s", r.OccurredAt, r.Hash, domain.Hash(domain.Genesis, stored))
	}
}
