package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/services/consolidation/internal/api"
)

type fakeMismatches struct {
	open int
	err  error
	seen []string
}

func (f *fakeMismatches) OpenMismatches(_ context.Context, legalEntityID, period string) (int, error) {
	f.seen = append(f.seen, legalEntityID+"|"+period)
	return f.open, f.err
}

type fakeAudit struct{ rows []string }

func (f *fakeAudit) RecordICOverride(_ context.Context, groupID, period, reason string, open int) error {
	f.rows = append(f.rows, fmt.Sprintf("%s|%s|%s|%d", groupID, period, reason, open))
	return nil
}

func gateRunBody(groupID, extra string) string {
	return strings.Replace(runBody, `"groupId":"G1"`, fmt.Sprintf(`"groupId":%q%s`, groupID, extra), 1)
}

func TestRunGate_OpenICMismatchRefusesUnlessApprovedWithReason(t *testing.T) {
	a, _ := groupsAPI(t)
	g := created[groupOut](t, call(a, http.MethodPost, "/v1/consol/groups", tenantID, api.DutyConfigure, "g1", `{"code":"CRP","name":"Cedar Ridge Pumps group","currency":"USD"}`))
	created[unitOut](t, call(a, http.MethodPost, "/v1/consol/groups/"+g.ID+"/units", tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-US","currency":"USD"}`, leUS)))
	mism, audit := &fakeMismatches{open: 2}, &fakeAudit{}
	a.WithICGate(mism, audit)
	reason := `,"icMismatchOverrideReason":"  accepted by group controller  "`

	rec := call(a, http.MethodPost, runsPath, tenantID, api.DutyRun, "r1", gateRunBody(g.ID, ""))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "CNS-0901") || len(audit.rows) != 0 {
		t.Fatalf("open mismatch = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(a, http.MethodPost, runsPath, tenantID, api.DutyRun, "r2", gateRunBody(g.ID, reason))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "CNS-0937") || len(audit.rows) != 0 {
		t.Fatalf("override without consolidation.approve = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(a, http.MethodPost, runsPath, tenantID, api.DutyRun+","+api.DutyApprove, "r3", gateRunBody(g.ID, reason))
	if rec.Code != http.StatusCreated {
		t.Fatalf("approved override = %d %s", rec.Code, rec.Body.String())
	}
	if want := g.ID + "|2026-06|accepted by group controller|2"; len(audit.rows) != 1 || audit.rows[0] != want {
		t.Fatalf("audit rows = %v, want %q", audit.rows, want)
	}
	if want := leUS + "|2026-06"; mism.seen[0] != want {
		t.Fatalf("reconciliation read for %q, want %q", mism.seen[0], want)
	}
}

func TestRunGate_NoOpenMismatchRunsAndUnreadableReconciliationRefuses(t *testing.T) {
	a, _ := groupsAPI(t)
	g := created[groupOut](t, call(a, http.MethodPost, "/v1/consol/groups", tenantID, api.DutyConfigure, "g1", `{"code":"CRP","name":"Cedar Ridge Pumps group","currency":"USD"}`))
	created[unitOut](t, call(a, http.MethodPost, "/v1/consol/groups/"+g.ID+"/units", tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-US","currency":"USD"}`, leUS)))
	mism, audit := &fakeMismatches{}, &fakeAudit{}
	a.WithICGate(mism, audit)

	if rec := call(a, http.MethodPost, runsPath, tenantID, api.DutyRun, "r1", gateRunBody(g.ID, "")); rec.Code != http.StatusCreated || len(audit.rows) != 0 {
		t.Fatalf("clean reconciliation = %d %s", rec.Code, rec.Body.String())
	}
	mism.err = errors.New("intercompany down")
	if rec := call(a, http.MethodPost, runsPath, tenantID, api.DutyRun+","+api.DutyApprove, "r2", gateRunBody(g.ID, `,"icMismatchOverrideReason":"x"`)); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "CNS-0936") {
		t.Fatalf("unreadable reconciliation = %d %s", rec.Code, rec.Body.String())
	}
}
