package dbx_test

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/testkit"
)

// The reverse bypasses are refused too: an owner role granted to depot_app (depot_app inherits the owner's rights), a
// depot_app login that switches to the owner with `options=-c role=<owner>`, and a configurable app role (MIGRATE_APP_ROLE).
func TestRunMigrateCommandRefusesAnOwnerTheAppRoleInherits(t *testing.T) {
	admin, _ := testkit.DBWithApp(t)
	ctx := context.Background()
	var schema string
	if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	owner := "b09_owner_" + strings.TrimPrefix(schema, "t_")[:12]
	if _, err := admin.Exec(ctx, "create role "+owner+" login password 'owner'; grant usage, create on schema "+schema+" to "+owner); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = admin.Exec(bg, "revoke "+owner+" from depot_app")
		_, _ = admin.Exec(bg, "drop owned by "+owner+" cascade")
		_, _ = admin.Exec(bg, "drop role if exists "+owner)
	})
	appURL, err := url.Parse(os.Getenv(testkit.EnvAppURL))
	if err != nil {
		t.Fatal(err)
	}
	withSchema := func(u url.URL, extra map[string]string) string {
		q := u.Query()
		q.Set("search_path", schema)
		for k, v := range extra {
			q.Set(k, v)
		}
		u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20") // pgx does not read '+' as a space
		return u.String()
	}
	ownerURL := *appURL
	ownerURL.User = url.UserPassword(owner, "owner")
	files := fstest.MapFS{"0001_a.sql": {Data: []byte("create table inherited (id int);")}}
	run := func(env map[string]string) error {
		env["MIGRATE_RETRY_TIMEOUT"] = "0"
		_, err := dbx.RunMigrateCommand(ctx, []string{"svc", "migrate"}, func(k string) string { return env[k] }, files)
		return err
	}
	refused := func(name string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "refusing to migrate") {
			t.Fatalf("%s: err=%v, want a refusal", name, err)
		}
	}

	// A plain owner role, nothing granted to depot_app: allowed (then undone, so the later cases start empty).
	if err := run(map[string]string{"MIGRATE_DATABASE_URL": withSchema(ownerURL, nil)}); err != nil {
		t.Fatalf("a plain owner role: %v", err)
	}
	if _, err := admin.Exec(ctx, "drop table "+schema+".inherited; drop table "+schema+".schema_migrations"); err != nil {
		t.Fatal(err)
	}
	// The owner granted to depot_app: refused, logged in as the owner or as depot_app switching to it.
	if _, err := admin.Exec(ctx, "grant "+owner+" to depot_app"); err != nil {
		t.Fatal(err)
	}
	refused("owner login", run(map[string]string{"MIGRATE_DATABASE_URL": withSchema(ownerURL, nil)}))
	refused("depot_app as the owner", run(map[string]string{"MIGRATE_DATABASE_URL": withSchema(*appURL, map[string]string{"options": "-c role=" + owner})}))
	if _, err := admin.Exec(ctx, "revoke "+owner+" from depot_app"); err != nil {
		t.Fatal(err)
	}
	// MIGRATE_APP_ROLE names the app role: the owner itself is refused when it is named.
	refused("MIGRATE_APP_ROLE", run(map[string]string{"MIGRATE_DATABASE_URL": withSchema(ownerURL, nil), "MIGRATE_APP_ROLE": owner}))
	var tables int
	if err := admin.QueryRow(ctx, "select count(*) from pg_tables where schemaname = $1 and tablename = 'inherited'", schema).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("a refused run created %d tables (%v)", tables, err)
	}
}

// A TLS certificate that does not verify fails at once, whatever MIGRATE_RETRY_TIMEOUT allows.
func TestRunMigrateCommandDoesNotRetryATLSFailure(t *testing.T) {
	raw := os.Getenv(testkit.EnvAppURL)
	if raw == "" {
		t.Skipf("%s not set", testkit.EnvAppURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	// The system bundle does not hold the test server's certificate (self-signed, or none), so verify-full cannot pass.
	u.RawQuery = url.Values{"sslmode": {"verify-full"}, "sslrootcert": {"/etc/ssl/certs/ca-certificates.crt"}}.Encode()
	env := map[string]string{"MIGRATE_DATABASE_URL": u.String(), "MIGRATE_RETRY_TIMEOUT": "2m"}
	start := time.Now()
	_, err = dbx.RunMigrateCommand(context.Background(), []string{"svc", "migrate"}, func(k string) string { return env[k] }, fstest.MapFS{})
	if err == nil || !dbx.IsTLSError(err) || dbx.IsConnectionError(err) {
		t.Fatalf("err=%v, want a TLS error that is not retried", err)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("a TLS failure was retried for %v", elapsed)
	}
}
