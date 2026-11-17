package migrations_test

import (
	"context"
	"io/fs"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/dbx"
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
}
