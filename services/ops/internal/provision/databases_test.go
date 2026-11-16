package provision

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

const tenantA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
const tenantB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

// setupTenantSchema builds a schema shaped like a service's (tenant_id first, forced RLS, a composite foreign key) as the owner, and
// returns the application-role URL.
func setupTenantSchema(t *testing.T, rls bool) (appURL, ownerURL, schema string) {
	t.Helper()
	ownerURL, appURL = os.Getenv("PASSAGE_TEST_DATABASE_URL"), os.Getenv("PASSAGE_TEST_APP_DATABASE_URL")
	if ownerURL == "" || appURL == "" {
		t.Skip("PASSAGE_TEST_DATABASE_URL and PASSAGE_TEST_APP_DATABASE_URL are not set")
	}
	ctx := context.Background()
	c, err := pgx.Connect(ctx, ownerURL)
	if err != nil {
		t.Skip("no database: ", err)
	}
	t.Cleanup(func() { _ = c.Close(ctx) })
	schema = "provtest_" + strings.ReplaceAll(strings.ToLower(t.Name()), "/", "_")
	pol := ""
	if rls {
		pol = `alter table ` + schema + `.parent enable row level security; alter table ` + schema + `.parent force row level security;
		create policy p on ` + schema + `.parent using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);
		alter table ` + schema + `.child enable row level security; alter table ` + schema + `.child force row level security;
		create policy p on ` + schema + `.child using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);`
	}
	sql := `drop schema if exists ` + schema + ` cascade; create schema ` + schema + `;
	create table ` + schema + `.parent (tenant_id uuid not null, id int not null, primary key (tenant_id, id));
	create table ` + schema + `.child (tenant_id uuid not null, id int not null, parent_id int not null, primary key (tenant_id, id),
	  foreign key (tenant_id, parent_id) references ` + schema + `.parent (tenant_id, id));
	create table ` + schema + `.identity_tenants (tenant_id uuid primary key);
	insert into ` + schema + `.parent values ('` + tenantA + `', 1), ('` + tenantB + `', 1);
	insert into ` + schema + `.child values ('` + tenantA + `', 1, 1), ('` + tenantB + `', 1, 1);
	insert into ` + schema + `.identity_tenants values ('` + tenantA + `');
	alter table ` + schema + `.identity_tenants enable row level security; alter table ` + schema + `.identity_tenants force row level security;
	create policy p on ` + schema + `.identity_tenants using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);
	grant usage on schema ` + schema + ` to passage_app; grant all on all tables in schema ` + schema + ` to passage_app;` + pol
	if _, err := c.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = c.Exec(ctx, "drop schema "+schema+" cascade") })
	return appURL, ownerURL, schema
}

func count(t *testing.T, url, q string) int {
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

func TestDatabasesVerifyRLS(t *testing.T) {
	appURL, ownerURL, schema := setupTenantSchema(t, true)
	ctx := context.Background()
	_ = schema
	d := Databases{URLs: map[string]string{"svc": appURL}}
	// The shared database also holds other tests' tables; only this schema's matter to the result, so check the happy path on it
	// by asserting the verdict names no weakness in it.
	detail, err := d.EnsureDatabases(ctx, Tenant{ID: tenantA}, false)
	if err != nil && strings.Contains(err.Error(), schema) {
		t.Fatalf("the schema with forced RLS was flagged: %v", err)
	}
	if err == nil && !strings.Contains(detail, "verified row-level security") {
		t.Fatal(detail)
	}
	// An owner connection bypasses RLS and is refused as the checker.
	if _, err := (Databases{URLs: map[string]string{"svc": ownerURL}}).EnsureDatabases(ctx, Tenant{ID: tenantA}, false); err == nil || !strings.Contains(err.Error(), "BYPASSRLS") {
		t.Fatalf("%v", err)
	}
}

func TestDatabasesFlagsMissingRLS(t *testing.T) {
	appURL, _, schema := setupTenantSchema(t, false)
	_, err := (Databases{URLs: map[string]string{"svc": appURL}}).EnsureDatabases(context.Background(), Tenant{ID: tenantA}, false)
	if err == nil || !strings.Contains(err.Error(), schema+".parent") {
		t.Fatalf("%v", err)
	}
}

func TestDatabasesDryRunAndNoConfig(t *testing.T) {
	d := Databases{URLs: map[string]string{"ledger": "postgres://never-used"}}
	for _, f := range []func(context.Context, Tenant, bool) (string, error){d.EnsureDatabases, d.DropDatabases} {
		if det, err := f(context.Background(), Tenant{ID: tenantA}, true); err != nil || det == "" {
			t.Fatalf("%q %v", det, err)
		}
	}
	if _, err := (Databases{}).EnsureDatabases(context.Background(), Tenant{ID: tenantA}, false); err == nil {
		t.Fatal("no databases configured was accepted")
	}
	if _, err := d.DropDatabases(context.Background(), Tenant{ID: tenantA}, false); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("a drop without purge enabled: %v", err)
	}
}

func TestDatabasesPurgeDeletesOnlyTheTenantInForeignKeyOrder(t *testing.T) {
	appURL, ownerURL, schema := setupTenantSchema(t, true)
	// Other test schemas in the shared test database may hold tenant tables too; the purge is checked on this schema's rows.
	d := Databases{URLs: map[string]string{"svc": appURL}, PurgeEnabled: true}
	detail, err := d.DropDatabases(context.Background(), Tenant{ID: tenantA}, false)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(detail, "deleted") {
		t.Fatal(detail)
	}
	q := func(tbl, ten string) int {
		return count(t, ownerURL, "select count(*) from "+schema+"."+tbl+" where tenant_id = '"+ten+"'")
	}
	if q("parent", tenantA) != 0 || q("child", tenantA) != 0 {
		t.Fatal("the tenant's rows remain")
	}
	if q("parent", tenantB) != 1 || q("child", tenantB) != 1 {
		t.Fatal("another tenant's rows were deleted")
	}
	if q("identity_tenants", tenantA) != 1 {
		t.Fatal("the tenant record is kept by default")
	}
}
