// Package reimbursement holds the wire contract between expense and payables for
// paying an approved expense report: expense publishes ReportApproved, payables turns
// it into a vendor open item (expense publishes it once the report's liability is in the
// general ledger), and payables publishes OpenItemSettled when a payment
// run settles that item.
package reimbursement

import "github.com/BenjaminSRussell/passage/platform/events"

var (
	ReportApprovedType  = events.Type("expense", "report", "approved", 1)
	OpenItemSettledType = events.Type("payables", "openitem", "settled", 1)
)

const (
	StatusSettled = "SETTLED"
	InvoicePrefix = "EXP-"
)

type ReportApproved struct {
	ReportID      string `json:"reportId"`
	LegalEntityID string `json:"legalEntityId"`
	WorkerID      string `json:"workerId"`
	Currency      string `json:"currency"`
	Amount        string `json:"amount"`
	ApprovedOn    string `json:"approvedOn"`
	// Revision is the report's version when it was approved; intercompany versions the charges of its lines by it (a revised report is a
	// new revision, so a charge can be repriced by the next version). Zero reads as 1.
	Revision int `json:"revision,omitempty"`
	// ChargedLines are the report's lines whose cost another legal entity bears (#40): intercompany raises a charge from the
	// paying entity to the bearing one for each. Empty when every line is the paying entity's own cost; payables ignores it.
	ChargedLines []ChargedLine `json:"chargedLines,omitempty"`
}

// ChargedLine is one expense line charged to a legal entity other than the one that pays the report.
type ChargedLine struct {
	LineID string `json:"lineId"`
	LineNo int    `json:"lineNo"`
	// Category is the expense category (MEALS, TRAVEL, ...); Amount is in the report currency (decimal string).
	Category        string `json:"category"`
	Amount          string `json:"amount"`
	Currency        string `json:"currency"`
	TransactionDate string `json:"transactionDate"`
	// ChargeToLegalEntityID is the cost-bearing entity; it differs from the report's legalEntityId.
	ChargeToLegalEntityID string `json:"chargeToLegalEntityId"`
}

type OpenItemSettled struct {
	OpenItemID      string `json:"open_item_id"`
	LegalEntityID   string `json:"legal_entity_id"`
	VendorID        string `json:"vendor_id"`
	SourceInvoiceID string `json:"source_invoice_id"`
	Currency        string `json:"currency"`
	OpenAmount      string `json:"open_amount"`
	Status          string `json:"status"`
	SettledAmount   string `json:"settled_amount"`
	SettlementID    string `json:"settlement_id"`
	// PayeeWorkerID is the worker a reimbursement pays when VendorID is the vendor the employee-vendor
	// register maps them to; empty while the item still names the worker as VendorID.
	PayeeWorkerID string `json:"payee_worker_id,omitempty"`
}
