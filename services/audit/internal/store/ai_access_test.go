package store_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
	"github.com/BenjaminSRussell/depot/services/audit/internal/store"
)

func aiStore(t *testing.T) (*pgxpool.Pool, *store.Postgres, context.Context) {
	t.Helper()
	admin, _, s := migratedWithApp(t)
	return admin, s, tenant.With(context.Background(), principal(1))
}

func aiAccess(ids ...string) domain.AIAccess {
	classes := make([]string, len(ids))
	for i := range classes {
		classes[i] = "NONE"
	}
	return domain.AIAccess{
		Kind: domain.AIAccessKindRead, Actor: "agent:copilot@user:user-1", UserID: "user-1", AgentID: "copilot", RunID: "run-1",
		Tool: "depot.ap.invoice.list", EntityType: "ap.invoice", RecordIDs: ids, RecordClasses: classes, HighestClass: "P1",
		RouteID: "route-1", RedactionCounts: map[string]int{"email": 1}, PolicyVersion: 2, Decision: "ALLOWED", TraceID: "run-1",
		OccurredAt: time.Now().UTC(),
	}
}

func TestAIAccessChainsPerTenantAndHoldsNoValues(t *testing.T) {
	admin, s, ctx := aiStore(t)
	first, err := s.RecordAIAccess(ctx, aiAccess("inv-1", "inv-2"))
	if err != nil || first.PrevHash != domain.Genesis || first.Seq == 0 {
		t.Fatalf("first %+v %v", first, err)
	}
	second, err := s.RecordAIAccess(ctx, aiAccess("inv-3"))
	if err != nil || second.PrevHash != first.Hash {
		t.Fatalf("second %+v %v; want it linked to %s", second, err, first.Hash)
	}
	other := tenant.With(context.Background(), principal(2))
	if got, err := s.RecordAIAccess(other, aiAccess("inv-1")); err != nil || got.PrevHash != domain.Genesis {
		t.Fatalf("tenant 2 has its own chain from Genesis: %+v %v", got, err)
	}
	var row string
	err = dbx.WithTenantTx(ctx, admin, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select string_agg(row_to_json(a)::text, ' ') from ai_data_access a`).Scan(&row)
	})
	if err != nil || !strings.Contains(row, "inv-1") || strings.Contains(row, "Acme") {
		t.Fatalf("rows %s %v", row, err)
	}
}

func TestAIAccessConcurrentAppendsDoNotFork(t *testing.T) {
	_, s, ctx := aiStore(t)
	var wg sync.WaitGroup
	hashes := make(chan [2]string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec, err := s.RecordAIAccess(ctx, aiAccess(fmt.Sprintf("inv-%d", i)))
			if err != nil {
				t.Error(err)
				return
			}
			hashes <- [2]string{rec.PrevHash, rec.Hash}
		}()
	}
	wg.Wait()
	close(hashes)
	prevs := map[string]bool{}
	for h := range hashes {
		if prevs[h[0]] {
			t.Fatalf("two appends linked to %s", h[0])
		}
		prevs[h[0]] = true
	}
}

func TestAIAccessRejectsRewriteAndOversizedRows(t *testing.T) {
	admin, s, ctx := aiStore(t)
	if _, err := s.RecordAIAccess(ctx, aiAccess("inv-1")); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`update ai_data_access set tool = 'x'`,
		`delete from ai_data_access`,
		`truncate ai_data_access`,
	} {
		err := dbx.WithTenantTx(ctx, admin, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "ai_data_access is append-only") {
			t.Errorf("%q = %v; want append-only", stmt, err)
		}
	}
	tooMany := make([]string, 201)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("i%d", i)
	}
	if _, err := s.RecordAIAccess(ctx, aiAccess(tooMany...)); err == nil {
		t.Fatal("201 ids must be refused")
	}
	err := dbx.WithTenantTx(ctx, admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into ai_data_access (tenant_id, id, kind, actor, user_id, run_id, tool, entity_type, record_ids,
			record_classes, highest_class, route_id, policy_version, decision, trace_id, occurred_at, prev_hash, hash)
			values ($1, gen_random_uuid(), 'READ', 'a', 'u', 'r', 't', 'e', array['what is the total'], array['NONE'], 'P0', 'r', 1, 'ALLOWED', 't', now(), repeat('0',64), repeat('1',64))`,
			principal(1).TenantID)
		return err
	})
	if err == nil {
		t.Fatal("an id with spaces must be refused by the table")
	}
}
