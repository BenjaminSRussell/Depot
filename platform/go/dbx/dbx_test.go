package dbx_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
)

func principal(legalEntity string, duties ...string) tenant.Principal {
	p := tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: legalEntity, Duties: map[string]struct{}{}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return p
}

func TestLegalEntityScopes(t *testing.T) {
	leA, leB := testkit.TenantID(11), testkit.TenantID(12)
	cases := []struct {
		name string
		p    tenant.Principal
		want string
	}{
		{"single entity", principal(leA), leA},
		{"entity list", principal(leA + "," + leB), leA + "," + leB},
		{"entity wins over all-scope duty", principal(leA, tenant.DutyAllScope), leA},
		{"wildcard entity dropped", principal("*", tenant.DutyAllScope), ""},
		{"spaces trimmed and case folded", principal(" " + strings.ToUpper(leA) + " , " + leB), leA + "," + leB},
		{"wildcard in list dropped", principal(leA + ",*"), leA},
		{"all keyword dropped", principal("all", tenant.DutyAllScope), ""},
		{"no entity, no duty fails closed", principal(""), ""},
		{"no entity, unrelated duty fails closed", principal("", "org.read"), ""},
		{"no entity with all-scope duty", principal("", tenant.DutyAllScope), "*"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dbx.LegalEntityScopes(c.p); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestWithTenantTxLegalEntityVisibility(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	leA, leB := testkit.TenantID(11), testkit.TenantID(12)
	var schema string
	if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	ddl := `grant usage on schema ` + schema + ` to depot_app;create table scoped (tenant_id uuid not null, legal_entity_id uuid not null);` +
		dbx.RLSPolicy("scoped") + dbx.LegalEntityScopePolicy("scoped") +
		`grant select, insert on scoped to depot_app;`
	if _, err := admin.Exec(ctx, ddl); err != nil {
		t.Fatal(err)
	}
	for _, le := range []string{leA, leB} {
		if _, err := admin.Exec(ctx, `insert into scoped values ($1, $2)`, testkit.TenantID(1), le); err != nil {
			t.Fatal(err)
		}
	}
	count := func(p tenant.Principal) int {
		var n int
		err := dbx.WithTenantTx(tenant.With(ctx, p), app, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `select count(*) from scoped`).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count(principal("")); got != 0 {
		t.Fatalf("no-entity principal saw %d rows, want 0", got)
	}
	if got := count(principal("", tenant.DutyAllScope)); got != 2 {
		t.Fatalf("all-scope principal saw %d rows, want 2", got)
	}
	if got := count(principal(leA)); got != 1 {
		t.Fatalf("single-entity principal saw %d rows, want 1", got)
	}
}

func TestTenantWidePredicate(t *testing.T) {
	const duty = "x.maintain"
	holds := map[string]struct{}{duty: {}, tenant.DutyTenantAdmin: {}}
	cases := []struct {
		name string
		p    tenant.Principal
		want bool
	}{
		{"no claim, duty and tenant.administer", tenant.Principal{Duties: holds}, true},
		{"no claim and duty without tenant.administer", tenant.Principal{Duties: map[string]struct{}{duty: {}}}, false},
		{"no claim and tenant.administer without duty", tenant.Principal{Duties: map[string]struct{}{tenant.DutyTenantAdmin: {}}}, false},
		{"claim and duty", tenant.Principal{LegalEntityID: testkit.TenantID(9), Duties: holds}, false},
		{"no claim and no duty", tenant.Principal{}, false},
		{"other duty only", tenant.Principal{Duties: map[string]struct{}{"y": {}}}, false},
	}
	for _, c := range cases {
		if got := dbx.TenantWide(c.p, duty); got != c.want {
			t.Errorf("%s: TenantWide = %v, want %v", c.name, got, c.want)
		}
		if c.want {
			continue
		}
		if err := dbx.WithTenantWideAdminTx(context.Background(), nil, c.p, duty, func(pgx.Tx) error { return nil }); !errors.Is(err, dbx.ErrNotTenantWide) {
			t.Errorf("%s: err = %v, want ErrNotTenantWide", c.name, err)
		}
	}
}

func TestWithTenantTxReleasesTheConnectionWhenTheClosurePanics(t *testing.T) {
	_, app := testkit.DBWithApp(t)
	cfg := app.Config().Copy()
	cfg.MaxConns = 1
	ctx := context.Background()
	single, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer single.Close()

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic must reach the caller")
			}
		}()
		_ = dbx.WithTenantTx(tenant.With(ctx, principal("")), single, func(pgx.Tx) error { panic("closure failed") })
	}()

	timeout, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var one int
	if err := single.QueryRow(timeout, `select 1`).Scan(&one); err != nil {
		t.Fatalf("the only pooled connection was not released after a panic: %v", err)
	}
	if n := single.Stat().AcquiredConns(); n != 0 {
		t.Fatalf("%d connections still acquired", n)
	}
}

// Under a profile that requires verified TLS, Connect refuses a weaker sslmode before it dials (#602).
func TestConnectRefusesWeakTLSUnderAGovernmentProfile(t *testing.T) {
	t.Setenv("DEPOT_CLOUD_PROFILE", "gcc-high")
	_, err := dbx.Connect(context.Background(), "postgres://u:p@127.0.0.1:1/x?sslmode=disable")
	if err == nil || !strings.Contains(err.Error(), "verify-full") {
		t.Fatalf("Connect err = %v, want a verify-full refusal", err)
	}
	t.Setenv("DEPOT_CLOUD_PROFILE", "no-such-profile")
	if _, err := dbx.Connect(context.Background(), "postgres://u:p@127.0.0.1:1/x"); err == nil || !strings.Contains(err.Error(), "unknown cloud profile") {
		t.Fatalf("a typo in the profile name must fail closed, got %v", err)
	}
}

// Two replicas that start together with migrate-on-start both succeed, and each migration runs once (#213): Migrate holds an
// advisory lock, so the second waits and then finds every file recorded.
func TestConcurrentMigrateAppliesEachMigrationOnce(t *testing.T) {
	first := testkit.DB(t)
	ctx := context.Background()
	second, err := pgxpool.NewWithConfig(ctx, first.Config().Copy()) // same throwaway schema, another "pod"
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	migrations := fstest.MapFS{
		// Not idempotent: a second run of any file fails (the table exists) or shows up as an extra row.
		"0001_create.sql": {Data: []byte("create table mig_runs (file text not null);\ninsert into mig_runs values ('0001');\n")},
		"0002_slow.sql":   {Data: []byte("select pg_sleep(0.3);\ninsert into mig_runs values ('0002');\n")},
		"0003_notx.sql":   {Data: []byte(dbx.NoTransactionMarker + "\ncreate table mig_notx (id int);\ninsert into mig_runs values ('0003');\n")},
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, pool := range []*pgxpool.Pool{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = dbx.Migrate(ctx, pool, migrations)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("replica %d: %v", i, err)
		}
	}
	rows, err := first.Query(ctx, `select file, count(*) from mig_runs group by file order by file`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (string, error) {
		var file string
		var n int
		err := r.Scan(&file, &n)
		return fmt.Sprintf("%s x%d", file, n), err
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "0001 x1,0002 x1,0003 x1" {
		t.Fatalf("migration runs = %v, want each file once", got)
	}
	var recorded int
	if err := first.QueryRow(ctx, `select count(*) from schema_migrations`).Scan(&recorded); err != nil || recorded != 3 {
		t.Fatalf("schema_migrations rows = %d (%v), want 3", recorded, err)
	}
	// The lock is released: a later run takes it at once and applies nothing.
	timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := dbx.Migrate(timeout, second, migrations); err != nil {
		t.Fatalf("a later run: %v", err)
	}
}

// Replicas waiting for the migration lock must not deadlock the holder's CREATE INDEX CONCURRENTLY (a dbx:no-transaction
// file): a waiter blocked inside pg_advisory_lock holds a snapshot that the index build waits for, so Postgres aborted the build
// (40P01) and left an INVALID index. Waiters poll instead; every replica succeeds and the index is valid.
func TestConcurrentMigrateWithACreateIndexConcurrently(t *testing.T) {
	first := testkit.DB(t)
	ctx := context.Background()
	pools := []*pgxpool.Pool{first}
	for range 3 {
		p, err := pgxpool.NewWithConfig(ctx, first.Config().Copy())
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		pools = append(pools, p)
	}
	migrations := fstest.MapFS{
		"0001_big.sql": {Data: []byte("create table big (id int primary key, k int not null);\n" +
			"insert into big select g, g % 1000 from generate_series(1, 50000) g;\n")},
		"0002_index.sql": {Data: []byte(dbx.NoTransactionMarker + "\n" +
			"drop index concurrently if exists big_k_ix;\n" +
			"create index concurrently big_k_ix on big (k);\n")},
	}
	var wg sync.WaitGroup
	errs := make([]error, len(pools))
	for i, pool := range pools {
		wg.Add(1)
		go func() {
			defer wg.Done()
			timeout, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			errs[i] = dbx.Migrate(timeout, pool, migrations)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("replica %d: %v", i, err)
		}
	}
	var valid bool
	if err := first.QueryRow(ctx, `select indisvalid from pg_index where indexrelid = 'big_k_ix'::regclass`).Scan(&valid); err != nil || !valid {
		t.Fatalf("big_k_ix valid = %v (%v), want a valid index", valid, err)
	}
	var recorded int
	if err := first.QueryRow(ctx, `select count(*) from schema_migrations`).Scan(&recorded); err != nil || recorded != 2 {
		t.Fatalf("schema_migrations rows = %d (%v), want 2", recorded, err)
	}
}

// A replica waiting for the lock gives up when its context ends.
func TestMigrateWaitForTheLockHonoursTheContext(t *testing.T) {
	first := testkit.DB(t)
	ctx := context.Background()
	holder, err := first.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if _, err := holder.Exec(ctx, `select pg_advisory_lock(hashtextextended('depot.dbx.migrate:' || current_schema(), 0))`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = holder.Exec(ctx, `select pg_advisory_unlock(hashtextextended('depot.dbx.migrate:' || current_schema(), 0))`)
	}()
	timeout, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if err := dbx.Migrate(timeout, first, fstest.MapFS{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Migrate while another holds the lock: err = %v, want context.DeadlineExceeded", err)
	}
}
