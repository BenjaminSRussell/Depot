package provision

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/depot/platform/rlsexempt"
)

// Databases is the DatabaseAdmin for Depot's tenancy model: one CloudNativePG database per bounded context, schema = context
// name, a shared schema with tenant_id as the first column of every table and row-level security (docs/architecture.md E.1, E.2).
// A tenant therefore has no database or schema of its own to create, and its rows are written by the services as it works (its
// legal entities by identity, its number sequences by numbering). Provisioning the databases means proving the tenant can safely
// start using them: for every configured service database (URLs holds one connection string per context, as the application role
// depot_app) EnsureDatabases checks that the role has neither SUPERUSER nor BYPASSRLS, that every table with a tenant_id column
// has row security enabled and forced (except the tables platform/go/rlsexempt allows, the same list tools/rlscheck enforces), and
// that a session scoped to the new tenant sees no row of another tenant (the platform tenant's reference rows are not "another
// tenant"). It changes nothing.
//
// Decommission does not delete data: DropDatabases reports DONE with the explicit detail that the data is retained, and the purge is
// the separate, gated Purger.
type Databases struct {
	URLs map[string]string // context name -> connection string
	// Schemas limits the check to these schemas (default: every non-system schema; tests use a throwaway schema).
	Schemas []string
}

func (d Databases) names() []string {
	n := make([]string, 0, len(d.URLs))
	for k := range d.URLs {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

type tenantTable struct{ Schema, Name string }

func (t tenantTable) ident() string { return pgx.Identifier{t.Schema, t.Name}.Sanitize() }

const tenantTablesSQL = `
select n.nspname, c.relname, c.relrowsecurity, c.relforcerowsecurity
from pg_class c
join pg_namespace n on n.oid = c.relnamespace
join pg_attribute a on a.attrelid = c.oid and a.attname = 'tenant_id' and not a.attisdropped
where c.relkind in ('r', 'p') and not c.relispartition
  and n.nspname not in ('pg_catalog', 'information_schema') and n.nspname not like 'pg\_%'
  and ($1::text[] is null or n.nspname = any($1))
order by 1, 2`

func scopeTenant(ctx context.Context, tx pgx.Tx, tenantID string) error {
	_, err := tx.Exec(ctx, `select set_config('depot.tenant_id', $1, true), set_config('depot.legal_entity_scopes', '*', true)`, tenantID)
	return err
}

func (d Databases) EnsureDatabases(ctx context.Context, t Tenant, dry bool) (string, error) {
	if err := checkTenantID(t.ID); err != nil {
		return "", err
	}
	if len(d.URLs) == 0 {
		return "", errors.New("databases: no service database is configured (OPS_PROVISION_DATABASES)")
	}
	if dry {
		return fmt.Sprintf("shared-schema tenancy: no database is created; would verify row-level security in %d service databases (%s) for tenant %s", len(d.URLs), strings.Join(d.names(), ", "), t.ID), nil
	}
	var total int
	for _, name := range d.names() {
		n, err := d.verify(ctx, name, t.ID)
		if err != nil {
			return "", fmt.Errorf("databases: %s: %w", name, err)
		}
		total += n
	}
	return fmt.Sprintf("verified row-level security on %d tenant tables in %d service databases; tenant %s starts empty and is seeded by the services", total, len(d.URLs), t.ID), nil
}

func (d Databases) schemaArg() any {
	if len(d.Schemas) == 0 {
		return nil
	}
	return d.Schemas
}

func (d Databases) verify(ctx context.Context, name, tenantID string) (int, error) {
	conn, err := pgx.Connect(ctx, d.URLs[name])
	if err != nil {
		return 0, fmt.Errorf("connect: %w", redact(err))
	}
	defer func() { _ = conn.Close(ctx) }()
	var bypass bool
	if err := conn.QueryRow(ctx, `select rolsuper or rolbypassrls from pg_roles where rolname = current_user`).Scan(&bypass); err != nil {
		return 0, err
	}
	if bypass {
		return 0, errors.New("the connection role has SUPERUSER or BYPASSRLS: use depot_app so the check proves what the services will see")
	}
	rows, err := conn.Query(ctx, tenantTablesSQL, d.schemaArg())
	if err != nil {
		return 0, err
	}
	var tables []tenantTable
	var weak []string
	for rows.Next() {
		var tt tenantTable
		var on, force bool
		if err := rows.Scan(&tt.Schema, &tt.Name, &on, &force); err != nil {
			rows.Close()
			return 0, err
		}
		if !on {
			weak = append(weak, tt.Schema+"."+tt.Name+" (row-level security is off)")
		} else if !force {
			// Not forced is allowed only where platform/go/rlsexempt says so (service_token_replay and the like).
			if _, ok := rlsexempt.Reason(name, tt.Name, rlsexempt.NotForced); !ok {
				weak = append(weak, tt.Schema+"."+tt.Name+" (not forced)")
			}
		}
		tables = append(tables, tt)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(weak) > 0 {
		return 0, fmt.Errorf("row-level security is not enabled and forced on %s (run the service's migrations)", strings.Join(weak, ", "))
	}
	if len(tables) == 0 {
		return 0, errors.New("no table with a tenant_id column: the service's migrations have not run")
	}
	// A session scoped to the new tenant must see nothing that is not its own. The platform tenant's rows (system reference data) and
	// the tables whose SELECT policy is open by documented design are not "another tenant"; a table the role cannot read at all
	// (no grant: reached only through SECURITY DEFINER functions) cannot show anything and is skipped.
	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := scopeTenant(ctx, tx, tenantID); err != nil {
		return 0, err
	}
	for _, tt := range tables {
		if _, ok := rlsexempt.Reason(name, tt.Name, rlsexempt.ExtraPolicy); ok {
			continue
		}
		if _, err := tx.Exec(ctx, "savepoint v"); err != nil {
			return 0, err
		}
		var foreign int64
		err := tx.QueryRow(ctx, "select count(*) from "+tt.ident()+" where tenant_id is distinct from $1::uuid and tenant_id <> $2::uuid", tenantID, rlsexempt.NilTenant).Scan(&foreign)
		if err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "42501" {
				if _, rerr := tx.Exec(ctx, "rollback to savepoint v"); rerr != nil {
					return 0, rerr
				}
				continue
			}
			return 0, fmt.Errorf("%s.%s: %w", tt.Schema, tt.Name, err)
		}
		_, _ = tx.Exec(ctx, "release savepoint v")
		if foreign > 0 {
			return 0, fmt.Errorf("%s.%s shows %d rows of other tenants to a session scoped to tenant %s", tt.Schema, tt.Name, foreign, tenantID)
		}
	}
	return len(tables), nil
}

// retainedDetail is what the decommission's databases step reports: the data stays until a separate, gated purge.
const retainedDetail = "tenant data retained under the retention policy; purge separately (depot-ops tenant purge, after the export is verified)"

func (d Databases) DropDatabases(_ context.Context, t Tenant, _ bool) (string, error) {
	if err := checkTenantID(t.ID); err != nil {
		return "", err
	}
	return retainedDetail, nil
}

// redact keeps a connection string's password out of an error that quotes it.
func redact(err error) error {
	s := err.Error()
	if i := strings.Index(s, "://"); i >= 0 {
		return errors.New("connection failed (details withheld: the error quoted the connection string)")
	}
	return err
}
