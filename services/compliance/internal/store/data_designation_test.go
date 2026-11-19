package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/domain"
)

const tenantAdminDuty = "tenant.administer"

func designationInput(scope, le string) DesignationInput {
	return DesignationInput{Scope: scope, LegalEntityID: le, Class: domain.ClassCUI, Category: "export-controlled", Reason: "drawings under ITAR"}
}

func countRows(t *testing.T, admin *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDesignate_TenantScopeNeedsTenantAdministerAndNoLegalEntityClaim(t *testing.T) {
	s, admin := migrated(t)
	tenantID, le := ids.New(), ids.New()
	for name, ctx := range map[string]context.Context{
		"write duty without tenant.administer":       asEntity(tenantID, "", ids.New(), domain.DutyDataDesignationWrite),
		"tenant.administer and a legal-entity claim": asEntity(tenantID, le, ids.New(), tenantAdminDuty, domain.DutyDataDesignationWrite),
		"tenant.administer without the write duty":   asEntity(tenantID, "", ids.New(), tenantAdminDuty),
	} {
		if _, err := s.Designate(ctx, designationInput(domain.ScopeTenant, ""), "k-"+name, t0); code(err) != "AUTH-0003" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := countRows(t, admin, "data_designation") + countRows(t, admin, "audit_event"); n != 0 {
		t.Fatalf("a refused designation left %d rows", n)
	}
}

func TestDesignate_TenantDesignationMakesEveryLegalEntityCUI(t *testing.T) {
	s, _ := migrated(t)
	tenantID, le, other := ids.New(), ids.New(), ids.New()
	admin := asEntity(tenantID, "", ids.New(), tenantAdminDuty, domain.DutyDataDesignationWrite)
	d, err := s.Designate(admin, designationInput(domain.ScopeTenant, ""), "k-1", t0)
	if err != nil || d.Scope != domain.ScopeTenant || d.Class != domain.ClassCUI || d.Version != 1 || d.LegalEntityID != "" || d.Category != "export-controlled" {
		t.Fatalf("designate %+v %v", d, err)
	}
	for _, id := range []string{le, other} {
		if got, err := s.CUISource(asEntity(tenantID, id, ids.New()), id); err != nil || got != domain.ScopeTenant {
			t.Errorf("legal entity %s resolves %q %v", id, got, err)
		}
	}
	if got, err := s.CUISource(asEntity(ids.New(), le, ids.New()), le); err != nil || got != "" {
		t.Fatalf("another tenant must see no designation: %q %v", got, err)
	}
}

func TestDesignate_LegalEntityDesignationLeavesOtherEntitiesUnaffected(t *testing.T) {
	s, _ := migrated(t)
	tenantID, le, other := ids.New(), ids.New(), ids.New()
	ctx := asEntity(tenantID, le, ids.New(), domain.DutyDataDesignationWrite)
	d, err := s.Designate(ctx, designationInput(domain.ScopeLegalEntity, le), "k-1", t0)
	if err != nil || d.LegalEntityID != le || d.Scope != domain.ScopeLegalEntity {
		t.Fatalf("designate %+v %v", d, err)
	}
	if got, err := s.CUISource(ctx, le); err != nil || got != domain.ScopeLegalEntity {
		t.Fatalf("designated entity resolves %q %v", got, err)
	}
	if got, err := s.CUISource(asEntity(tenantID, other, ids.New()), other); err != nil || got != "" {
		t.Fatalf("other entity resolves %q %v", got, err)
	}
	if got, err := s.CUISource(asEntity(tenantID, other, ids.New()), le); err != nil || got != "" {
		t.Fatalf("RLS must hide the designation from another legal entity's caller: %q %v", got, err)
	}
}

func TestDesignate_TenantRowWinsOverLegalEntityRowAsSource(t *testing.T) {
	s, _ := migrated(t)
	tenantID, le := ids.New(), ids.New()
	if _, err := s.Designate(asEntity(tenantID, le, ids.New(), domain.DutyDataDesignationWrite), designationInput(domain.ScopeLegalEntity, le), "k-1", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Designate(asEntity(tenantID, "", ids.New(), tenantAdminDuty, domain.DutyDataDesignationWrite), designationInput(domain.ScopeTenant, ""), "k-2", t0); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CUISource(asEntity(tenantID, le, ids.New()), le); err != nil || got != domain.ScopeTenant {
		t.Fatalf("source %q %v", got, err)
	}
}

func TestDesignate_AuditEventIsWrittenInTheSameTransaction(t *testing.T) {
	s, admin := migrated(t)
	tenantID, le := ids.New(), ids.New()
	user := ids.New()
	ctx := asEntity(tenantID, le, user, domain.DutyDataDesignationWrite)
	d, err := s.Designate(ctx, designationInput(domain.ScopeLegalEntity, le), "k-1", t0)
	if err != nil {
		t.Fatal(err)
	}
	var entityID, action, actor, reason, afterReason string
	var beforeNull bool
	if err := admin.QueryRow(context.Background(), `select entity_id::text, action, actor, reason, before is null, after->>'reason' from audit_event`).
		Scan(&entityID, &action, &actor, &reason, &beforeNull, &afterReason); err != nil {
		t.Fatalf("exactly one audit row expected: %v", err)
	}
	if entityID != d.DesignationID || action != "DESIGNATE" || actor != user || reason != "drawings under ITAR" || afterReason != reason || !beforeNull {
		t.Fatalf("audit row: %s %s %s %q %q %v", entityID, action, actor, reason, afterReason, beforeNull)
	}

	if _, err := admin.Exec(context.Background(), `create function fail_audit() returns trigger language plpgsql as $$ begin raise exception 'audit down'; end $$;
		create trigger fail_audit before insert on audit_event for each row execute function fail_audit()`); err != nil {
		t.Fatal(err)
	}
	other := ids.New()
	if _, err := s.Designate(asEntity(tenantID, other, user, domain.DutyDataDesignationWrite), designationInput(domain.ScopeLegalEntity, other), "k-2", t0); err == nil {
		t.Fatal("a failing audit write must fail the designation")
	}
	if n := countRows(t, admin, "data_designation"); n != 1 {
		t.Fatalf("designation rows after a failed audit write: %d", n)
	}
}

func TestDesignate_ConflictReplayAndKeyReuse(t *testing.T) {
	s, admin := migrated(t)
	tenantID, le := ids.New(), ids.New()
	ctx := asEntity(tenantID, le, ids.New(), domain.DutyDataDesignationWrite)
	first, err := s.Designate(ctx, designationInput(domain.ScopeLegalEntity, le), "k-1", t0)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Designate(ctx, designationInput(domain.ScopeLegalEntity, le), "k-1", t0.Add(1))
	if err != nil || replay.DesignationID != first.DesignationID {
		t.Fatalf("replay must return the first designation: %+v %v", replay, err)
	}
	if n := countRows(t, admin, "data_designation") + countRows(t, admin, "audit_event"); n != 2 {
		t.Fatalf("replay wrote rows: %d", n)
	}
	changed := designationInput(domain.ScopeLegalEntity, le)
	changed.Reason = "a different reason"
	if _, err := s.Designate(ctx, changed, "k-1", t0); code(err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse with another body: %v", err)
	}
	if _, err := s.Designate(ctx, designationInput(domain.ScopeLegalEntity, le), "k-2", t0); code(err) != CodeDesignationDupe {
		t.Fatalf("second designation of the same target: %v", err)
	}
	if n := countRows(t, admin, "data_designation"); n != 1 {
		t.Fatalf("rows after the duplicate: %d", n)
	}
}

func TestDesignate_RejectsInvalidInput(t *testing.T) {
	s, _ := migrated(t)
	tenantID, le := ids.New(), ids.New()
	ctx := asEntity(tenantID, le, ids.New(), domain.DutyDataDesignationWrite)
	for name, in := range map[string]DesignationInput{
		"class not CUI":            {Scope: domain.ScopeLegalEntity, LegalEntityID: le, Class: "P3", Reason: "r"},
		"record scope":             {Scope: domain.ScopeRecord, LegalEntityID: le, Class: domain.ClassCUI, Reason: "r"},
		"legal entity missing":     {Scope: domain.ScopeLegalEntity, Class: domain.ClassCUI, Reason: "r"},
		"nil legal entity":         {Scope: domain.ScopeLegalEntity, LegalEntityID: ids.Nil, Class: domain.ClassCUI, Reason: "r"},
		"empty reason":             {Scope: domain.ScopeLegalEntity, LegalEntityID: le, Class: domain.ClassCUI, Reason: "  "},
		"legal entity on a tenant": {Scope: domain.ScopeTenant, LegalEntityID: le, Class: domain.ClassCUI, Reason: "r"},
	} {
		if _, err := s.Designate(ctx, in, "k", t0); code(err) != "COM-0002" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDesignate_AppRoleCannotDeleteDesignationsOrAuditEvents(t *testing.T) {
	if os.Getenv(testkit.EnvAppURL) == "" {
		t.Skip("needs DEPOT_TEST_APP_DATABASE_URL")
	}
	s, _ := migrated(t)
	tenantID, le := ids.New(), ids.New()
	ctx := asEntity(tenantID, le, ids.New(), domain.DutyDataDesignationWrite)
	if _, err := s.Designate(ctx, designationInput(domain.ScopeLegalEntity, le), "k", t0); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"data_designation", "audit_event"} {
		err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `delete from `+table)
			return err
		})
		if err == nil {
			t.Errorf("depot_app deleted from %s", table)
		}
	}
}
