package store

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/domain"
)

func nthAdmin(tenantID, le string) context.Context {
	return asEntity(tenantID, le, ids.New(), tenantAdminDuty, domain.DutyCUIPolicyAdmin, domain.DutyCUIPolicyRead)
}

func TestNeedToKnow_SetAuditsInTheSameTransactionAndBumpsTheVersion(t *testing.T) {
	s, admin := migrated(t)
	tenantID := ids.New()
	ctx := nthAdmin(tenantID, "")
	first, err := s.SetNeedToKnow(ctx, NeedToKnowInput{Enabled: true, Reason: "contract requires it", ExpectedVersion: 1}, t0)
	if err != nil || !first.Enabled || first.Version != 2 || first.LegalEntityID != "" {
		t.Fatalf("first set %+v %v", first, err)
	}
	second, err := s.SetNeedToKnow(ctx, NeedToKnowInput{Enabled: false, Reason: "scope reduced", ExpectedVersion: 2}, t0.Add(time.Hour))
	if err != nil || second.Enabled || second.Version != 3 || second.SettingID != first.SettingID {
		t.Fatalf("second set %+v %v", second, err)
	}
	var action, reason string
	var before, after *bool
	rows, err := admin.Query(context.Background(), `select action, reason, (before->>'enabled')::boolean, (after->>'enabled')::boolean from audit_event where entity_type = 'need_to_know_setting' order by occurred_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		if err := rows.Scan(&action, &reason, &before, &after); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action)
	}
	if len(actions) != 2 || actions[0] != "ENABLE" || actions[1] != "DISABLE" || reason != "scope reduced" || before == nil || !*before || after == nil || *after {
		t.Fatalf("audit rows %v reason %q before %v after %v", actions, reason, before, after)
	}
}

func TestNeedToKnow_RefusalsWriteNoRowAndNoAudit(t *testing.T) {
	s, admin := migrated(t)
	tenantID, le, other := ids.New(), ids.New(), ids.New()
	if _, err := s.SetNeedToKnow(nthAdmin(tenantID, ""), NeedToKnowInput{Enabled: true, Reason: "baseline", ExpectedVersion: 1}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetNeedToKnow(asEntity(tenantID, le, ids.New(), domain.DutyCUIPolicyAdmin), NeedToKnowInput{LegalEntityID: le, Enabled: true, Reason: "tighten", ExpectedVersion: 1}, t0); err != nil {
		t.Fatalf("a scoped admin may tighten: %v", err)
	}
	rows, audits := countRows(t, admin, "need_to_know_setting"), countRows(t, admin, "audit_event")
	scoped := asEntity(tenantID, le, ids.New(), domain.DutyCUIPolicyAdmin)
	for name, tc := range map[string]struct {
		ctx  context.Context
		in   NeedToKnowInput
		code string
	}{
		"scoped admin turns OFF":          {scoped, NeedToKnowInput{LegalEntityID: le, Enabled: false, Reason: "r", ExpectedVersion: 2}, "AUTH-0003"},
		"scoped admin, another entity":    {scoped, NeedToKnowInput{LegalEntityID: other, Enabled: true, Reason: "r", ExpectedVersion: 1}, "AUTH-0003"},
		"scoped admin, tenant level":      {scoped, NeedToKnowInput{Enabled: true, Reason: "r", ExpectedVersion: 2}, "AUTH-0003"},
		"tenant admin without admin duty": {asEntity(tenantID, "", ids.New(), tenantAdminDuty, domain.DutyCUIPolicyRead), NeedToKnowInput{Enabled: false, Reason: "r", ExpectedVersion: 2}, "AUTH-0003"},
		"stale version":                   {nthAdmin(tenantID, ""), NeedToKnowInput{Enabled: false, Reason: "r", ExpectedVersion: 1}, CodeVersionConflict},
		"stale first write":               {nthAdmin(tenantID, ""), NeedToKnowInput{LegalEntityID: other, Enabled: true, Reason: "r", ExpectedVersion: 5}, CodeVersionConflict},
		"empty reason":                    {nthAdmin(tenantID, ""), NeedToKnowInput{Enabled: false, Reason: "  ", ExpectedVersion: 2}, "COM-0002"},
		"nil legal entity":                {nthAdmin(tenantID, ""), NeedToKnowInput{LegalEntityID: ids.Nil, Enabled: true, Reason: "r", ExpectedVersion: 1}, "COM-0002"},
	} {
		if _, err := s.SetNeedToKnow(tc.ctx, tc.in, t0); code(err) != tc.code {
			t.Errorf("%s: %v, want %s", name, err, tc.code)
		}
	}
	if countRows(t, admin, "need_to_know_setting") != rows || countRows(t, admin, "audit_event") != audits {
		t.Fatalf("refusals wrote rows: %d settings, %d audits", countRows(t, admin, "need_to_know_setting"), countRows(t, admin, "audit_event"))
	}
	var enabled bool
	if err := admin.QueryRow(context.Background(), `select enabled from need_to_know_setting where tenant_id = $1 and legal_entity_id is null`, tenantID).Scan(&enabled); err != nil || !enabled {
		t.Fatalf("tenant row after refusals: %v %v", enabled, err)
	}
}

func TestNeedToKnow_TenantsAndLegalEntitiesSeeOnlyTheirOwn(t *testing.T) {
	s, _ := migrated(t)
	tenantA, tenantB, leA, leB := ids.New(), ids.New(), ids.New(), ids.New()
	for _, tc := range []struct {
		tenantID, le string
		enabled      bool
	}{{tenantA, "", false}, {tenantA, leA, true}, {tenantA, leB, true}, {tenantB, "", true}} {
		if _, err := s.SetNeedToKnow(nthAdmin(tc.tenantID, ""), NeedToKnowInput{LegalEntityID: tc.le, Enabled: tc.enabled, Reason: "seed", ExpectedVersion: 1}, t0); err != nil {
			t.Fatal(err)
		}
	}
	scoped, err := s.GetNeedToKnow(asEntity(tenantA, leA, ids.New(), domain.DutyCUIPolicyRead))
	if err != nil || scoped.Tenant == nil || scoped.Tenant.Enabled || len(scoped.Overrides) != 1 || scoped.Overrides[0].LegalEntityID != leA {
		t.Fatalf("scoped reader %+v %v", scoped, err)
	}
	wide, err := s.GetNeedToKnow(nthAdmin(tenantA, ""))
	if err != nil || len(wide.Overrides) != 2 {
		t.Fatalf("tenant-wide reader %+v %v", wide, err)
	}
	other, err := s.GetNeedToKnow(nthAdmin(tenantB, ""))
	if err != nil || other.Tenant == nil || !other.Tenant.Enabled || len(other.Overrides) != 0 {
		t.Fatalf("second tenant %+v %v", other, err)
	}
	empty, err := s.GetNeedToKnow(nthAdmin(ids.New(), ""))
	if err != nil || empty.Tenant != nil || empty.VersionOf("") != 1 {
		t.Fatalf("never set %+v %v", empty, err)
	}
}
