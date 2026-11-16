package provision

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

const tenantA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
const tenantB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

func needDB(t *testing.T) (ownerURL, appURL string) {
	t.Helper()
	ownerURL, appURL = os.Getenv(testkit.EnvURL), os.Getenv(testkit.EnvAppURL)
	if ownerURL == "" || appURL == "" {
		t.Skip("PASSAGE_TEST_DATABASE_URL and PASSAGE_TEST_APP_DATABASE_URL are not set")
	}
	return
}

func exec(t *testing.T, url, sql string) {
	t.Helper()
	c, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(context.Background()) }()
	if _, err := c.Exec(context.Background(), sql); err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
}

func count(t *testing.T, url, q string) int {
	t.Helper()
	c, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(context.Background()) }()
	var n int
	if err := c.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// withUser swaps the user of a connection URL.
func withUser(t *testing.T, raw, user, pass string) string {
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(user, pass)
	return u.String()
}

// purgeRole is a login role that may delete (the purge credentials) and is not passage_app and has no BYPASSRLS.
func purgeRole(t *testing.T, ownerURL, appURL string) string {
	t.Helper()
	exec(t, ownerURL, `do $$ begin if not exists (select 1 from pg_roles where rolname = 'provtest_purger') then
		create role provtest_purger login password 'purge-pw' nosuperuser nobypassrls; end if; end $$`)
	return withUser(t, appURL, "provtest_purger", "purge-pw")
}

func dbURL(raw, db string) string {
	u, _ := url.Parse(raw)
	u.Path = "/" + db
	return u.String()
}

// shapedSchema builds a schema shaped like a service's as the owner, with forced RLS for the tenant policy, and grants it to the
// purge role.
func shapedSchema(t *testing.T, ownerURL, schema string, extra string) {
	t.Helper()
	pol := func(tbl string) string {
		return `alter table ` + schema + `.` + tbl + ` enable row level security; alter table ` + schema + `.` + tbl + ` force row level security;
		create policy p on ` + schema + `.` + tbl + ` using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);`
	}
	exec(t, ownerURL, `drop schema if exists `+schema+` cascade; create schema `+schema+`;
	create table `+schema+`.parent (tenant_id uuid not null, id int not null, primary key (tenant_id, id));
	create table `+schema+`.child (tenant_id uuid not null, id int not null, parent_id int not null, primary key (tenant_id, id),
	  foreign key (tenant_id, parent_id) references `+schema+`.parent (tenant_id, id));
	create table `+schema+`.identity_tenants (tenant_id uuid primary key);
	insert into `+schema+`.parent values ('`+tenantA+`', 1), ('`+tenantB+`', 1);
	insert into `+schema+`.child values ('`+tenantA+`', 1, 1), ('`+tenantB+`', 1, 1);
	insert into `+schema+`.identity_tenants values ('`+tenantA+`');`+pol("parent")+pol("child")+pol("identity_tenants")+extra+`
	grant usage on schema `+schema+` to passage_app, provtest_purger;
	grant select, insert, update, delete on all tables in schema `+schema+` to passage_app, provtest_purger;`)
	t.Cleanup(func() { exec(t, ownerURL, "drop schema if exists "+schema+" cascade") })
}

func schemaName(t *testing.T) string {
	n := "provtest_" + strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name()))
	if len(n) > 60 { // Postgres truncates longer identifiers
		n = n[:60]
	}
	return n
}

func TestVerifyAcceptsTheRealMigratedSchemas(t *testing.T) {
	_, appURL := needDB(t)
	for _, svc := range []string{"org", "identity", "ledger", "agents-ts"} {
		t.Run(svc, func(t *testing.T) {
			admin, _ := testkit.DBWithApp(t)
			ctx := context.Background()
			if err := dbx.Migrate(ctx, admin, os.DirFS(filepath.Join("..", "..", "..", svc, "migrations"))); err != nil {
				t.Fatal(err)
			}
			var schema string
			if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
				t.Fatal(err)
			}
			d := Databases{URLs: map[string]string{svc: appURL}, Schemas: []string{schema}}
			detail, err := d.EnsureDatabases(ctx, Tenant{ID: tenantA}, false)
			if err != nil {
				t.Fatalf("a real migrated %s schema was refused: %v", svc, err)
			}
			if !strings.Contains(detail, "verified row-level security") {
				t.Fatal(detail)
			}
		})
	}
}

func TestVerifyRefusesWeakSchemasAndTheOwnerRole(t *testing.T) {
	ownerURL, appURL := needDB(t)
	schema := schemaName(t)
	purgeRole(t, ownerURL, appURL)
	// reference rows of the platform tenant and a not-forced service_token_replay are accepted; a plain not-forced table is not.
	shapedSchema(t, ownerURL, schema, `
	create table `+schema+`.ref (tenant_id uuid not null, code text not null, primary key (tenant_id, code));
	insert into `+schema+`.ref values ('00000000-0000-0000-0000-000000000000', 'X');
	alter table `+schema+`.ref enable row level security; alter table `+schema+`.ref force row level security;
	create policy p on `+schema+`.ref using (tenant_id in (nullif(current_setting('passage.tenant_id', true), '')::uuid, '00000000-0000-0000-0000-000000000000'));
	create table `+schema+`.service_token_replay (tenant_id uuid not null, jti text not null, primary key (tenant_id, jti));
	alter table `+schema+`.service_token_replay enable row level security;
	create policy p on `+schema+`.service_token_replay using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);
	grant select on `+schema+`.ref, `+schema+`.service_token_replay to passage_app;`)
	d := Databases{URLs: map[string]string{"ledger": appURL}, Schemas: []string{schema}}
	if _, err := d.EnsureDatabases(context.Background(), Tenant{ID: tenantA}, false); err != nil {
		t.Fatalf("exemptions were refused: %v", err)
	}
	exec(t, ownerURL, `create table `+schema+`.plain (tenant_id uuid not null); alter table `+schema+`.plain enable row level security;
		grant select on `+schema+`.plain to passage_app`)
	if _, err := d.EnsureDatabases(context.Background(), Tenant{ID: tenantA}, false); err == nil || !strings.Contains(err.Error(), schema+".plain") {
		t.Fatalf("a not-forced table was accepted: %v", err)
	}
	if _, err := (Databases{URLs: map[string]string{"x": ownerURL}, Schemas: []string{schema}}).EnsureDatabases(context.Background(), Tenant{ID: tenantA}, false); err == nil || !strings.Contains(err.Error(), "BYPASSRLS") {
		t.Fatalf("%v", err)
	}
}

func TestVerifyFlagsRowsOfOtherTenants(t *testing.T) {
	ownerURL, appURL := needDB(t)
	schema := schemaName(t)
	purgeRole(t, ownerURL, appURL)
	shapedSchema(t, ownerURL, schema, `
	create table `+schema+`.leaky (tenant_id uuid not null); insert into `+schema+`.leaky values ('`+tenantB+`');
	alter table `+schema+`.leaky enable row level security; alter table `+schema+`.leaky force row level security;
	create policy p on `+schema+`.leaky using (true);
	grant select on `+schema+`.leaky to passage_app;`)
	_, err := (Databases{URLs: map[string]string{"svc": appURL}, Schemas: []string{schema}}).EnsureDatabases(context.Background(), Tenant{ID: tenantA}, false)
	if err == nil || !strings.Contains(err.Error(), "leaky shows 1 rows of other tenants") {
		t.Fatalf("%v", err)
	}
}

func TestDatabasesDryRunAndDecommissionRetainsData(t *testing.T) {
	d := Databases{URLs: map[string]string{"ledger": "postgres://never-used"}}
	det, err := d.EnsureDatabases(context.Background(), Tenant{ID: tenantA}, true)
	if err != nil || !strings.Contains(det, "would verify") {
		t.Fatalf("%q %v", det, err)
	}
	for _, dry := range []bool{true, false} {
		det, err := d.DropDatabases(context.Background(), Tenant{ID: tenantA}, dry)
		if err != nil || !strings.Contains(det, "tenant data retained under the retention policy; purge separately") {
			t.Fatalf("%q %v", det, err)
		}
	}
	if _, err := (Databases{}).EnsureDatabases(context.Background(), Tenant{ID: tenantA}, false); err == nil {
		t.Fatal("no databases configured was accepted")
	}
}

func rowsOf(t *testing.T, ownerURL, schema, tbl, tenant string) int {
	return count(t, ownerURL, "select count(*) from "+schema+"."+tbl+" where tenant_id = '"+tenant+"'")
}

func TestPurgeDeletesInFKOrderRetainsImmutableAndListsOtherKeys(t *testing.T) {
	ownerURL, appURL := needDB(t)
	schema := schemaName(t)
	purgeURL := purgeRole(t, ownerURL, appURL)
	shapedSchema(t, ownerURL, schema, `
	create table `+schema+`.audit_log (tenant_id uuid not null, id int not null, primary key (tenant_id, id));
	insert into `+schema+`.audit_log values ('`+tenantA+`', 1);
	create function `+schema+`.no_delete() returns trigger language plpgsql as $f$ begin raise exception 'append-only'; end $f$;
	create trigger no_del before delete on `+schema+`.audit_log for each row execute function `+schema+`.no_delete();
	alter table `+schema+`.audit_log enable row level security; alter table `+schema+`.audit_log force row level security;
	create policy p on `+schema+`.audit_log using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);
	create table `+schema+`.report_due (report_tenant uuid primary key); insert into `+schema+`.report_due values ('`+tenantA+`');
	create table `+schema+`.nogrant (tenant_id uuid not null); insert into `+schema+`.nogrant values ('`+tenantA+`');
	alter table `+schema+`.nogrant enable row level security; alter table `+schema+`.nogrant force row level security;
	create policy p on `+schema+`.nogrant using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);`)
	exec(t, ownerURL, `revoke delete on `+schema+`.nogrant from provtest_purger`)
	p := Purger{URLs: map[string]string{"svc": purgeURL}, Schemas: []string{schema}}
	rep, err := p.Purge(context.Background(), tenantA)
	if err != nil {
		t.Fatal(err)
	}
	if rowsOf(t, ownerURL, schema, "parent", tenantA) != 0 || rowsOf(t, ownerURL, schema, "child", tenantA) != 0 {
		t.Fatalf("the tenant rows remain: %s", rep.Detail())
	}
	if rowsOf(t, ownerURL, schema, "parent", tenantB) != 1 || rowsOf(t, ownerURL, schema, "child", tenantB) != 1 {
		t.Fatal("another tenant's rows were deleted")
	}
	if rowsOf(t, ownerURL, schema, "identity_tenants", tenantA) != 1 || rowsOf(t, ownerURL, schema, "audit_log", tenantA) != 1 || rowsOf(t, ownerURL, schema, "nogrant", tenantA) != 1 {
		t.Fatal("a retained table was purged")
	}
	d := rep.Detail()
	for _, want := range []string{"identity_tenants: listed as retained", "audit_log: has a BEFORE DELETE trigger", "nogrant: the purge role has no DELETE grant", "report_due: keyed by report_tenant"} {
		if !strings.Contains(d, want) {
			t.Fatalf("detail lacks %q: %s", want, d)
		}
	}
	if rep.Deleted != 2 || rep.Tables != 2 {
		t.Fatalf("%+v", rep)
	}
}

func TestPurgeFailsWhenPolicyHidesRowsFromTheDelete(t *testing.T) {
	ownerURL, appURL := needDB(t)
	schema := schemaName(t)
	purgeURL := purgeRole(t, ownerURL, appURL)
	// FOR SELECT only: the DELETE matches no row and reports success; the count inside the transaction sees the rows.
	shapedSchema(t, ownerURL, schema, `
	create table `+schema+`.readonly_t (tenant_id uuid not null); insert into `+schema+`.readonly_t values ('`+tenantA+`');
	alter table `+schema+`.readonly_t enable row level security; alter table `+schema+`.readonly_t force row level security;
	create policy p on `+schema+`.readonly_t for select using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);
	grant delete on `+schema+`.readonly_t to provtest_purger;`)
	_, err := (Purger{URLs: map[string]string{"svc": purgeURL}, Schemas: []string{schema}}).Purge(context.Background(), tenantA)
	if err == nil || !strings.Contains(err.Error(), "readonly_t has 1 rows left") {
		t.Fatalf("%v", err)
	}
	if rowsOf(t, ownerURL, schema, "parent", tenantA) != 1 {
		t.Fatal("the failed purge was not rolled back")
	}
}

func TestPurgeRollsBackEveryDatabaseWhenOneFails(t *testing.T) {
	ownerURL, appURL := needDB(t)
	purgeURL := purgeRole(t, ownerURL, appURL)
	schema := schemaName(t)
	second := "agent_r4j_purge2"
	root, _ := url.Parse(ownerURL)
	root.Path = "/postgres"
	c, err := pgx.Connect(context.Background(), root.String())
	if err != nil {
		t.Fatal(err)
	}
	var have bool
	_ = c.QueryRow(context.Background(), "select exists(select 1 from pg_database where datname = $1)", second).Scan(&have)
	if !have {
		if _, err := c.Exec(context.Background(), "create database "+second); err != nil {
			t.Skip("cannot create a second database: ", err)
		}
	}
	_ = c.Close(context.Background())
	owner2 := dbURL(ownerURL, second)
	exec(t, owner2, `grant all on schema public to passage_app, provtest_purger`)
	shapedSchema(t, ownerURL, schema, "")
	shapedSchema(t, owner2, schema, `
	create table `+schema+`.trap (tenant_id uuid not null); insert into `+schema+`.trap values ('`+tenantA+`');
	alter table `+schema+`.trap enable row level security; alter table `+schema+`.trap force row level security;
	create policy p on `+schema+`.trap for select using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);
	grant delete on `+schema+`.trap to provtest_purger;`)
	_, err = (Purger{URLs: map[string]string{"a": purgeURL, "b": dbURL(purgeURL, second)}, Schemas: []string{schema}}).Purge(context.Background(), tenantA)
	if err == nil || !strings.Contains(err.Error(), "b: rows remain") || strings.Contains(err.Error(), "a: ") || !strings.Contains(err.Error(), "every transaction was rolled back") {
		t.Fatalf("%v", err)
	}
	if rowsOf(t, ownerURL, schema, "parent", tenantA) != 1 || count(t, owner2, "select count(*) from "+schema+".parent where tenant_id = '"+tenantA+"'") != 1 {
		t.Fatal("a database was purged although another failed")
	}
	// Without the trap both purge.
	exec(t, owner2, "drop table "+schema+".trap")
	rep, err := (Purger{URLs: map[string]string{"a": purgeURL, "b": dbURL(purgeURL, second)}, Schemas: []string{schema}}).Purge(context.Background(), tenantA)
	if err != nil || rep.Databases != 2 {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestPurgePlanAndEnv(t *testing.T) {
	p := Purger{URLs: map[string]string{"ledger": "postgres://never-used"}, KeepTables: []string{"kept"}}
	plan, err := p.Plan(tenantA)
	if err != nil || !strings.Contains(plan, "identity_tenants") || !strings.Contains(plan, "kept") || !strings.Contains(plan, "ledger") {
		t.Fatalf("%q %v", plan, err)
	}
	if _, err := PurgerFromEnv(func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "OPS_PURGE_DATABASES") {
		t.Fatalf("%v", err)
	}
	f := filepath.Join(t.TempDir(), "purge.json")
	_ = os.WriteFile(f, []byte(`{"ledger":"postgres://x","billing":"postgres://y"}`), 0o600)
	env := map[string]string{"OPS_PURGE_DATABASES": "@" + f, "OPS_PURGE_KEEP_TABLES": "kept, other"}
	got, err := PurgerFromEnv(func(k string) string { return env[k] })
	if err != nil || len(got.URLs) != 2 || len(got.KeepTables) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if k := got.keep(); !k["identity_tenants"] || !k["kept"] || !k["other"] {
		t.Fatalf("keep list %v", k)
	}
	env["OPS_PURGE_DATABASES"] = "@/nonexistent"
	if _, err := PurgerFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatal("missing file accepted")
	}
	// @file also works for the verification databases
	env2 := map[string]string{"OPS_PROVISION_DATABASES": "@" + f}
	ex, _, err := FromEnv(func(k string) string { return env2[k] }, nil, nil)
	if err != nil || len(ex.Databases.(Databases).URLs) != 2 {
		t.Fatalf("%v", err)
	}
}
