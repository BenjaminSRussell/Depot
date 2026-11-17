package dbx_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/testkit"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestMigrateURLPrefersTheOwnerURL(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"owner url wins", map[string]string{"MIGRATE_DATABASE_URL": "postgres://owner", "DATABASE_URL": "postgres://app"}, "postgres://owner"},
		{"falls back to DATABASE_URL", map[string]string{"DATABASE_URL": "postgres://app"}, "postgres://app"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := dbx.MigrateURL(env(c.env))
			if err != nil || got != c.want {
				t.Fatalf("got %q, %v; want %q", got, err, c.want)
			}
		})
	}
	if _, err := dbx.MigrateURL(env(nil)); err == nil {
		t.Fatal("no URL set: want an error")
	}
}

func TestRunMigrateCommandLeavesOtherCommandsAlone(t *testing.T) {
	never := env(map[string]string{"DATABASE_URL": "postgres://unused.invalid/x"})
	for _, args := range [][]string{{"svc"}, {"svc", "consume"}, {"svc", "migrate-later"}, {"svc", "Migrate"}} {
		ran, err := dbx.RunMigrateCommand(context.Background(), args, never, fstest.MapFS{})
		if ran || err != nil {
			t.Fatalf("%q: ran=%v err=%v, want the service to start as before", args, ran, err)
		}
	}
}

func TestRunMigrateCommandRefusesExtraArgumentsAndAMissingURL(t *testing.T) {
	ran, err := dbx.RunMigrateCommand(context.Background(), []string{"svc", "migrate", "now"}, env(map[string]string{"DATABASE_URL": "postgres://x"}), fstest.MapFS{})
	if !ran || err == nil || !strings.Contains(err.Error(), "takes no arguments") {
		t.Fatalf("extra argument: ran=%v err=%v", ran, err)
	}
	ran, err = dbx.RunMigrateCommand(context.Background(), []string{"svc", "migrate"}, env(nil), fstest.MapFS{})
	if !ran || err == nil {
		t.Fatalf("no URL: ran=%v err=%v, want a failed migrate run", ran, err)
	}
}

// The command connects with MIGRATE_DATABASE_URL (not DATABASE_URL), applies the files once, runs the hooks on the owner
// pool, and a second run (a Job retry or the next sync) applies nothing.
func TestRunMigrateCommandAppliesMigrationsAsTheOwner(t *testing.T) {
	admin := testkit.DB(t)
	ctx := context.Background()
	var schema string
	if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(os.Getenv(testkit.EnvURL))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	getenv := env(map[string]string{"MIGRATE_DATABASE_URL": u.String(), "DATABASE_URL": "postgres://depot_app@unreachable.invalid/x"})
	files := fstest.MapFS{
		"0001_a.sql": {Data: []byte("create table widget (id int primary key);")},
		"0002_b.sql": {Data: []byte("insert into widget values (1);")},
	}
	hooks := 0
	hook := func(ctx context.Context, pool *pgxpool.Pool) error {
		hooks++
		_, err := pool.Exec(ctx, "insert into widget values ($1)", 100+hooks)
		return err
	}
	for range 2 {
		ran, err := dbx.RunMigrateCommand(ctx, []string{"svc", dbx.MigrateCommand}, getenv, files, hook)
		if !ran || err != nil {
			t.Fatalf("ran=%v err=%v", ran, err)
		}
	}
	var applied, rows int
	if err := admin.QueryRow(ctx, "select count(*) from schema_migrations").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, "select count(*) from widget").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if applied != 2 || rows != 3 || hooks != 2 {
		t.Fatalf("applied=%d rows=%d hooks=%d, want 2 files once, the seed row and two hook rows", applied, rows, hooks)
	}

	failing := func(context.Context, *pgxpool.Pool) error { return errors.New("seed failed") }
	if ran, err := dbx.RunMigrateCommand(ctx, []string{"svc", "migrate"}, getenv, files, failing); !ran || err == nil {
		t.Fatalf("failing hook: ran=%v err=%v, want the run to fail", ran, err)
	}

	// A migration's own SQL error fails at once: no retry, whatever MIGRATE_RETRY_TIMEOUT allows.
	broken := fstest.MapFS{"0001_a.sql": files["0001_a.sql"], "0002_b.sql": files["0002_b.sql"], "0003_c.sql": {Data: []byte("create tabel nope (id int);")}}
	retrying := func(k string) string {
		if k == "MIGRATE_RETRY_TIMEOUT" {
			return "2m"
		}
		return getenv(k)
	}
	start := time.Now()
	if _, err := dbx.RunMigrateCommand(ctx, []string{"svc", "migrate"}, retrying, broken); err == nil || dbx.IsConnectionError(err) {
		t.Fatalf("broken migration: err=%v, want its SQL error", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("a SQL error was retried for %v", elapsed)
	}
}

// The serving role is refused (its tables would belong to depot_app) unless MIGRATE_ALLOW_APP_ROLE=true.
func TestRunMigrateCommandRefusesTheAppRole(t *testing.T) {
	admin, _ := testkit.DBWithApp(t)
	ctx := context.Background()
	var schema string
	if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "grant usage, create on schema "+schema+" to depot_app"); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(os.Getenv(testkit.EnvAppURL))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	env := map[string]string{"MIGRATE_DATABASE_URL": u.String(), "MIGRATE_RETRY_TIMEOUT": "0"}
	getenv := func(k string) string { return env[k] }
	files := fstest.MapFS{"0001_a.sql": {Data: []byte("create table app_owned (id int);")}}
	ran, err := dbx.RunMigrateCommand(ctx, []string{"svc", "migrate"}, getenv, files)
	if !ran || err == nil || !strings.Contains(err.Error(), "refusing to migrate") {
		t.Fatalf("as depot_app: ran=%v err=%v, want a refusal", ran, err)
	}
	var tables int
	if err := admin.QueryRow(ctx, "select count(*) from pg_tables where schemaname = $1 and tablename = 'app_owned'", schema).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("the refused run created %d tables (%v)", tables, err)
	}
	env["MIGRATE_ALLOW_APP_ROLE"] = "true"
	if ran, err := dbx.RunMigrateCommand(ctx, []string{"svc", "migrate"}, getenv, files); !ran || err != nil {
		t.Fatalf("with MIGRATE_ALLOW_APP_ROLE: ran=%v err=%v", ran, err)
	}
}
