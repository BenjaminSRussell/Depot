package domain

import (
	"errors"
	"strings"
	"time"
)

type Module string

const (
	ModuleAll        Module = "all"
	ModuleGL         Module = "gl"
	ModuleAP         Module = "ap"
	ModuleAR         Module = "ar"
	ModuleInventory  Module = "inventory"
	ModuleAssets     Module = "assets"
	ModuleCash       Module = "cash"
	ModuleProduction Module = "production"
	ModuleProjects   Module = "projects"
	ModuleTax        Module = "tax"
	ModuleBank       Module = "bank"
	ModulePayroll    Module = "payroll"
)

type Status string

const (
	StatusOpen              Status = "open"
	StatusOnHold            Status = "on_hold"
	StatusClosed            Status = "closed"
	StatusPermanentlyClosed Status = "permanently_closed"
	StatusNeverOpened       Status = "never_opened"
)

type Row struct {
	Module         Module
	Status         Status
	AllowedGroupID string
}

var sourceModules = map[string]Module{
	"manual": ModuleGL, "gl": ModuleGL, "ap": ModuleAP, "ar": ModuleAR, "inv": ModuleInventory,
	"fa": ModuleAssets, "proj": ModuleProjects, "tax": ModuleTax, "bank": ModuleBank,
	"payroll": ModulePayroll, "cash": ModuleCash, "prod": ModuleProduction,
}

func ModuleForSource(system string) Module {
	if m, ok := sourceModules[system]; ok {
		return m
	}
	return ModuleAll
}

func Resolve(rows []Row, module Module) Row {
	var fallback *Row
	for i := range rows {
		switch rows[i].Module {
		case module:
			return rows[i]
		case ModuleAll:
			fallback = &rows[i]
		}
	}
	if fallback != nil {
		return *fallback
	}
	return Row{Module: module, Status: StatusNeverOpened}
}

func Decide(rows []Row, module Module, group string) (Status, error) {
	if module != ModuleAll {
		return decideRow(Resolve(rows, module), module, group)
	}
	candidates := rows
	if !hasModule(rows, ModuleAll) {
		candidates = append(append([]Row{}, rows...), Row{Module: ModuleAll, Status: StatusNeverOpened})
	}
	result := StatusOpen
	var held error
	for _, row := range candidates {
		status, err := decideRow(row, module, group)
		var de *Error
		switch {
		case errors.As(err, &de) && de.Code == CodePeriodClosed:
			return status, err
		case err != nil:
			held, result = err, status
		case status == StatusOnHold && held == nil:
			result = status
		}
	}
	return result, held
}

func hasModule(rows []Row, module Module) bool {
	for _, r := range rows {
		if r.Module == module {
			return true
		}
	}
	return false
}

func decideRow(row Row, module Module, group string) (Status, error) {
	switch row.Status {
	case StatusOpen:
		return row.Status, nil
	case StatusOnHold:
		if group != "" && group == row.AllowedGroupID {
			return row.Status, nil
		}
		return row.Status, fail(CodePeriodOnHold, "period is on hold for module "+string(module))
	default:
		return row.Status, fail(CodePeriodClosed, "period is "+string(row.Status)+" for module "+string(module))
	}
}

// CurrentStatus returns the status a transition of module starts from and
// whether the module has a row of its own. A module without a row is governed
// by the ALL row (FR-14.1, as in Resolve); with no ALL row either it is never
// opened.
func CurrentStatus(rows []Row, module Module) (Status, bool) {
	inherited := StatusNeverOpened
	for _, r := range rows {
		switch r.Module {
		case module:
			return r.Status, true
		case ModuleAll:
			inherited = r.Status
		}
	}
	return inherited, false
}

func ValidateTransition(rows []Row, to Status, module Module) error {
	current, own := CurrentStatus(rows, module)
	where := "module " + string(module) + " "
	if !own && module != ModuleAll && current != StatusNeverOpened {
		where += "(through all, it has no row of its own) "
	}
	switch {
	case to != StatusOpen && to != StatusOnHold && to != StatusClosed && to != StatusPermanentlyClosed:
		return fail(CodeInvalidTransition, "unknown target status "+string(to)+"; use open, on_hold, closed or permanently_closed")
	case current == StatusPermanentlyClosed:
		return fail(CodeInvalidTransition, where+"is permanently closed; permanently closed periods cannot change")
	case current == StatusClosed && to != StatusPermanentlyClosed:
		return fail(CodeInvalidTransition, where+"is closed; closed periods change only through the reopen workflow, so request a reopen")
	case current == StatusNeverOpened && to != StatusOpen:
		return fail(CodeInvalidTransition, where+"was never opened; a never opened period must be opened first")
	case current == to && own:
		// A module without a row may take a row of its own with the status it
		// inherits from ALL, so it keeps that status when ALL moves on.
		return fail(CodeInvalidTransition, where+"is already "+string(to))
	}
	if to != StatusClosed && to != StatusPermanentlyClosed {
		return nil
	}
	switch module {
	case ModuleAll:
		// FR-14.2: closing ALL requires every module row, GL included, to be
		// closed or absent. An absent module is governed by the ALL row itself.
		for _, r := range rows {
			if r.Module != ModuleAll && postable(r.Status) {
				return fail(CodeInvalidTransition, "cannot close all: module "+string(r.Module)+" is still "+string(r.Status)+"; close "+string(r.Module)+" first"+glLast(r.Module))
			}
		}
	case ModuleGL:
		// FR-14.2: GL closes last. A subledger without a row of its own is
		// governed by the ALL row (FR-14.1); ALL on hold already stops its
		// postings (BR-14.1, the BR-14.2 reference matrix holds ALL while GL
		// closes), so only an open ALL row blocks the GL close.
		for _, r := range rows {
			if r.Module != ModuleGL && r.Module != ModuleAll && postable(r.Status) {
				return fail(CodeInvalidTransition, "cannot close gl: module "+string(r.Module)+" is still "+string(r.Status)+"; close "+string(r.Module)+" before the general ledger")
			}
		}
		for _, m := range SubledgerModules() {
			if r := Resolve(rows, m); r.Module == ModuleAll && r.Status == StatusOpen {
				return fail(CodeInvalidTransition, "cannot close gl: module "+string(m)+" has no row of its own and all is still open; put all on hold or close "+string(m)+" before the general ledger")
			}
		}
	}
	return nil
}

func glLast(m Module) string {
	if m == ModuleGL {
		return " (after every subledger)"
	}
	return ""
}

// Modules returns every module of the period-control matrix (FR-14.1).
func Modules() []Module {
	return append([]Module{ModuleAll, ModuleGL}, SubledgerModules()...)
}

// SubledgerModules returns the modules that must be closed before the general
// ledger (FR-14.2). GL and ALL are not subledgers.
func SubledgerModules() []Module {
	return []Module{
		ModuleAP, ModuleAR, ModuleInventory, ModuleAssets, ModuleCash, ModuleProduction,
		ModuleProjects, ModuleTax, ModuleBank, ModulePayroll,
	}
}

// postable reports whether postings can still reach a module in this status
// (BR-14.1): open always, on_hold for the allowed group. Closed, permanently
// closed and never opened modules accept no postings, so they do not block a
// close. A never_opened row is never stored (the status CHECK in
// migrations/0001_period_status.sql allows only open, on_hold, closed and
// permanently_closed); a module is never opened only by having no row.
func postable(s Status) bool {
	return s == StatusOpen || s == StatusOnHold
}

type Span struct {
	FiscalPeriodID string
	Start          time.Time
	End            time.Time
	Rows           []Row
}

func SelectSpan(spans []Span, date time.Time) (Span, error) {
	switch len(spans) {
	case 0:
		return Span{}, fail(CodeNoPeriod, "no fiscal period covers "+date.Format("2006-01-02"))
	case 1:
		return spans[0], nil
	}
	found := make([]string, len(spans))
	for i, s := range spans {
		found[i] = s.FiscalPeriodID
	}
	return Span{}, fail(CodeAmbiguousPeriod, "several fiscal periods cover "+date.Format("2006-01-02")+": "+strings.Join(found, ", "))
}
