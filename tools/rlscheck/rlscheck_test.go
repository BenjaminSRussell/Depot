package rlscheck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/rlsexempt"
	"github.com/BenjaminSRussell/depot/platform/testkit"
)

const appRole = "depot_app"

const (
	problemNotForced   = "rls-not-enabled-and-forced"
	problemNoTenantID  = "no-tenant-id-column"
	problemNoTenantPol = "no-tenant-policy"
	problemNoEntityPol = "no-legal-entity-policy"
	problemExtraPolicy = "extra-permissive-policy"
	problemNullEntity  = "null-admitting-legal-entity-policy"
	problemNoScope     = rlsexempt.NoEntityScope
	// problemDefinerPath: a SECURITY DEFINER function without a search_path that ends with pg_temp. PostgreSQL then
	// searches pg_temp first for relations, so a caller's temp table or view runs as the function owner (B-07a
	// re-review N1). The violation's "table" is the function name.
	problemDefinerPath = "definer-search-path-without-trailing-pg_temp"
)

// definerQuery lists the SECURITY DEFINER functions of a schema with their search_path setting (” when unset).
const definerQuery = `
select p.proname, coalesce((select substr(c, length('search_path=') + 1) from unnest(p.proconfig) c where c like 'search_path=%'), '')
from pg_proc p join pg_namespace n on n.oid = p.pronamespace
where n.nspname = $1 and p.prosecdef
order by p.proname`

// definerPathSafe reports whether a function's search_path lists pg_temp, and lists it last.
func definerPathSafe(path string) bool {
	parts := strings.Split(path, ",")
	last := strings.Trim(strings.TrimSpace(parts[len(parts)-1]), `"`)
	return path != "" && last == "pg_temp"
}

type tableFacts struct {
	Name         string
	Enabled      bool
	Forced       bool
	HasTenantID  bool
	TenantPolicy bool
	HasEntity    bool
	EntityPolicy bool
	ExtraPolicy  bool
	NullEntity   bool
	// EntityScope is any restrictive policy on depot.legal_entity_scopes, including one that follows a parent row's
	// legal entity; NoEntityScope is judged only in rlsexempt.EntityScopedServices.
	EntityScope bool
}

type violation struct {
	Service string
	Table   string
	Problem string
}

func (v violation) key() string { return v.Service + "." + v.Table + ":" + v.Problem }

const tenantColumn = `(^|[^._[:alnum:]])tenant_id([^._[:alnum:]]|$)`

const tenantExpr = tenantColumn + `[^=]*= *[A-Za-z_(]*current_setting\(.depot\.tenant_id.`

// entityScopeExpr is what a restrictive policy that follows a legal entity compares: a legal_entity_id (the row's, or
// a parent's in an EXISTS) against the list in depot.legal_entity_scopes. orTrue is a disjunct that admits every row.
const (
	entityScopeExpr = `legal_entity_id[^,]*= *ANY *\(string_to_array\(current_setting\(.depot\.legal_entity_scopes.`
	orTrue          = `[[:<:]]or[[:space:]]+(true[[:>:]]|\(*[0-9]+ *= *[0-9]+)`
)

const tableFactsQuery = `
select c.relname, c.relrowsecurity, c.relforcerowsecurity,
  exists (select 1 from pg_attribute a where a.attrelid = c.oid and a.attname = 'tenant_id' and not a.attisdropped),
  exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.permissive = 'PERMISSIVE'
          and p.cmd in ('ALL', 'SELECT') and (p.roles = '{public}' or 'depot_app' = any (p.roles))
          and p.qual ~ '` + tenantExpr + `'
          and (p.with_check is null or p.with_check ~ '` + tenantExpr + `')),
  exists (select 1 from pg_attribute a where a.attrelid = c.oid and a.attname = 'legal_entity_id' and not a.attisdropped),
  exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.permissive = 'RESTRICTIVE'
          and p.cmd = 'ALL' and (p.roles = '{public}' or 'depot_app' = any (p.roles))
          and p.qual like '%legal_entity_id%' and p.qual like '%depot.legal_entity_scopes%'
          and (p.with_check is null or p.with_check like '%depot.legal_entity_scopes%')),
  exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.permissive = 'PERMISSIVE'
          and (p.roles = '{public}' or 'depot_app' = any (p.roles))
          and (coalesce(p.qual, '') || ' ' || coalesce(p.with_check, '')) !~ '` + tenantColumn + `'),
  exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.permissive = 'RESTRICTIVE'
          and (coalesce(p.qual, '') || ' ' || coalesce(p.with_check, '')) ~* 'legal_entity_id[[:space:])]+is[[:space:]]+null'),
  exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.permissive = 'RESTRICTIVE'
          and p.cmd = 'ALL' and (p.roles = '{public}' or 'depot_app' = any (p.roles))
          and p.qual ~ '` + entityScopeExpr + `' and p.qual !~* '` + orTrue + `'
          and (p.with_check is null or (p.with_check ~ '` + entityScopeExpr + `' and p.with_check !~* '` + orTrue + `')))
from pg_class c
join pg_namespace n on n.oid = c.relnamespace
where n.nspname = $1 and c.relkind in ('r', 'p') and not c.relispartition and c.relname <> 'schema_migrations'
order by c.relname`

const userSchemasQuery = `select nspname from pg_namespace where nspname not like 'pg\_%' and nspname <> 'information_schema'`

func migrationDirs(t *testing.T) map[string]string {
	t.Helper()
	dirs := map[string]string{}
	for _, glob := range []string{"../../services/*/migrations", "../migration-factory/migrations"} {
		matches, err := filepath.Glob(glob)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range matches {
			dirs[filepath.Base(filepath.Dir(m))] = m
		}
	}
	if len(dirs) == 0 {
		t.Fatal("no service migrations found")
	}
	return dirs
}

// createdByMigration reports whether schema is one a service migration would
// create (today only `migration`). testkit throwaways are t_<32 hex>; other
// packages on a shared Postgres can leak those mid-migrate and must not be
// attributed to the service under check.
func createdByMigration(schema string) bool {
	if schema == "public" || schema == "pg_temp" {
		return false
	}
	if strings.HasPrefix(schema, "t_") && len(schema) == 34 {
		return false
	}
	return true
}

func userSchemas(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]bool {
	t.Helper()
	rows, err := pool.Query(ctx, userSchemasQuery)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[n] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func schemaViolations(t *testing.T, service, dir string) []violation {
	t.Helper()
	pool := testkit.DB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var current string
	if err := pool.QueryRow(ctx, "select current_schema()").Scan(&current); err != nil {
		t.Fatal(err)
	}
	before := userSchemas(t, ctx, pool)
	var extra []string
	t.Cleanup(func() {
		for _, s := range extra {
			_, _ = pool.Exec(context.Background(), "drop schema if exists "+pgx.Identifier{s}.Sanitize()+" cascade")
		}
	})
	if err := dbx.Migrate(ctx, pool, os.DirFS(dir)); err != nil {
		t.Fatalf("%s: migrate: %v", service, err)
	}
	for s := range userSchemas(t, ctx, pool) {
		if !before[s] && createdByMigration(s) {
			extra = append(extra, s)
		}
	}
	sort.Strings(extra)
	var out []violation
	tables := 0
	for _, schema := range append([]string{current}, extra...) {
		n, vs := collect(t, ctx, pool, service, schema)
		tables += n
		out = append(out, vs...)
	}
	if tables == 0 {
		t.Fatalf("%s: migrations yielded no tables to check", service)
	}
	return out
}

func collect(t *testing.T, ctx context.Context, pool *pgxpool.Pool, service, schema string) (int, []violation) {
	t.Helper()
	rows, err := pool.Query(ctx, tableFactsQuery, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []violation
	tables := 0
	for rows.Next() {
		var f tableFacts
		if err := rows.Scan(&f.Name, &f.Enabled, &f.Forced, &f.HasTenantID, &f.TenantPolicy, &f.HasEntity, &f.EntityPolicy, &f.ExtraPolicy, &f.NullEntity, &f.EntityScope); err != nil {
			t.Fatal(err)
		}
		tables++
		out = append(out, judge(service, f)...)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	defs, err := pool.Query(ctx, definerQuery, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer defs.Close()
	for defs.Next() {
		var name, path string
		if err := defs.Scan(&name, &path); err != nil {
			t.Fatal(err)
		}
		if !definerPathSafe(path) {
			out = append(out, violation{service, name, problemDefinerPath})
		}
	}
	if err := defs.Err(); err != nil {
		t.Fatal(err)
	}
	return tables, out
}

func judge(service string, f tableFacts) []violation {
	var out []violation
	add := func(problem string) { out = append(out, violation{service, f.Name, problem}) }
	if !f.Enabled || !f.Forced {
		add(problemNotForced)
	}
	if !f.HasTenantID {
		add(problemNoTenantID)
	} else if !f.TenantPolicy {
		add(problemNoTenantPol)
	}
	if f.HasEntity && !f.EntityPolicy {
		add(problemNoEntityPol)
	}
	if f.ExtraPolicy {
		add(problemExtraPolicy)
	}
	if f.NullEntity {
		add(problemNullEntity)
	}
	if rlsexempt.EntityScopedServices[service] && !f.HasEntity && !f.EntityScope {
		add(problemNoScope)
	}
	return out
}

func TestAllowReasonCoversReplayOnAnyService(t *testing.T) {
	v := violation{"revrec", "service_token_replay", problemNotForced}
	reason, ok := allowReason(v)
	if !ok || reason != replayNotForcedReason {
		t.Fatalf("replay table on an unlisted service: reason %q ok %v", reason, ok)
	}
	if _, ok := allowReason(violation{"revrec", "revenue_contract", problemNotForced}); ok {
		t.Fatal("a real gap on revrec must still fail")
	}
}

func TestCreatedByMigrationIgnoresTestkitLeftovers(t *testing.T) {
	if createdByMigration("t_0123456789abcdef0123456789abcdef") {
		t.Fatal("testkit leftover schema must not be attributed to a service")
	}
	if createdByMigration("public") {
		t.Fatal("public is never a service-created schema")
	}
	if !createdByMigration("migration") {
		t.Fatal("the migration service creates schema migration")
	}
}

func TestJudge(t *testing.T) {
	clean := tableFacts{Name: "x", Enabled: true, Forced: true, HasTenantID: true, TenantPolicy: true, HasEntity: true, EntityPolicy: true}
	if got := judge("s", clean); len(got) != 0 {
		t.Fatalf("clean table flagged: %v", got)
	}
	bad := tableFacts{Name: "x", HasEntity: true}
	if got := judge("s", bad); len(got) != 3 {
		t.Fatalf("got %d violations, want 3: %v", len(got), got)
	}
}

// B-03b (D17): in a service D17 named, a table must carry legal_entity_id or follow a parent's legal entity with a
// restrictive policy; elsewhere a tenant-wide table is not judged on it.
func TestJudgeNoEntityScopeOnlyInD17Services(t *testing.T) {
	tenantWide := tableFacts{Name: "x", Enabled: true, Forced: true, HasTenantID: true, TenantPolicy: true}
	if got := judge("ledger", tenantWide); len(got) != 0 {
		t.Fatalf("a tenant-wide table outside D17 flagged: %v", got)
	}
	if got := judge("shopfloor", tenantWide); len(got) != 1 || got[0].Problem != problemNoScope {
		t.Fatalf("a tenant-wide shopfloor table: %v, want one %s", got, problemNoScope)
	}
	child := tenantWide
	child.EntityScope = true
	if got := judge("shopfloor", child); len(got) != 0 {
		t.Fatalf("a child following its parent's legal entity flagged: %v", got)
	}
	owner := tableFacts{Name: "x", Enabled: true, Forced: true, HasTenantID: true, TenantPolicy: true, HasEntity: true}
	if got := judge("shopfloor", owner); len(got) != 1 || got[0].Problem != problemNoEntityPol {
		t.Fatalf("a legal_entity_id column without its policy: %v, want one %s", got, problemNoEntityPol)
	}
	for _, kernel := range []string{"outbox", "consumer_inbox", "idempotency_keys", "service_token_replay"} {
		if _, ok := allowReason(violation{"fx", kernel, problemNoScope}); !ok {
			t.Errorf("kernel table %s has no %s reason", kernel, problemNoScope)
		}
	}
	if _, ok := allowReason(violation{"fx", "exchange_rate_unlisted", problemNoScope}); ok {
		t.Error("an unlisted table must not be excused")
	}
}

func TestAppRoleHasNoBypassRLS(t *testing.T) {
	pool := testkit.DB(t)
	var super, bypass bool
	err := pool.QueryRow(context.Background(), `select rolsuper, rolbypassrls from pg_roles where rolname = $1`, appRole).Scan(&super, &bypass)
	if err != nil {
		t.Fatalf("role %s: %v", appRole, err)
	}
	if super || bypass {
		t.Fatalf("%s superuser=%v bypassrls=%v; RLS would not bind the application role", appRole, super, bypass)
	}
}

func TestEveryTableEnforcesRLS(t *testing.T) {
	testkit.DB(t)
	dirs := migrationDirs(t)
	services := make([]string, 0, len(dirs))
	for s := range dirs {
		services = append(services, s)
	}
	sort.Strings(services)

	var all []violation
	failed := map[string]bool{}
	for _, s := range services {
		ok := t.Run(s, func(t *testing.T) {
			all = append(all, schemaViolations(t, s, dirs[s])...)
		})
		failed[s] = !ok
	}

	var unexpected, allowed []violation
	seen := map[string]bool{}
	for _, v := range all {
		seen[v.key()] = true
		if _, ok := allowReason(v); ok {
			allowed = append(allowed, v)
		} else {
			unexpected = append(unexpected, v)
		}
	}
	var stale []string
	for k := range allowlist {
		if !seen[k] && !failed[strings.SplitN(k, ".", 2)[0]] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)

	if len(allowed) > 0 {
		t.Logf("allowlisted (see allowlist.go):\n%s", table(allowed))
	}
	if len(unexpected) > 0 {
		t.Errorf("RLS coverage violations (%d):\n%s", len(unexpected), table(unexpected))
	}
	if len(stale) > 0 {
		t.Errorf("allowlist entries that no longer violate; remove them: %s", strings.Join(stale, ", "))
	}
}

func table(vs []violation) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "SERVICE\tTABLE\tPROBLEM")
	for _, v := range vs {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", v.Service, v.Table, v.Problem)
	}
	_ = w.Flush()
	return b.String()
}

// B-03b review: in a service D17 named, a child table passes only with a restrictive policy that compares a legal
// entity against the scope list; one that merely mentions the setting, admits every row, or joins its parent without
// the parent's legal entity is a no-legal-entity-scope violation.
func TestEntityScopeFixtureViolationsArePinned(t *testing.T) {
	testkit.DB(t)
	const service = "entityscope-fixture"
	rlsexempt.EntityScopedServices[service] = true
	t.Cleanup(func() { delete(rlsexempt.EntityScopedServices, service) })
	var keys []string
	for _, v := range schemaViolations(t, service, "testdata/entityscope") {
		keys = append(keys, v.key())
	}
	sort.Strings(keys)
	want := []string{
		service + ".child_no_entity:" + problemNoScope,
		service + ".child_tautology:" + problemNoScope,
		service + ".child_vacuous:" + problemNoScope,
		service + ".tenant_wide:" + problemNoScope,
	}
	if strings.Join(keys, "\n") != strings.Join(want, "\n") {
		t.Fatalf("entity-scope fixture violations:\n%s\nwant:\n%s", strings.Join(keys, "\n"), strings.Join(want, "\n"))
	}
}

func TestFixtureViolationsArePinned(t *testing.T) {
	testkit.DB(t)
	got := schemaViolations(t, "fixture", "testdata/fixture")
	var keys []string
	for _, v := range got {
		keys = append(keys, v.key())
	}
	sort.Strings(keys)
	want := []string{
		"fixture.bad_column:" + problemExtraPolicy,
		"fixture.bad_column:" + problemNoTenantPol,
		"fixture.definer_from_current:" + problemDefinerPath,
		"fixture.definer_no_path:" + problemDefinerPath,
		"fixture.definer_temp_first:" + problemDefinerPath,
		"fixture.extra_true:" + problemExtraPolicy,
		"fixture.insert_true:" + problemExtraPolicy,
		"fixture.null_admitting:" + problemNullEntity,
		"fixture.null_check:" + problemNullEntity,
		"fixture.select_restrictive:" + problemNoEntityPol,
	}
	if strings.Join(keys, "\n") != strings.Join(want, "\n") {
		t.Fatalf("fixture violations:\n%s\nwant:\n%s", strings.Join(keys, "\n"), strings.Join(want, "\n"))
	}
}
