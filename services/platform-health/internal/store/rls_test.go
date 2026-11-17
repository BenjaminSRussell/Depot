package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/platform-health/migrations"
)

func TestAppRole_CrossTenantRLS(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	testkit.AssertTenantRLS(t, admin)
	testkit.CrossTenant(t, app, []testkit.RLSCase{
		{
			Table:       "hello_entry",
			Insert:      `insert into hello_entry (tenant_id, id, legal_entity_id, message) values ($1, $2, $3, 'hello')`,
			Args:        func(tenantID, entityID string) []any { return []any{tenantID, ids.New(), entityID} },
			LegalEntity: true,
		},
	})
}
