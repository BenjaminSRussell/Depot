// Package domain holds the pure consolidation rules: IAS 21 translation and
// CTA, the E1–E5 elimination entries with NCI, ownership validation, unrealized
// profit per IC receipt layer, the equity method and divestiture gain,
// two-sided IC elimination and the unit validation rules of the monitor. It
// performs no I/O; runs, versions and the consolidated dataset are later slices.
package domain

import "fmt"

// Error is a rule violation with a stable code from the wave-40 catalog.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Detail) }

// Codes raised by the domain. CodeBargainPurchase is not in the PRD catalog and is listed in NOT-IMPLEMENTED.md.
const (
	CodeInvalid          = "COM-0001"
	CodeUnbalancedRule   = "CNS-0903"
	CodeOwnership        = "CNS-0904"
	CodeICThreshold      = "CNS-0905"
	CodeValidationFailed = "CNS-0906"
	CodeGroupCostUnknown = "CNS-0908"
	CodeChangeOfControl  = "CNS-0910"
	CodeBargainPurchase  = "CNS-0920"
	CodeRunNotFound      = "CNS-0921"
	CodeIdempotencyReuse = "CNS-0922"
	CodeRunScope         = "CNS-0923"
	// CodeProfitEstimated warns that the unrealised profit in inventory came from the partner balances, not from receipt layers or
	// a stated unsold share; it is not in the PRD catalog and is listed in NOT-IMPLEMENTED.md.
	CodeProfitEstimated = "CNS-0933"
	// CodeICOutsidePairs warns that intercompany balances sit between two units that are not a holder and its subsidiary.
	CodeICOutsidePairs = "CNS-0934"
	// CodeICUntagged warns that intercompany balances without a trading partner on their ledger lines were found and how the run took them.
	CodeICUntagged = "CNS-0935"
)

func invalid(detail string) error { return &Error{Code: CodeInvalid, Detail: detail} }
