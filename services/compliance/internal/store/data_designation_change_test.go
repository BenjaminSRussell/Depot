package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/domain"
	cevents "github.com/BenjaminSRussell/depot/services/compliance/internal/events"
)

func designateEntity(t *testing.T, s *Postgres, tenantID, le string) Designation {
	t.Helper()
	d, err := s.Designate(asEntity(tenantID, le, ids.New(), domain.DutyDataDesignationWrite), designationInput(domain.ScopeLegalEntity, le), "k-"+le, t0)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDesignate_WritesExactlyOneChangedEventAndReplayWritesNone(t *testing.T) {
	s, admin := migrated(t)
	tenantID, le := ids.New(), ids.New()
	ctx := asEntity(tenantID, le, ids.New(), domain.DutyDataDesignationWrite)
	d, err := s.Designate(ctx, designationInput(domain.ScopeLegalEntity, le), "k-1", t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Designate(ctx, designationInput(domain.ScopeLegalEntity, le), "k-1", t0); err != nil {
		t.Fatal(err)
	}
	if got := outboxTypes(t, admin, d.DesignationID); len(got) != 1 || got[0] != cevents.DesignationChanged {
		t.Fatalf("outbox after create and replay: %v", got)
	}
	var payload []byte
	if err := admin.QueryRow(context.Background(), `select payload from outbox where aggregate_id = $1`, d.DesignationID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var event struct {
		Data cevents.DesignationChangedData
	}
	if err := json.Unmarshal(payload, &event); err != nil || event.Data.Class != domain.ClassCUI || event.Data.Version != 1 || event.Data.PreviousClass != "" || event.Data.LegalEntityID != le {
		t.Fatalf("event payload %s: %+v %v", payload, event, err)
	}
}

func TestChange_DowngradeKeepsTheRowAuditsAndEmitsOneEvent(t *testing.T) {
	s, admin := migrated(t)
	tenantID, le, user := ids.New(), ids.New(), ids.New()
	d := designateEntity(t, s, tenantID, le)
	ctx := asEntity(tenantID, le, user, domain.DutyDataDesignationWrite)
	out, err := s.ChangeDesignation(ctx, d.DesignationID, d.Version, DesignationChange{Class: domain.ClassNONE, Reason: "released to public"}, t0.Add(time.Hour))
	if err != nil || out.Class != domain.ClassNONE || out.Version != 2 || out.Reason != "released to public" || out.DesignatedBy != user {
		t.Fatalf("downgrade %+v %v", out, err)
	}
	if n := countRows(t, admin, "data_designation"); n != 1 {
		t.Fatalf("a downgrade must keep the row: %d rows", n)
	}
	var action, reason, beforeClass, afterClass string
	if err := admin.QueryRow(context.Background(), `select action, reason, before->>'class', after->>'class' from audit_event where action = 'DOWNGRADE'`).Scan(&action, &reason, &beforeClass, &afterClass); err != nil {
		t.Fatalf("one downgrade audit row expected: %v", err)
	}
	if reason != "released to public" || beforeClass != domain.ClassCUI || afterClass != domain.ClassNONE {
		t.Fatalf("audit %s %q %s -> %s", action, reason, beforeClass, afterClass)
	}
	if got := outboxTypes(t, admin, d.DesignationID); len(got) != 2 || got[1] != cevents.DesignationChanged {
		t.Fatalf("outbox: %v", got)
	}
	resolved, err := s.Resolve(ctx, []ResolveRef{{EntityType: "project", RecordID: ids.New(), LegalEntityID: le}})
	if err != nil || resolved[0].Class != domain.ClassNONE || resolved[0].Source != domain.SourceDefault {
		t.Fatalf("a NONE row must not designate: %+v %v", resolved, err)
	}
	back, err := s.ChangeDesignation(ctx, d.DesignationID, out.Version, DesignationChange{Class: domain.ClassCUI, Reason: "re-designated"}, t0.Add(2*time.Hour))
	if err != nil || back.Class != domain.ClassCUI || back.Version != 3 {
		t.Fatalf("upgrade %+v %v", back, err)
	}
}

func TestChange_RefusalsLeaveNoRowAuditOrEvent(t *testing.T) {
	s, admin := migrated(t)
	tenantID, le := ids.New(), ids.New()
	d := designateEntity(t, s, tenantID, le)
	ctx := asEntity(tenantID, le, ids.New(), domain.DutyDataDesignationWrite)
	for name, tc := range map[string]struct {
		ctx     context.Context
		id      string
		version int
		in      DesignationChange
		code    string
	}{
		"stale version":        {ctx, d.DesignationID, 7, DesignationChange{Class: domain.ClassNONE, Reason: "r"}, CodeVersionConflict},
		"same class":           {ctx, d.DesignationID, 1, DesignationChange{Class: domain.ClassCUI, Reason: "r"}, CodeDesignationUnchanged},
		"no reason":            {ctx, d.DesignationID, 1, DesignationChange{Class: domain.ClassNONE}, "COM-0002"},
		"class P3":             {ctx, d.DesignationID, 1, DesignationChange{Class: "P3", Reason: "r"}, "COM-0002"},
		"unknown id":           {ctx, ids.New(), 1, DesignationChange{Class: domain.ClassNONE, Reason: "r"}, CodeDesignationNotFound},
		"another tenant":       {asEntity(ids.New(), le, ids.New(), domain.DutyDataDesignationWrite), d.DesignationID, 1, DesignationChange{Class: domain.ClassNONE, Reason: "r"}, CodeDesignationNotFound},
		"another legal entity": {asEntity(tenantID, ids.New(), ids.New(), domain.DutyDataDesignationWrite), d.DesignationID, 1, DesignationChange{Class: domain.ClassNONE, Reason: "r"}, CodeDesignationNotFound},
	} {
		if _, err := s.ChangeDesignation(tc.ctx, tc.id, tc.version, tc.in, t0); code(err) != tc.code {
			t.Errorf("%s: %v, want %s", name, err, tc.code)
		}
	}
	var class string
	var version int
	if err := admin.QueryRow(context.Background(), `select class, version from data_designation`).Scan(&class, &version); err != nil || class != domain.ClassCUI || version != 1 {
		t.Fatalf("row after refusals: %s v%d %v", class, version, err)
	}
	if n := countRows(t, admin, "audit_event"); n != 1 {
		t.Fatalf("audit rows after refusals: %d", n)
	}
	if got := outboxTypes(t, admin, d.DesignationID); len(got) != 1 {
		t.Fatalf("outbox after refusals: %v", got)
	}
}

func TestChange_TenantRowNeedsTenantAdministerEvenWhenVisible(t *testing.T) {
	s, admin := migrated(t)
	tenantID, le := ids.New(), ids.New()
	tenantAdmin := asEntity(tenantID, "", ids.New(), tenantAdminDuty, domain.DutyDataDesignationWrite)
	d, err := s.Designate(tenantAdmin, designationInput(domain.ScopeTenant, ""), "k-t", t0)
	if err != nil {
		t.Fatal(err)
	}
	change := DesignationChange{Class: domain.ClassNONE, Reason: "r"}
	for name, ctx := range map[string]context.Context{
		"legal-entity scoped writer":       asEntity(tenantID, le, ids.New(), domain.DutyDataDesignationWrite),
		"legal-entity scoped tenant admin": asEntity(tenantID, le, ids.New(), tenantAdminDuty, domain.DutyDataDesignationWrite),
		"claimless writer":                 asEntity(tenantID, "", ids.New(), domain.DutyDataDesignationWrite),
	} {
		if _, err := s.ChangeDesignation(ctx, d.DesignationID, 1, change, t0); code(err) != "AUTH-0003" {
			t.Errorf("%s: %v", name, err)
		}
	}
	var class string
	if err := admin.QueryRow(context.Background(), `select class from data_designation`).Scan(&class); err != nil || class != domain.ClassCUI {
		t.Fatalf("tenant row after refusals: %s %v", class, err)
	}
	if out, err := s.ChangeDesignation(tenantAdmin, d.DesignationID, 1, change, t0); err != nil || out.Class != domain.ClassNONE {
		t.Fatalf("tenant admin: %+v %v", out, err)
	}
}

func TestChange_TenantAdminReachesLegalEntityRowsAndScopedCallersDoNotSeeOthers(t *testing.T) {
	s, _ := migrated(t)
	tenantID, le := ids.New(), ids.New()
	d := designateEntity(t, s, tenantID, le)
	tenantAdmin := asEntity(tenantID, "", ids.New(), tenantAdminDuty, domain.DutyDataDesignationWrite, domain.DutyDataDesignationRead)
	if got, err := s.GetDesignation(tenantAdmin, d.DesignationID); err != nil || got.DesignationID != d.DesignationID {
		t.Fatalf("tenant admin read: %+v %v", got, err)
	}
	if _, err := s.GetDesignation(asEntity(tenantID, ids.New(), ids.New(), domain.DutyDataDesignationRead), d.DesignationID); code(err) != CodeDesignationNotFound {
		t.Fatalf("scoped read of another entity's designation: %v", err)
	}
	if out, err := s.ChangeDesignation(tenantAdmin, d.DesignationID, 1, DesignationChange{Class: domain.ClassNONE, Reason: "r"}, t0); err != nil || out.Class != domain.ClassNONE {
		t.Fatalf("tenant admin change: %+v %v", out, err)
	}
}

func TestListDesignations_FiltersByRecord(t *testing.T) {
	s, _ := migrated(t)
	tenantID, le, record := ids.New(), ids.New(), ids.New()
	ctx := asEntity(tenantID, le, ids.New(), domain.DutyDataDesignationWrite, domain.DutyDataDesignationRead)
	if _, err := s.Designate(ctx, recordInput(le, record), "k-r", t0); err != nil {
		t.Fatal(err)
	}
	designateEntity(t, s, tenantID, le)
	if got, err := s.ListDesignations(ctx, DesignationFilter{}); err != nil || len(got) != 2 {
		t.Fatalf("unfiltered: %+v %v", got, err)
	}
	got, err := s.ListDesignations(ctx, DesignationFilter{EntityType: "project", RecordID: record})
	if err != nil || len(got) != 1 || got[0].RecordID != record {
		t.Fatalf("by record: %+v %v", got, err)
	}
	if got, err := s.ListDesignations(asEntity(tenantID, ids.New(), ids.New(), domain.DutyDataDesignationRead), DesignationFilter{}); err != nil || len(got) != 0 {
		t.Fatalf("another legal entity must see none: %+v %v", got, err)
	}
}

func TestDesignationHistory_ListsEveryChangeNewestFirst(t *testing.T) {
	s, _ := migrated(t)
	tenantID, le, user := ids.New(), ids.New(), ids.New()
	d := designateEntity(t, s, tenantID, le)
	ctx := asEntity(tenantID, le, user, domain.DutyDataDesignationWrite, domain.DutyDataDesignationRead)
	if _, err := s.ChangeDesignation(ctx, d.DesignationID, 1, DesignationChange{Class: domain.ClassNONE, Reason: "public release"}, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	history, err := s.DesignationHistory(ctx, d.DesignationID)
	if err != nil || len(history) != 2 {
		t.Fatalf("history %+v %v", history, err)
	}
	newest, oldest := history[0], history[1]
	if newest.Action != "DOWNGRADE" || newest.Actor != user || newest.Reason != "public release" || newest.Scope != domain.ScopeLegalEntity || len(newest.Before) == 0 || len(newest.After) == 0 {
		t.Fatalf("newest entry %+v", newest)
	}
	if oldest.Action != "DESIGNATE" || len(oldest.Before) != 0 || oldest.Reason != "drawings under ITAR" {
		t.Fatalf("oldest entry %+v", oldest)
	}
	if _, err := s.DesignationHistory(asEntity(tenantID, ids.New(), user, domain.DutyDataDesignationRead), d.DesignationID); code(err) != CodeDesignationNotFound {
		t.Fatalf("history of another legal entity's designation: %v", err)
	}
	if _, err := s.DesignationHistory(asEntity(ids.New(), le, user, domain.DutyDataDesignationRead), d.DesignationID); code(err) != CodeDesignationNotFound {
		t.Fatalf("history across tenants: %v", err)
	}
}
