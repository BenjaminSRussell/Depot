package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/BenjaminSRussell/depot/platform/ids"
)

// Scheduled statements (#46, 46-4): a schedule renders the statement of every customer of one legal entity that has open
// items, on a weekly or monthly cadence, and delivers each by the PERIODIC_STATEMENT output rules.

const (
	CadenceWeekly  = "WEEKLY"
	CadenceMonthly = "MONTHLY"

	CodeScheduleNotFound = "PRN-0019" // statement schedule not found (service-local)

	// MaxStatementLines is the most lines one statement PDF holds; a longer statement is cut and says so.
	MaxStatementLines = maxDocLines
)

// StatementSchedule is one recurring statement run.
type StatementSchedule struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	LegalEntityID string `json:"legalEntityId"`
	// SenderName is the name printed in the letterhead.
	SenderName string `json:"senderName"`
	Cadence    string `json:"cadence"`
	// RunDay is the day of the month (MONTHLY) or the ISO weekday, 1 Monday to 7 Sunday (WEEKLY); a month day stops at 28 so
	// every month has it.
	RunDay       int    `json:"runDay"`
	Language     string `json:"language"`
	TemplateCode string `json:"templateCode,omitempty"`
	Enabled      bool   `json:"enabled"`
	NextRunOn    string `json:"nextRunOn"`
	LastRunOn    string `json:"lastRunOn,omitempty"`
	// RunAsOf is the date of a run that has not finished (a customer's statement failed): the next pass retries that date, so a
	// statement already sent is not sent again. Attempts counts the passes of that run.
	RunAsOf  string `json:"runAsOf,omitempty"`
	Attempts int    `json:"attempts,omitempty"`
}

// Validate returns field-level errors, or none.
func (s StatementSchedule) Validate() map[string]string {
	f := map[string]string{}
	if n := len(strings.TrimSpace(s.Name)); n < 1 || n > 100 {
		f["name"] = "is required, at most 100 characters"
	}
	if n := len(strings.TrimSpace(s.SenderName)); n < 1 || n > 200 {
		f["senderName"] = "is required, at most 200 characters"
	}
	if !ids.Valid(s.LegalEntityID) {
		f["legalEntityId"] = "must be an id"
	}
	switch s.Cadence {
	case CadenceWeekly:
		if s.RunDay < 1 || s.RunDay > 7 {
			f["runDay"] = "must be a weekday from 1 (Monday) to 7 (Sunday)"
		}
	case CadenceMonthly:
		if s.RunDay < 1 || s.RunDay > 28 {
			f["runDay"] = "must be a day of the month from 1 to 28"
		}
	default:
		f["cadence"] = "must be WEEKLY or MONTHLY"
	}
	if !ValidLanguage(s.Language) {
		f["language"] = "must be a language tag such as en or de-CH"
	}
	if s.TemplateCode != "" && !ValidCodeName(s.TemplateCode) {
		f["templateCode"] = "must be a template code"
	}
	return f
}

// NextRunOn is the first run date strictly after day for the cadence: the next matching weekday, or the next matching day of a month.
func NextRunOn(cadence string, runDay int, day time.Time) time.Time {
	d := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	if cadence == CadenceWeekly {
		for int(d.Weekday()+6)%7+1 != runDay {
			d = d.AddDate(0, 0, 1)
		}
		return d
	}
	for d.Day() != runDay {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

// StatementSection is one currency of a customer's statement, as receivables returns it.
type StatementSection struct {
	Currency       string          `json:"currency"`
	ClosingBalance string          `json:"closingBalance"`
	Lines          []StatementLine `json:"lines"`
}

// StatementLine is one open item.
type StatementLine struct {
	SourceType     string  `json:"sourceType"`
	DocumentNumber string  `json:"documentNumber"`
	DocumentDate   string  `json:"documentDate"`
	DueDate        *string `json:"dueDate"`
	Amount         string  `json:"amount"`
	OpenAmount     string  `json:"openAmount"`
	RunningBalance string  `json:"runningBalance"`
}

var statementSourceLabels = map[string]string{
	"INVOICE": "Invoice", "FEE": "Dunning fee", "INTEREST": "Interest note", "DEDUCTION": "Deduction", "PAYMENT": "Unapplied payment",
	"ADJUSTMENT": "Adjustment", "REBATE": "Rebate", "CREDIT_NOTE": "Credit note",
}

// cleanText turns what the renderer refuses (control characters, line separators) into spaces and cuts at max runes.
func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x2028 || r == 0x2029 {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// StatementDocument builds the PERIODIC_STATEMENT document of one customer as of a date, the way the customer portal does
// for the statement it shows (same columns and wording), so the two PDFs of one statement read alike.
func StatementDocument(sender, customer, asOf string, sections []StatementSection) DocumentRequest {
	type row struct {
		currency string
		StatementLine
	}
	var all []row
	for _, s := range sections {
		for _, l := range s.Lines {
			all = append(all, row{s.Currency, l})
		}
	}
	shown := all
	if len(shown) > MaxStatementLines {
		shown = shown[:MaxStatementLines]
	}
	balances := make([]string, 0, len(sections))
	for _, s := range sections {
		balances = append(balances, s.ClosingBalance+" "+s.Currency)
	}
	body := fmt.Sprintf("Statement of account as of %s. Nothing is open on your account.", asOf)
	if len(sections) > 0 {
		body = fmt.Sprintf("Statement of account as of %s. The table lists every open item and the running balance in each currency. Closing balance: %s.", asOf, strings.Join(balances, ", "))
	}
	doc := DocumentRequest{
		DocumentType: DocPeriodicStatement,
		Number:       "Statement " + asOf,
		LegalEntity:  DocParty{Name: cleanText(strings.TrimSpace(sender), maxDocField)},
		Fields:       []DocField{{Label: "As of", Value: asOf}},
		Body:         body,
		Columns: []DocColumn{{Header: "Currency"}, {Header: "Document"}, {Header: "Kind"}, {Header: "Date"}, {Header: "Due"},
			{Header: "Amount", Align: "R"}, {Header: "Open", Align: "R"}, {Header: "Balance", Align: "R"}},
	}
	if c := strings.TrimSpace(customer); c != "" {
		doc.Counterparty = &DocParty{Label: "Customer", Name: cleanText(c, maxDocField)}
	}
	for _, l := range shown {
		kind := statementSourceLabels[l.SourceType]
		if kind == "" {
			kind = l.SourceType
		}
		due := ""
		if l.DueDate != nil {
			due = *l.DueDate
		}
		doc.Lines = append(doc.Lines, []string{l.currency, cleanText(l.DocumentNumber, maxDocCell), kind, l.DocumentDate, due, l.Amount, l.OpenAmount, l.RunningBalance})
	}
	for i, s := range sections {
		if i == maxDocFieldCount {
			break
		}
		doc.Totals = append(doc.Totals, DocField{Label: "Closing balance " + s.Currency, Value: s.ClosingBalance})
	}
	if len(all) > MaxStatementLines {
		doc.Notes = fmt.Sprintf("The first %d of %d lines are shown. Ask us for the full statement.", MaxStatementLines, len(all))
	}
	return doc
}
