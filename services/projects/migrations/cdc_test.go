package migrations_test

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/projects/migrations"
)

const (
	publicationMigration = "0011_cdc_publication.sql"
	grantMigration       = "0012_cdc_role_grants.sql"
	cdcRole              = "debezium_cdc_projects"
)

func TestCDCMigrationsPublishTheOutboxAndRerunSafely(t *testing.T) {
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1)})
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var published bool
	if err := pool.QueryRow(ctx, `select exists(select 1 from pg_publication_rel pr join pg_publication p on p.oid = pr.prpubid where p.pubname = 'passage_outbox_projects' and pr.prrelid = to_regclass(current_schema() || '.outbox'))`).Scan(&published); err != nil || !published {
		t.Fatalf("outbox published = %v, %v", published, err)
	}
	for _, name := range []string{publicationMigration, grantMigration} {
		sql, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err := pool.Exec(ctx, string(sql)); err != nil {
				t.Fatalf("%s is not re-runnable: %v", name, err)
			}
		}
	}
}

// Roles are cluster-wide, so the grant migration runs against a role with a name of
// its own instead of the real CDC role: parallel runs never share it, and it is
// dropped afterwards. The role ends with USAGE on the schema and SELECT on outbox,
// and no other privilege on outbox or any other table of the schema.
func TestCDCRoleGrantReadsOnlyTheOutbox(t *testing.T) {
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1)})
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	role := cdcRole + "_t" + strings.ReplaceAll(ids.New(), "-", "")[:12]
	if _, err := pool.Exec(ctx, "create role "+role+" nologin"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "drop owned by "+role)
		_, _ = pool.Exec(context.Background(), "drop role "+role)
	})
	sql, err := fs.ReadFile(migrations.FS, grantMigration)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sql), cdcRole) {
		t.Fatalf("%s does not name %s", grantMigration, cdcRole)
	}
	for range 2 {
		if _, err := pool.Exec(ctx, strings.ReplaceAll(string(sql), cdcRole, role)); err != nil {
			t.Fatal(err)
		}
	}
	var usage, read, write bool
	if err := pool.QueryRow(ctx, `select has_schema_privilege($1, current_schema(), 'USAGE'),
		has_table_privilege($1, current_schema() || '.outbox', 'SELECT'),
		has_table_privilege($1, current_schema() || '.outbox', 'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')`, role).Scan(&usage, &read, &write); err != nil {
		t.Fatal(err)
	}
	if !usage || !read || write {
		t.Fatalf("schema usage %v, outbox select %v, outbox write %v; want true, true, false", usage, read, write)
	}
	rows, err := pool.Query(ctx, `select c.relname from pg_class c
		where c.relnamespace = current_schema()::regnamespace and c.relkind in ('r', 'p', 'v', 'm') and c.relname <> 'outbox'
		and has_table_privilege($1, c.oid, 'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')`, role)
	if err != nil {
		t.Fatal(err)
	}
	var others []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		others = append(others, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(others) > 0 {
		t.Fatalf("%s holds privileges on %v besides outbox", role, others)
	}
}
