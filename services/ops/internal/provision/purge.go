package provision

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Purger deletes a decommissioned tenant's rows from the service databases (`depot-ops tenant purge`). It is deliberately not part of
// the decommission: the API only authorises it for a tenant whose decommission is DONE and whose export is verified. It connects with
// its own credentials (URLs, OPS_PURGE_DATABASES: a role that may delete, never the depot_app role the verification uses).
//
// For every database it opens one transaction, scopes it to the tenant (all legal entities) and deletes the tenant's rows from every
// table with a tenant_id column, in whatever order the foreign keys allow, then counts the tenant's remaining rows in each purged
// table in the same transaction: a table whose rows a policy hid from the DELETE (it affects zero rows without an error) fails the
// purge. Tables that cannot or must not be purged are skipped and named as retained, not failed: those in KeepTables (always including
// identity_tenants), those with a BEFORE DELETE row trigger (append-only audit and ledger tables), those the role has no DELETE grant
// on. Tables keyed by a tenant column not named tenant_id (report_tenant, customer_tenant) are platform state and listed, not purged.
// Nothing is committed until every database prepared cleanly; if any fails, every transaction is rolled back and the failed databases
// are named. The commits themselves are sequential (separate databases cannot commit atomically); a commit that fails after another
// succeeded is reported with the databases already committed.
type Purger struct {
	URLs       map[string]string
	KeepTables []string // added to the default, identity_tenants
	Schemas    []string // limit to these schemas (tests); default every non-system schema
}

// PurgeReport is what a purge did.
type PurgeReport struct {
	Deleted   int64
	Tables    int
	Databases int
	Retained  []string // "<db>.<schema>.<table>: <reason>"
}

// Detail is the summary reported to the API.
func (r PurgeReport) Detail() string {
	d := fmt.Sprintf("deleted %d rows from %d tables in %d databases", r.Deleted, r.Tables, r.Databases)
	if len(r.Retained) > 0 {
		d += "; retained (not purged): " + strings.Join(r.Retained, "; ")
	}
	return d
}

func (p Purger) names() []string {
	n := make([]string, 0, len(p.URLs))
	for k := range p.URLs {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

func (p Purger) keep() map[string]bool {
	k := map[string]bool{"identity_tenants": true}
	for _, t := range p.KeepTables {
		k[t] = true
	}
	return k
}

// Plan says what Purge would do, without connecting anywhere.
func (p Purger) Plan(tenantID string) (string, error) {
	if err := checkTenantID(tenantID); err != nil {
		return "", err
	}
	if len(p.URLs) == 0 {
		return "", errors.New("purge: no database is configured (OPS_PURGE_DATABASES)")
	}
	var keep []string
	for k := range p.keep() {
		keep = append(keep, k)
	}
	sort.Strings(keep)
	return fmt.Sprintf("would delete tenant %s's rows in one transaction per database (%s), verify none remain, commit only if every database is clean; retained: tables named %s, tables with delete triggers, tables with no delete grant", tenantID, strings.Join(p.names(), ", "), strings.Join(keep, ", ")), nil
}

type purgeTx struct {
	name     string
	conn     *pgx.Conn
	tx       pgx.Tx
	deleted  int64
	tables   int
	retained []string
}

func (pt *purgeTx) rollback(ctx context.Context) {
	if pt.tx != nil {
		_ = pt.tx.Rollback(ctx)
	}
	if pt.conn != nil {
		_ = pt.conn.Close(ctx)
	}
}

// Purge runs the purge; see the type's comment.
func (p Purger) Purge(ctx context.Context, tenantID string) (PurgeReport, error) {
	var rep PurgeReport
	if err := checkTenantID(tenantID); err != nil {
		return rep, err
	}
	if len(p.URLs) == 0 {
		return rep, errors.New("purge: no database is configured (OPS_PURGE_DATABASES)")
	}
	var open []*purgeTx
	var failed []string
	for _, name := range p.names() {
		pt, err := p.prepare(ctx, name, tenantID)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		open = append(open, pt)
	}
	if len(failed) > 0 {
		for _, pt := range open {
			pt.rollback(ctx)
		}
		return rep, fmt.Errorf("purge failed in %d of %d databases; every transaction was rolled back and nothing was deleted: %s", len(failed), len(p.URLs), strings.Join(failed, "; "))
	}
	var committed []string
	for i, pt := range open {
		if err := pt.tx.Commit(ctx); err != nil {
			for _, rest := range open[i+1:] {
				rest.rollback(ctx)
			}
			_ = pt.conn.Close(ctx)
			return rep, fmt.Errorf("purge: committing %s failed (%v); already committed: %s; the rest were rolled back: run the purge again", pt.name, redact(err), strings.Join(committed, ", "))
		}
		pt.tx = nil
		_ = pt.conn.Close(ctx)
		committed = append(committed, pt.name)
		rep.Deleted += pt.deleted
		rep.Tables += pt.tables
		rep.Databases++
		rep.Retained = append(rep.Retained, pt.retained...)
	}
	return rep, nil
}

const otherTenantColumnsSQL = `
select n.nspname, c.relname, a.attname
from pg_attribute a
join pg_class c on c.oid = a.attrelid
join pg_namespace n on n.oid = c.relnamespace
where c.relkind in ('r', 'p') and not c.relispartition and not a.attisdropped
  and a.attname ~ '_tenant$' and a.attname <> 'tenant_id'
  and n.nspname not in ('pg_catalog', 'information_schema') and n.nspname not like 'pg\_%'
  and ($1::text[] is null or n.nspname = any($1))
order by 1, 2, 3`

const deleteTriggerSQL = `select exists(select 1 from pg_trigger t where t.tgrelid = $1::regclass and not t.tgisinternal
  and (t.tgtype & 1) = 1 and (t.tgtype & 2) = 2 and (t.tgtype & 8) = 8)`

func (p Purger) prepare(ctx context.Context, name, tenantID string) (_ *purgeTx, err error) {
	conn, err := pgx.Connect(ctx, p.URLs[name])
	if err != nil {
		return nil, fmt.Errorf("connect: %w", redact(err))
	}
	pt := &purgeTx{name: name, conn: conn}
	defer func() {
		if err != nil {
			pt.rollback(ctx)
		}
	}()
	var schemas any
	if len(p.Schemas) > 0 {
		schemas = p.Schemas
	}
	rows, err := conn.Query(ctx, tenantTablesSQL, schemas)
	if err != nil {
		return nil, err
	}
	var all []tenantTable
	for rows.Next() {
		var tt tenantTable
		var on, force bool
		if err := rows.Scan(&tt.Schema, &tt.Name, &on, &force); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, tt)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	orows, err := conn.Query(ctx, otherTenantColumnsSQL, schemas)
	if err != nil {
		return nil, err
	}
	for orows.Next() {
		var s, t, c string
		if err := orows.Scan(&s, &t, &c); err != nil {
			orows.Close()
			return nil, err
		}
		pt.retained = append(pt.retained, fmt.Sprintf("%s.%s.%s: keyed by %s, not tenant_id (platform state)", name, s, t, c))
	}
	orows.Close()
	if err := orows.Err(); err != nil {
		return nil, err
	}

	pt.tx, err = conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if err := scopeTenant(ctx, pt.tx, tenantID); err != nil {
		return nil, err
	}
	keep := p.keep()
	var pending []tenantTable
	for _, tt := range all {
		var blocked bool
		if err := pt.tx.QueryRow(ctx, deleteTriggerSQL, tt.ident()).Scan(&blocked); err != nil {
			return nil, err
		}
		switch {
		case keep[tt.Name]:
			pt.retained = append(pt.retained, fmt.Sprintf("%s.%s.%s: listed as retained", name, tt.Schema, tt.Name))
		case blocked:
			pt.retained = append(pt.retained, fmt.Sprintf("%s.%s.%s: has a BEFORE DELETE trigger (append-only)", name, tt.Schema, tt.Name))
		default:
			pending = append(pending, tt)
		}
	}
	var purged []tenantTable
	lastErr := map[string]error{}
	// Foreign keys decide the order: go round the pending tables while a round deletes (or retains) at least one; what is left after a
	// round without progress is blocked for a reason other than order.
	for len(pending) > 0 {
		var next []tenantTable
		progress := false
		for _, tt := range pending {
			if _, err := pt.tx.Exec(ctx, "savepoint purge_t"); err != nil {
				return nil, err
			}
			tag, err := pt.tx.Exec(ctx, "delete from "+tt.ident()+" where tenant_id = $1::uuid", tenantID)
			if err == nil {
				_, _ = pt.tx.Exec(ctx, "release savepoint purge_t")
				pt.deleted += tag.RowsAffected()
				purged = append(purged, tt)
				progress = true
				continue
			}
			if _, rerr := pt.tx.Exec(ctx, "rollback to savepoint purge_t"); rerr != nil {
				return nil, rerr
			}
			var pg *pgconn.PgError
			switch {
			case errors.As(err, &pg) && pg.Code == "42501":
				pt.retained = append(pt.retained, fmt.Sprintf("%s.%s.%s: the purge role has no DELETE grant", name, tt.Schema, tt.Name))
				progress = true
			case errors.As(err, &pg) && pg.Code == "23503":
				lastErr[tt.Schema+"."+tt.Name] = err
				next = append(next, tt)
			default:
				return nil, fmt.Errorf("%s.%s: %w", tt.Schema, tt.Name, err)
			}
		}
		if !progress {
			var msgs []string
			for _, tt := range next {
				msgs = append(msgs, fmt.Sprintf("%s.%s (%v)", tt.Schema, tt.Name, lastErr[tt.Schema+"."+tt.Name]))
			}
			return nil, fmt.Errorf("could not purge %s: a foreign key from a retained or unpurgeable table blocks the delete", strings.Join(msgs, "; "))
		}
		pending = next
	}
	// The same transaction sees what the DELETE could not reach: a table whose policy hid rows from it deleted zero without an error.
	var leftovers []string
	for _, tt := range purged {
		var n int64
		if err := pt.tx.QueryRow(ctx, "select count(*) from "+tt.ident()+" where tenant_id = $1::uuid", tenantID).Scan(&n); err != nil {
			return nil, fmt.Errorf("%s.%s: counting what remains: %w", tt.Schema, tt.Name, err)
		}
		if n > 0 {
			leftovers = append(leftovers, fmt.Sprintf("%s.%s has %d rows left", tt.Schema, tt.Name, n))
		}
	}
	if len(leftovers) > 0 {
		return nil, fmt.Errorf("rows remain after the delete (a policy hides them from the purge role?): %s", strings.Join(leftovers, ", "))
	}
	pt.tables = len(purged)
	sort.Strings(pt.retained)
	return pt, nil
}
