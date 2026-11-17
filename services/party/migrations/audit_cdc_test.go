package migrations_test

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/party/migrations"
)

// B-07a: vendor_bank_account is published to depot_audit_party with its full old row, and the migration re-runs.
func TestAuditCDCMigrationPublishesVendorBankAccounts(t *testing.T) {
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1)})
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var published bool
	var identity string
	if err := pool.QueryRow(ctx, `select exists(select 1 from pg_publication_rel pr join pg_publication p on p.oid = pr.prpubid
			where p.pubname = 'depot_audit_party' and pr.prrelid = to_regclass(current_schema() || '.vendor_bank_account')),
		(select relreplident::text from pg_class where oid = to_regclass(current_schema() || '.vendor_bank_account'))`).Scan(&published, &identity); err != nil {
		t.Fatal(err)
	}
	if !published || identity != "f" {
		t.Fatalf("published = %v, replica identity = %q; want true, full", published, identity)
	}
	var truncates bool
	if err := pool.QueryRow(ctx, `select pubtruncate from pg_publication where pubname = 'depot_audit_party'`).Scan(&truncates); err != nil || truncates {
		t.Fatalf("pubtruncate = %v, %v; the audit publication does not publish truncate", truncates, err)
	}
	sql, err := fs.ReadFile(migrations.FS, "0021_audit_cdc_publication.sql")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("0021 is not re-runnable: %v", err)
		}
	}
	// The CDC role reads every column but the sealed ones (review finding 8). Roles are cluster-wide, so the grant runs
	// against a role with a name of its own.
	role := cdcRole + "_a" + strings.ReplaceAll(ids.New(), "-", "")[:12]
	if _, err := pool.Exec(ctx, "create role "+role+" nologin"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "drop owned by "+role)
		_, _ = pool.Exec(context.Background(), "drop role "+role)
	})
	for range 2 {
		if _, err := pool.Exec(ctx, strings.ReplaceAll(string(sql), cdcRole, role)); err != nil {
			t.Fatal(err)
		}
	}
	var table, holder, sealed, routing bool
	if err := pool.QueryRow(ctx, `select has_table_privilege($1, current_schema() || '.vendor_bank_account', 'SELECT'),
		has_column_privilege($1, current_schema() || '.vendor_bank_account', 'account_holder', 'SELECT'),
		has_column_privilege($1, current_schema() || '.vendor_bank_account', 'account_number_sealed', 'SELECT'),
		has_column_privilege($1, current_schema() || '.vendor_bank_account', 'routing_number_sealed', 'SELECT')`, role).Scan(&table, &holder, &sealed, &routing); err != nil {
		t.Fatal(err)
	}
	if table || !holder || sealed || routing {
		t.Fatalf("table-wide %v, account_holder %v, account_number_sealed %v, routing_number_sealed %v; want only the unsealed columns", table, holder, sealed, routing)
	}
}
