// Package testkit gives integration tests a Postgres pool scoped to a fresh
// schema. Tests call DB(t) and are skipped when PASSAGE_TEST_DATABASE_URL is
// unset, so `go test ./...` always passes on a laptop without Postgres.
package testkit

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// EnvURL is the environment variable that enables database tests.
const EnvURL = "PASSAGE_TEST_DATABASE_URL"

// EnvAppURL is the application-role database URL used by RLS integration tests.
const EnvAppURL = "PASSAGE_TEST_APP_DATABASE_URL"

// sharedExtensions runs sharedExtensionsSQL once per test binary.
var sharedExtensions sync.Once

// sharedExtensionsSQL puts btree_gist in public before any migration runs. Some
// services' first migration (hr, identity, scheduling) creates it unqualified,
// which lands in the first schema of the search_path: under DB that is the
// test's throwaway schema, and dropping that schema on cleanup drops the
// extension under every test running in parallel ("referenced operator class
// was concurrently dropped"). With the extension already in public, those
// migrations' IF NOT EXISTS is a no-op. An extension a crashed run left in a
// throwaway schema is moved to public. Concurrent test binaries race on
// pg_extension's unique index; the loser finds the winner's extension.
const sharedExtensionsSQL = `DO $$
DECLARE
  ext_schema name;
BEGIN
  SELECT n.nspname INTO ext_schema
    FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
   WHERE e.extname = 'btree_gist';
  IF ext_schema IS NULL THEN
    BEGIN
      CREATE EXTENSION IF NOT EXISTS btree_gist WITH SCHEMA public;
    EXCEPTION WHEN unique_violation OR duplicate_object THEN
      NULL;
    END;
  ELSIF ext_schema <> 'public' THEN
    BEGIN
      ALTER EXTENSION btree_gist SET SCHEMA public;
    EXCEPTION WHEN insufficient_privilege OR duplicate_object THEN
      NULL;
    END;
  END IF;
END
$$`

// DB returns a pool whose search_path is a throwaway schema, dropped on cleanup.
func DB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(EnvURL)
	if url == "" {
		t.Skipf("%s not set; skipping database test", EnvURL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := "t_" + strings.ReplaceAll(ids.New(), "-", "")
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	sharedExtensions.Do(func() {
		if _, err := admin.Exec(ctx, sharedExtensionsSQL); err != nil {
			t.Logf("testkit: shared extensions not ensured in public: %v", err)
		}
	})
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	cfg, _ := pgxpool.ParseConfig(url)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "drop schema "+schema+" cascade")
		admin.Close()
	})
	return pool
}

// DBWithApp returns admin and application-role pools sharing a throwaway schema.
// The admin pool applies migrations; assertions and application queries use app.
func DBWithApp(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	admin := DB(t)
	appURL := os.Getenv(EnvAppURL)
	if appURL == "" {
		t.Fatalf("%s must be set for an RLS database test", EnvAppURL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var schema string
	if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	appConfig, err := pgxpool.ParseConfig(appURL)
	if err != nil {
		t.Fatal(err)
	}
	appConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	app, err := pgxpool.NewWithConfig(ctx, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		app.Close()
	})
	return admin, app
}

// Ctx returns a context carrying a test principal for tenantID with the given duties.
func Ctx(tenantID string, duties ...string) context.Context {
	p := tenant.Principal{TenantID: tenantID, LegalEntityID: "00000000-0000-0000-0000-0000000000e1", UserID: "user-test", Duties: map[string]struct{}{}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return tenant.With(context.Background(), p)
}

// ScopeDuties returns the duties for a test principal in legalEntity: the
// all-entity and tenant-admin duties when legalEntity is empty, none otherwise.
func ScopeDuties(legalEntity string) map[string]struct{} {
	d := map[string]struct{}{}
	if legalEntity == "" {
		d[tenant.DutyAllScope] = struct{}{}
		d[tenant.DutyTenantAdmin] = struct{}{}
	}
	return d
}

// TenantID returns a deterministic UUID for tests: 00000000-0000-0000-0000-00000000000N.
func TenantID(n int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012d", n) }
