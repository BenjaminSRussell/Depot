package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

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
	entries := make([]domain.Entry, len(got))
	prev := domain.Genesis
	for i, r := range got {
		c := domain.Change{Entity: r.Entity, RecordID: r.RecordID, Field: r.Field, OldValue: r.OldValue, NewValue: r.NewValue, UserID: r.UserID,
			ActorKind: r.ActorKind, OnBehalfOf: r.OnBehalfOf, TraceID: r.TraceID, OccurredAt: r.OccurredAt, Reason: r.Reason,
			ChangeSetID: r.ChangeSetID, EventType: r.EventType, EventID: r.EventID, WorkflowInstanceID: r.WorkflowInstanceID}
		entries[i] = domain.Entry{Change: c, PrevHash: prev, Hash: r.Hash}
		prev = r.Hash
	}
	if err := domain.Verify(entries); err != nil {
		t.Fatalf("chain across the migration: %v", err)
	}

	// The database refuses what the domain refuses, whoever writes the row.
	for name, stmt := range map[string]string{
		"unknown actor kind":      `insert into audit_entry (tenant_id, id, entity, record_id, field, user_id, trace_id, occurred_at, prev_hash, hash, actor_kind) values ($1, $2, 'e', 'r', 'f', 'u', 't', now(), repeat('0', 64), repeat('1', 64), 'robot')`,
		"person on behalf of one": `insert into audit_entry (tenant_id, id, entity, record_id, field, user_id, trace_id, occurred_at, prev_hash, hash, actor_kind, on_behalf_of) values ($1, $2, 'e', 'r', 'f', 'u', 't', now(), repeat('0', 64), repeat('2', 64), 'person', 'ana')`,
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
