package store_test

import (
	"context"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/audit/internal/store"
	"github.com/BenjaminSRussell/depot/services/audit/migrations"
)

// TestAnchorRecordReadsTheWholeChainAsANonSuperuserOwner (second re-review R2-1): deployed, the migrations and so
// audit_anchor_record run as the tables' owner, which is not a superuser, so forced row-level security binds the
// function too. A caller with the legal-entity scope ” must still anchor the real head, a row of another legal
// entity, and get its own scope back.
func TestAnchorRecordReadsTheWholeChainAsANonSuperuserOwner(t *testing.T) {
	base, appURL := os.Getenv(testkit.EnvURL), os.Getenv(testkit.EnvAppURL)
	if base == "" || appURL == "" {
		t.Skip("no test database")
	}
	admin := testkit.DB(t)
	ctx := context.Background()
	role := "audit_owner_" + strings.ReplaceAll(ids.New(), "-", "")[:12]
	schema := "o_" + role
	for _, stmt := range []string{
		`create role ` + role + ` login password 'owner-pw' nosuperuser nobypassrls`,
		`create schema ` + schema + ` authorization ` + role,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `drop schema `+schema+` cascade`)
		_, _ = admin.Exec(context.Background(), `drop owned by `+role)
		_, _ = admin.Exec(context.Background(), `drop role `+role)
	})
	pool := func(raw, user, password string) *pgxpool.Pool {
		t.Helper()
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if user != "" {
			u.User = url.UserPassword(user, password)
		}
		cfg, err := pgxpool.ParseConfig(u.String())
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
		p, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	// Over the app URL's host (password authentication), as another login role.
	owner := pool(appURL, role, "owner-pw")
	// The owner runs every migration but the publication: a publication is database-wide, so in the shared test
	// database another test's superuser already created depot_outbox_audit and only its owner may alter it. Deployed,
	// the migrating role creates and owns it (the print service's owner test does the same).
	all := fstest.MapFS{}
	names, _ := fs.Glob(migrations.FS, "*.sql")
	for _, n := range names {
		if n != "0007_cdc_publication.sql" {
			b, _ := fs.ReadFile(migrations.FS, n)
			all[n] = &fstest.MapFile{Data: b}
		}
	}
	if err := dbx.Migrate(ctx, owner, all); err != nil {
		t.Fatalf("migrations as the owner: %v", err)
	}
	app := pool(appURL, "", "")
	s := store.New(app)
	writer := scoped(1, "")
	at := time.Now()
	for i, le := range []string{leA, "", leB} {
		if err := s.Apply(writer, placed("b-1", le, at.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	var seqs []int64
	rows, err := admin.Query(ctx, `select seq from `+schema+`.audit_entry order by seq`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, seq)
	}
	rows.Close()
	if len(seqs) != 3 {
		t.Fatalf("seqs %v", seqs)
	}
	call := func(headSeq int64) (bool, string, error) {
		var created bool
		var after string
		err := pgx.BeginFunc(ctx, app, func(tx pgx.Tx) error {
			// The caller's own scope sees no legal entity's rows; only the NULL one (seq 2) would look like the head.
			if _, err := tx.Exec(ctx, `select set_config('depot.tenant_id', $1, true), set_config('depot.legal_entity_scopes', '', true)`, testkit.TenantID(1)); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `select audit_anchor_record((now() at time zone 'utc')::date, $1, 3, true, 0, null, 0)`, headSeq).Scan(&created); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `select current_setting('depot.legal_entity_scopes', true)`).Scan(&after)
		})
		return created, after, err
	}
	var pgErr *pgconn.PgError
	if _, _, err := call(seqs[1]); !errors.As(err, &pgErr) || pgErr.Code != "40001" {
		t.Fatalf("anchoring seq %d (not the head) under scope '' = %v; want 40001", seqs[1], err)
	}
	created, after, err := call(seqs[2])
	if err != nil || !created || after != "" {
		t.Fatalf("anchoring the real head: created %v, scope after %q, %v", created, after, err)
	}
	var headSeq int64
	var headHash, realHash string
	if err := admin.QueryRow(ctx, `select a.head_seq, a.head_hash, e.hash from `+schema+`.audit_anchor a join `+schema+`.audit_entry e
		on e.tenant_id = a.tenant_id and e.seq = a.head_seq`).Scan(&headSeq, &headHash, &realHash); err != nil {
		t.Fatal(err)
	}
	if headSeq != seqs[2] || headHash != realHash {
		t.Fatalf("anchor names seq %d, want the real head %d", headSeq, seqs[2])
	}
}
