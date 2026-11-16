package provision

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Databases is the DatabaseAdmin for Passage's tenancy model: one CloudNativePG database per bounded context, schema = context
// name, a shared schema with tenant_id as the first column of every table and row-level security (docs/architecture.md E.1, E.2).
// A tenant therefore has no database or schema of its own to create, and its rows are written by the services as it works (its
// legal entities by identity, its number sequences by numbering). Provisioning the databases means proving the tenant can safely
// start using them: for every configured service database (URLs holds one connection string per context, as the application role
// passage_app) EnsureDatabases checks that the role has neither SUPERUSER nor BYPASSRLS and that every table with a tenant_id column
// has row security enabled and forced, and that a session scoped to the new tenant sees no row of any other tenant. It changes nothing.
//
// DropDatabases is the decommission: with PurgeEnabled it deletes the tenant's rows (set per session as the tenant, all legal
// entities) from every tenant table except KeepTables, retrying tables whose foreign keys block a delete until none is left; tables
// that refuse (immutable audit tables) fail the step with their names. Without PurgeEnabled it fails, because declaring the step done
// would leave the data behind. Connect as a role that may delete (for example passage_retention), not the application role.
type Databases struct {
	URLs         map[string]string // context name -> connection string
	PurgeEnabled bool
	KeepTables   []string // table names never purged (default identity_tenants: the tenant record outlives its data)
}

func (d Databases) names() []string {
	n := make([]string, 0, len(d.URLs))
	for k := range d.URLs {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

func (d Databases) keep() map[string]bool {
	k := map[string]bool{}
	list := d.KeepTables
	if list == nil {
		list = []string{"identity_tenants"}
	}
	for _, t := range list {
		k[t] = true
	}
	return k
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
order by 1, 2`

func scopeTenant(ctx context.Context, tx pgx.Tx, tenantID string) error {
	_, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', '*', true)`, tenantID)
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
		return 0, errors.New("the connection role has SUPERUSER or BYPASSRLS: use passage_app so the check proves what the services will see")
	}
	rows, err := conn.Query(ctx, tenantTablesSQL)
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
		if !on || !force {
			weak = append(weak, tt.Schema+"."+tt.Name)
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
	// A session scoped to the new tenant must see nothing that is not its own.
	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := scopeTenant(ctx, tx, tenantID); err != nil {
		return 0, err
	}
	for _, tt := range tables {
		var foreign int64
		if err := tx.QueryRow(ctx, "select count(*) from "+tt.ident()+" where tenant_id is distinct from $1::uuid", tenantID).Scan(&foreign); err != nil {
			return 0, fmt.Errorf("%s.%s: %w", tt.Schema, tt.Name, err)
		}
		if foreign > 0 {
			return 0, fmt.Errorf("%s.%s shows %d rows of other tenants to a session scoped to tenant %s", tt.Schema, tt.Name, foreign, tenantID)
		}
	}
	return len(tables), nil
}

func (d Databases) DropDatabases(ctx context.Context, t Tenant, dry bool) (string, error) {
	if err := checkTenantID(t.ID); err != nil {
		return "", err
	}
	if len(d.URLs) == 0 {
		return "", errors.New("databases: no service database is configured (OPS_PROVISION_DATABASES)")
	}
	if dry {
		return fmt.Sprintf("would delete tenant %s's rows from every tenant table in %d service databases (%s); purge enabled: %t", t.ID, len(d.URLs), strings.Join(d.names(), ", "), d.PurgeEnabled), nil
	}
	if !d.PurgeEnabled {
		return "", errors.New("databases: purging tenant rows is not enabled (OPS_PROVISION_DB_PURGE=true, with a connection role that may delete); not reporting the step done while the data remains")
	}
	var total int64
	for _, name := range d.names() {
		n, err := d.purge(ctx, name, t.ID)
		if err != nil {
			return "", fmt.Errorf("databases: %s: %w", name, err)
		}
		total += n
	}
	return fmt.Sprintf("deleted %d rows of tenant %s from %d service databases", total, t.ID, len(d.URLs)), nil
}

func (d Databases) purge(ctx context.Context, name, tenantID string) (int64, error) {
	conn, err := pgx.Connect(ctx, d.URLs[name])
	if err != nil {
		return 0, fmt.Errorf("connect: %w", redact(err))
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx, tenantTablesSQL)
	if err != nil {
		return 0, err
	}
	keep := d.keep()
	var pending []tenantTable
	for rows.Next() {
		var tt tenantTable
		var on, force bool
		if err := rows.Scan(&tt.Schema, &tt.Name, &on, &force); err != nil {
			rows.Close()
			return 0, err
		}
		if !keep[tt.Name] {
			pending = append(pending, tt)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := scopeTenant(ctx, tx, tenantID); err != nil {
		return 0, err
	}
	var total int64
	lastErr := map[string]error{}
	// Foreign keys decide the order: try every pending table, keep the ones that fail, and go round again while a round deletes
	// at least one table's rows. What is still pending after a round with no progress is blocked for a reason other than order.
	for len(pending) > 0 {
		var next []tenantTable
		progress := false
		for _, tt := range pending {
			if _, err := tx.Exec(ctx, "savepoint purge_t"); err != nil {
				return 0, err
			}
			tag, err := tx.Exec(ctx, "delete from "+tt.ident()+" where tenant_id = $1::uuid", tenantID)
			if err != nil {
				lastErr[tt.Schema+"."+tt.Name] = err
				if _, rerr := tx.Exec(ctx, "rollback to savepoint purge_t"); rerr != nil {
					return 0, rerr
				}
				next = append(next, tt)
				continue
			}
			_, _ = tx.Exec(ctx, "release savepoint purge_t")
			total += tag.RowsAffected()
			progress = true
		}
		if !progress {
			var msgs []string
			for _, tt := range next {
				k := tt.Schema + "." + tt.Name
				msgs = append(msgs, fmt.Sprintf("%s (%v)", k, lastErr[k]))
			}
			return 0, fmt.Errorf("could not purge %s; nothing was deleted from this database", strings.Join(msgs, "; "))
		}
		pending = next
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return total, nil
}

// redact keeps a connection string's password out of an error that quotes it.
func redact(err error) error {
	s := err.Error()
	if i := strings.Index(s, "://"); i >= 0 {
		return errors.New("connection failed (details withheld: the error quoted the connection string)")
	}
	return err
}
