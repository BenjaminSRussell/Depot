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

func ValidateTransition(rows []Row, to Status, module Module) error {
	var current Status = StatusNeverOpened
	for _, r := range rows {
		if r.Module == module {
			current = r.Status
		}
	}
	switch {
	case to != StatusOpen && to != StatusOnHold && to != StatusClosed && to != StatusPermanentlyClosed:
		return fail(CodeInvalidTransition, "unknown target status "+string(to))
	case current == StatusPermanentlyClosed:
		return fail(CodeInvalidTransition, "permanently closed periods cannot change")
	case current == StatusClosed && to != StatusPermanentlyClosed:
		return fail(CodeInvalidTransition, "closed periods change only through the reopen workflow")
	case current == StatusNeverOpened && to != StatusOpen:
		return fail(CodeInvalidTransition, "a never opened period must be opened first")
	case current == to:
		return fail(CodeInvalidTransition, "period is already "+string(to))
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
				return fail(CodeInvalidTransition, "module "+string(r.Module)+" is still "+string(r.Status)+"; close every module before closing all")
			}
		}
	case ModuleGL:
		// FR-14.2: GL closes last. A subledger without its own row is governed
		// by the ALL row (FR-14.1), so an open ALL row blocks the GL close
		// unless every subledger has a row of its own that is closed.
		for _, r := range rows {
			if r.Module != ModuleGL && r.Module != ModuleAll && postable(r.Status) {
				return fail(CodeInvalidTransition, "module "+string(r.Module)+" is still "+string(r.Status)+"; close it before the general ledger")
			}
		}
		for _, m := range SubledgerModules() {
			if r := Resolve(rows, m); r.Module == ModuleAll && postable(r.Status) {
				return fail(CodeInvalidTransition, "module "+string(m)+" has no row of its own and all is still "+string(r.Status)+"; close it before the general ledger")
			}
		}
	}
	return nil
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
// close.
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
