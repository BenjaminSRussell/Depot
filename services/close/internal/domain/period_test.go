package domain_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/services/close/internal/domain"
)

func code(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestDecide_Table(t *testing.T) {
	modules := []domain.Module{domain.ModuleGL, domain.ModuleAP, domain.ModuleAR, domain.ModuleInventory, domain.ModuleAssets}
	groups := []string{"", "controllers", "others"}
	statuses := map[domain.Status]string{
		domain.StatusOpen: "", domain.StatusOnHold: domain.CodePeriodOnHold,
		domain.StatusClosed: domain.CodePeriodClosed, domain.StatusPermanentlyClosed: domain.CodePeriodClosed,
		domain.StatusNeverOpened: domain.CodePeriodClosed,
	}
	for status, want := range statuses {
		for _, m := range modules {
			for _, g := range groups {
				rows := []domain.Row{{Module: m, Status: status, AllowedGroupID: "controllers"}}
				if status == domain.StatusNeverOpened {
					rows = nil
				}
				wantCode := want
				if status == domain.StatusOnHold && g == "controllers" {
					wantCode = ""
				}
				if _, err := domain.Decide(rows, m, g); code(err) != wantCode {
					t.Errorf("status %s module %s group %q: got %q, want %q", status, m, g, code(err), wantCode)
				}
			}
		}
	}
}

func TestDecide_ModuleRowOverridesAll(t *testing.T) {
	rows := []domain.Row{{Module: domain.ModuleAll, Status: domain.StatusClosed}, {Module: domain.ModuleAP, Status: domain.StatusOpen}}
	if _, err := domain.Decide(rows, domain.ModuleAP, ""); err != nil {
		t.Fatalf("module row must win over ALL: %v", err)
	}
	if _, err := domain.Decide(rows, domain.ModuleAR, ""); code(err) != domain.CodePeriodClosed {
		t.Fatalf("ALL row must apply to modules without a row: %v", err)
	}
}

func TestDecide_AllWithoutAllRowIsNeverOpened(t *testing.T) {
	rows := []domain.Row{{Module: domain.ModuleGL, Status: domain.StatusOpen}}
	if _, err := domain.Decide(rows, domain.ModuleAll, ""); code(err) != domain.CodePeriodClosed {
		t.Fatalf("ALL without an ALL row must be never_opened: %v", err)
	}
}

func TestPostingPaths_ClosedRejected(t *testing.T) {
	paths := []string{"manual", "ap", "ar", "inv", "fa", "proj", "tax", "bank", "payroll", "cash", "prod", "reversal", "migration", "batch", "unknown-system", ""}
	closed := []domain.Row{{Module: domain.ModuleAll, Status: domain.StatusClosed}}
	for _, system := range paths {
		if _, err := domain.Decide(closed, domain.ModuleForSource(system), "controllers"); code(err) != domain.CodePeriodClosed {
			t.Errorf("posting path %q was not rejected in a closed period: %v", system, err)
		}
	}
}

func TestPostingPaths_ManualGLAllowedWhenOnlyAPClosed(t *testing.T) {
	rows := []domain.Row{{Module: domain.ModuleAP, Status: domain.StatusClosed}, {Module: domain.ModuleGL, Status: domain.StatusOpen}}
	if _, err := domain.Decide(rows, domain.ModuleForSource("manual"), ""); err != nil {
		t.Fatalf("manual GL posting must pass: %v", err)
	}
	if _, err := domain.Decide(rows, domain.ModuleForSource("ap"), ""); code(err) != domain.CodePeriodClosed {
		t.Fatalf("AP posting must be rejected: %v", err)
	}
}

func TestPostingPaths_UnmappedSourceRejectedWhenAnyModuleClosed(t *testing.T) {
	paths := []string{"manual", "ap", "ar", "inv", "fa", "proj", "tax", "bank", "payroll", "cash", "prod", "reversal", "migration", "batch", "unknown-system", ""}
	rows := []domain.Row{{Module: domain.ModuleAll, Status: domain.StatusOpen}, {Module: domain.ModuleAP, Status: domain.StatusClosed}}
	for _, system := range paths {
		module := domain.ModuleForSource(system)
		_, err := domain.Decide(rows, module, "")
		wantClosed := module == domain.ModuleAP || module == domain.ModuleAll
		if wantClosed != (code(err) == domain.CodePeriodClosed) {
			t.Errorf("posting path %q (module %s): got %v", system, module, err)
		}
	}
}

func TestValidateTransition_Table(t *testing.T) {
	apOpen := []domain.Row{{Module: domain.ModuleGL, Status: domain.StatusOpen}, {Module: domain.ModuleAP, Status: domain.StatusOpen}}
	apClosed := []domain.Row{{Module: domain.ModuleGL, Status: domain.StatusOpen}, {Module: domain.ModuleAP, Status: domain.StatusClosed}}
	tests := []transitionCase{
		{"GL close while AP open", apOpen, domain.StatusClosed, domain.ModuleGL, domain.CodeInvalidTransition, "module ap is still open"},
		{"GL close after AP closed", apClosed, domain.StatusClosed, domain.ModuleGL, "", ""},
		{"AP close while GL open", apOpen, domain.StatusClosed, domain.ModuleAP, "", ""},
		{"closed to open without reopen workflow", apClosed, domain.StatusOpen, domain.ModuleAP, domain.CodeInvalidTransition, "module ap is closed; closed periods change only through the reopen workflow"},
		{"permanently closed is final", []domain.Row{{Module: domain.ModuleGL, Status: domain.StatusPermanentlyClosed}}, domain.StatusOpen, domain.ModuleGL, domain.CodeInvalidTransition, "module gl is permanently closed"},
		{"never opened to closed", nil, domain.StatusClosed, domain.ModuleGL, domain.CodeInvalidTransition, "module gl was never opened"},
		{"never opened to open", nil, domain.StatusOpen, domain.ModuleGL, "", ""},
		{"open to on hold", apOpen, domain.StatusOnHold, domain.ModuleAP, "", ""},
		{"same status", apOpen, domain.StatusOpen, domain.ModuleAP, domain.CodeInvalidTransition, "module ap is already open"},
		{"unknown target", apOpen, domain.StatusNeverOpened, domain.ModuleAP, domain.CodeInvalidTransition, "unknown target status never_opened"},
	}
	tests = append(tests, closeOrderCases(domain.StatusClosed)...)
	tests = append(tests, closeOrderCases(domain.StatusPermanentlyClosed)...)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.ValidateTransition(tc.rows, tc.to, tc.module)
			if code(err) != tc.wantErr || (tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg)) {
				t.Fatalf("got %v, want code %q containing %q", err, tc.wantErr, tc.wantMsg)
			}
		})
	}
}

type transitionCase = struct {
	name    string
	rows    []domain.Row
	to      domain.Status
	module  domain.Module
	wantErr string
	wantMsg string
}

func row(m domain.Module, s domain.Status) domain.Row { return domain.Row{Module: m, Status: s} }

// subledgers returns a row per subledger module with the given status.
func subledgers(s domain.Status) []domain.Row {
	out := make([]domain.Row, 0, len(domain.SubledgerModules()))
	for _, m := range domain.SubledgerModules() {
		out = append(out, row(m, s))
	}
	return out
}

func with(base []domain.Row, extra ...domain.Row) []domain.Row {
	return append(append([]domain.Row{}, base...), extra...)
}

// closeOrderCases are the FR-14.2 ordering cases (B-08) for a close to the
// given target: GL closes after every subledger, ALL after every module.
func closeOrderCases(to domain.Status) []transitionCase {
	closed := subledgers(domain.StatusClosed)
	sealed := subledgers(domain.StatusPermanentlyClosed)
	open, held := domain.StatusOpen, domain.StatusOnHold
	gl, all := domain.ModuleGL, domain.ModuleAll
	bad, name := domain.CodeInvalidTransition, func(s string) string { return string(to) + ": " + s }
	return []transitionCase{
		{name("ALL while GL open"), []domain.Row{row(all, open), row(gl, open)}, to, all, bad, "cannot close all: module gl is still open"},
		{name("ALL while GL on hold"), []domain.Row{row(all, open), row(gl, held)}, to, all, bad, "cannot close all: module gl is still on_hold"},
		{name("ALL while GL open and every subledger closed"), with(closed, row(all, open), row(gl, open)), to, all, bad, "cannot close all: module gl is still open"},
		{name("ALL while AP open and GL closed"), []domain.Row{row(all, open), row(gl, domain.StatusClosed), row(domain.ModuleAP, open)}, to, all, bad, "cannot close all: module ap is still open"},
		{name("ALL on hold while GL open"), []domain.Row{row(all, held), row(gl, open)}, to, all, bad, "cannot close all: module gl is still open"},
		{name("ALL after GL and every subledger closed"), with(closed, row(all, open), row(gl, domain.StatusClosed)), to, all, "", ""},
		{name("ALL after GL and every subledger permanently closed"), with(sealed, row(all, open), row(gl, domain.StatusPermanentlyClosed)), to, all, "", ""},
		{name("ALL with no module rows"), []domain.Row{row(all, open)}, to, all, "", ""},
		{name("ALL with only GL closed (absent modules follow ALL)"), []domain.Row{row(all, held), row(gl, domain.StatusClosed)}, to, all, "", ""},
		{name("GL while ALL open and no subledger rows"), []domain.Row{row(all, open), row(gl, open)}, to, gl, bad, "module ap has no row of its own and all is still open; put all on hold or close ap"},
		{name("GL while ALL on hold and no subledger rows (BR-14.2)"), []domain.Row{row(all, held), row(gl, open)}, to, gl, "", ""},
		{name("GL while ALL open and one subledger without a row"), with(closed[1:], row(all, open), row(gl, open)), to, gl, bad, "module ap has no row of its own"},
		{name("GL while ALL open and every subledger closed"), with(closed, row(all, open), row(gl, open)), to, gl, "", ""},
		{name("GL while ALL closed and no subledger rows"), []domain.Row{row(all, domain.StatusClosed), row(gl, open)}, to, gl, "", ""},
		{name("GL with no ALL row and AP closed"), []domain.Row{row(gl, held), row(domain.ModuleAP, domain.StatusClosed)}, to, gl, "", ""},
		{name("GL while AP on hold and ALL closed"), []domain.Row{row(all, domain.StatusClosed), row(gl, open), row(domain.ModuleAP, held)}, to, gl, bad, "cannot close gl: module ap is still on_hold"},
		{name("subledger while GL and ALL open"), []domain.Row{row(all, open), row(gl, open), row(domain.ModuleAR, open)}, to, domain.ModuleAR, "", ""},
	}
}

// Without a row of its own a module starts from the ALL row's status (FR-14.1),
// so a closed or permanently closed ALL cannot be bypassed through a module.
func TestValidateTransition_InheritsAllWithoutOwnRow(t *testing.T) {
	all, ap, gl := domain.ModuleAll, domain.ModuleAP, domain.ModuleGL
	bad := domain.CodeInvalidTransition
	tests := []transitionCase{
		{"AP open under a permanently closed ALL", []domain.Row{row(all, domain.StatusPermanentlyClosed)}, domain.StatusOpen, ap, bad, "module ap (through all, it has no row of its own) is permanently closed"},
		{"GL open under a permanently closed ALL", []domain.Row{row(all, domain.StatusPermanentlyClosed)}, domain.StatusOpen, gl, bad, "module gl (through all, it has no row of its own) is permanently closed"},
		{"AP open under a closed ALL", []domain.Row{row(all, domain.StatusClosed)}, domain.StatusOpen, ap, bad, "module ap (through all, it has no row of its own) is closed; closed periods change only through the reopen workflow"},
		{"GL on hold under a closed ALL", []domain.Row{row(all, domain.StatusClosed)}, domain.StatusOnHold, gl, bad, "module gl (through all, it has no row of its own) is closed"},
		{"AP permanently closed under a closed ALL", []domain.Row{row(all, domain.StatusClosed)}, domain.StatusPermanentlyClosed, ap, "", ""},
		{"AP pinned open under an open ALL", []domain.Row{row(all, domain.StatusOpen)}, domain.StatusOpen, ap, "", ""},
		{"AP closed under an open ALL", []domain.Row{row(all, domain.StatusOpen)}, domain.StatusClosed, ap, "", ""},
		{"cash closed under a held ALL", []domain.Row{row(all, domain.StatusOnHold)}, domain.StatusClosed, domain.ModuleCash, "", ""},
		{"AP pinned open under a held ALL", []domain.Row{row(all, domain.StatusOnHold)}, domain.StatusOpen, ap, "", ""},
		{"AP closed with no ALL row", []domain.Row{row(gl, domain.StatusOpen)}, domain.StatusClosed, ap, bad, "module ap was never opened; a never opened period must be opened first"},
		{"AP own row already open", []domain.Row{row(all, domain.StatusOpen), row(ap, domain.StatusOpen)}, domain.StatusOpen, ap, bad, "module ap is already open"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.ValidateTransition(tc.rows, tc.to, tc.module)
			if code(err) != tc.wantErr || (tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg)) {
				t.Fatalf("got %v, want code %q containing %q", err, tc.wantErr, tc.wantMsg)
			}
		})
	}
}

// matrixStep is one transition of the BR-14.2 reference configuration for a
// March close; refuse names the expected refusal instead of success.
type matrixStep struct {
	module domain.Module
	to     domain.Status
	refuse string
}

// br142Steps replays the BR-14.2 matrix column by column. Cash and production
// have no row, so the ALL row governs them throughout.
func br142Steps() [][]matrixStep {
	open, held, closed := domain.StatusOpen, domain.StatusOnHold, domain.StatusClosed
	return [][]matrixStep{
		{ // Day 1-3 after period end
			{domain.ModuleInventory, held, ""}, {domain.ModuleProjects, held, ""}, {domain.ModulePayroll, held, ""},
			{domain.ModuleAP, open, ""}, {domain.ModuleAR, open, ""}, {domain.ModuleBank, open, ""},
			{domain.ModuleTax, open, ""}, {domain.ModuleAssets, open, ""}, {domain.ModuleGL, open, ""},
			{domain.ModuleGL, closed, "cannot close gl: module inventory is still on_hold"},
		},
		{ // Day 4-5
			{domain.ModuleInventory, closed, ""}, {domain.ModuleProjects, closed, ""}, {domain.ModulePayroll, closed, ""},
			{domain.ModuleAP, held, ""}, {domain.ModuleAR, held, ""}, {domain.ModuleBank, held, ""},
			{domain.ModuleAssets, closed, ""}, {domain.ModuleGL, held, ""}, {domain.ModuleAll, held, ""},
			{domain.ModuleGL, closed, "cannot close gl: module ap is still on_hold"},
			{domain.ModuleAll, closed, "cannot close all: module ap is still on_hold"},
		},
		{ // After GL close
			{domain.ModuleAP, closed, ""}, {domain.ModuleAR, closed, ""}, {domain.ModuleBank, closed, ""},
			{domain.ModuleGL, closed, "cannot close gl: module tax is still open"},
			{domain.ModuleTax, closed, ""}, // closed after settlement
			{domain.ModuleAll, closed, "cannot close all: module gl is still on_hold"},
			{domain.ModuleGL, closed, ""}, // cash and production follow the held ALL row
			{domain.ModuleAll, closed, ""},
		},
	}
}

func TestValidateTransition_BR142ReferenceMatrix(t *testing.T) {
	rows := []domain.Row{row(domain.ModuleAll, domain.StatusOpen)}
	for col, steps := range br142Steps() {
		for _, st := range steps {
			err := domain.ValidateTransition(rows, st.to, st.module)
			if st.refuse != "" {
				if code(err) != domain.CodeInvalidTransition || !strings.Contains(err.Error(), st.refuse) {
					t.Fatalf("column %d, %s to %s: got %v, want refusal %q", col, st.module, st.to, err, st.refuse)
				}
				continue
			}
			if err != nil {
				t.Fatalf("column %d, %s to %s: %v", col, st.module, st.to, err)
			}
			if i := slices.IndexFunc(rows, func(r domain.Row) bool { return r.Module == st.module }); i >= 0 {
				rows[i].Status = st.to
			} else {
				rows = append(rows, row(st.module, st.to))
			}
		}
	}
	for _, r := range rows {
		if r.Status != domain.StatusClosed {
			t.Errorf("after GL close %s is %s", r.Module, r.Status)
		}
	}
	for _, m := range domain.Modules() {
		if _, err := domain.Decide(rows, m, "controllers"); code(err) != domain.CodePeriodClosed {
			t.Errorf("after GL close, module %s still takes postings: %v", m, err)
		}
	}
}

func TestModulesAreAllGLAndTheSubledgers(t *testing.T) {
	mods := domain.Modules()
	if len(mods) != len(domain.SubledgerModules())+2 || mods[0] != domain.ModuleAll || mods[1] != domain.ModuleGL {
		t.Fatalf("modules = %v", mods)
	}
	for _, m := range domain.SubledgerModules() {
		if m == domain.ModuleAll || m == domain.ModuleGL {
			t.Fatalf("%s is not a subledger", m)
		}
	}
}
