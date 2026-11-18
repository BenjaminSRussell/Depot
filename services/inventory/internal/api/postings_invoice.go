package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/eventing"
	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/money"
	"github.com/BenjaminSRussell/depot/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/depot/services/inventory/internal/store"
)

const (
	InvoicePostedType = "dev.depot.payables.invoice.posted.v1"
	InvoiceTopic      = "depot.receiving.vendorinvoice"

	CodePostingNotReceived = "INV-PST-006"
	invoiceShareScale      = 12
)

type invoicePostedData struct {
	InvoiceID     string `json:"invoice_id"`
	LegalEntityID string `json:"legal_entity_id"`
	PostingDate   string `json:"posting_date"`
	Currency      string `json:"currency"`
	LedgerEntryID string `json:"ledger_entry_id"`
	Lines         []struct {
		LineNo        int    `json:"line_no"`
		ReceiptID     string `json:"receipt_id"`
		ReceiptLineNo int    `json:"receipt_line_no"`
		Qty           string `json:"qty"`
		Amount        string `json:"amount"`
	} `json:"lines"`
}

func invoiceShares(total decimal.Decimal, n int, currency string) []decimal.Decimal {
	out := make([]decimal.Decimal, n)
	if n == 1 {
		out[0] = total
		return out
	}
	count := decimal.NewFromInt(int64(n))
	share := total.DivRound(count, invoiceShareScale).Truncate(money.MinorUnits(currency))
	for i := range out {
		out[i] = share
	}
	out[n-1] = total.Sub(share.Mul(count.Sub(decimal.NewFromInt(1))))
	return out
}

func enteredTotal(rows []store.TransactionRow) decimal.Decimal {
	total := decimal.Zero
	for _, r := range rows {
		total = total.Add(r.EnteredQty)
	}
	return total
}

func stepInvoiceLine(ctx context.Context, s PostingsStore, d invoicePostedData, lineNo int, receiptID string, qty, amount decimal.Decimal, rows []store.TransactionRow,
	evt events.Event) (int, error) {
	if qty.LessThan(enteredTotal(rows)) {
		slog.WarnContext(ctx, "invoice line partial, stays RECEIVED", "invoiceId", d.InvoiceID, "receiptId", receiptID, "lineNo", lineNo,
			"invoicedQty", qty.String(), "bookedQty", enteredTotal(rows).String(), "eventId", evt.ID)
		return 0, nil
	}
	date, _ := postingDate(d.PostingDate, evt)
	shares := invoiceShares(amount, len(rows), d.Currency)
	stepped := 0
	for i, row := range rows {
		switch domain.Status(row.ReceiptStatus) {
		case domain.StatusPurchased:
			continue
		case domain.StatusReceived:
		default:
			return stepped, eventing.Permanent(fmt.Errorf("%s: booking %s is at %s, not RECEIVED", CodePostingNotReceived, row.ID, row.ReceiptStatus))
		}
		cost := shares[i]
		change, applied, err := s.ApplyStatusStep(ctx, store.StatusStepInput{
			TransactionID: row.ID, Command: domain.CommandStep, From: domain.StatusReceived, To: domain.StatusPurchased, Date: date,
			CostAmount: &cost, LedgerEntryID: d.LedgerEntryID,
			IdempotencyKey: postingsKeyPrefix + "invoice:" + strings.ToLower(d.InvoiceID) + ":" + strconv.Itoa(lineNo) + ":" + row.ID,
		})
		if err != nil {
			return stepped, err
		}
		stepped++
		slog.InfoContext(ctx, "invoice line stepped to PURCHASED", "invoiceId", d.InvoiceID, "receiptId", receiptID, "lineNo", lineNo, "transactionId", row.ID,
			"statusChangeId", change.ID, "costAmount", cost.String(), "ledgerEntryId", d.LedgerEntryID, "applied", applied, "eventId", evt.ID)
	}
	return stepped, nil
}

func bookInvoice(ctx context.Context, s PostingsStore, evt events.Event) error {
	var d invoicePostedData
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return eventing.Permanent(fmt.Errorf("invoice.posted data: %w", err))
	}
	ctx, err := postingsScope(ctx, evt, ReceiptSource, d.LegalEntityID)
	if err != nil {
		return err
	}
	if _, err := postingDate(d.PostingDate, evt); err != nil || !ids.Valid(d.InvoiceID) || !ids.Valid(d.LedgerEntryID) || !currencyCode.MatchString(d.Currency) {
		return eventing.Permanent(errors.New("invoice.posted needs invoice_id, ledger_entry_id, currency and a valid posting_date"))
	}
	if len(d.Lines) == 0 {
		slog.WarnContext(ctx, "invoice has no lines, nothing stepped", "invoiceId", d.InvoiceID, "eventId", evt.ID)
		return nil
	}
	var origins []string
	for _, l := range d.Lines {
		if ids.Valid(l.ReceiptID) && l.ReceiptLineNo > 0 {
			origins = append(origins, receiptLineSourceID(evt.TenantID, l.ReceiptID, l.ReceiptLineNo))
		}
	}
	bookings, err := s.LineBookings(ctx, strings.ToLower(d.LegalEntityID), sourceReceiptLine, origins)
	if err != nil {
		slog.ErrorContext(ctx, "read the booked receipt lines failed", "invoiceId", d.InvoiceID, "err", err, "eventId", evt.ID)
		return fmt.Errorf("read the booked receipt lines: %w", err)
	}
	var failed lineFailures
	stepped := 0
	for _, l := range d.Lines {
		what := "line " + strconv.Itoa(l.LineNo)
		qty, qerr := decimal.NewFromString(l.Qty)
		amount, aerr := decimal.NewFromString(l.Amount)
		if !ids.Valid(l.ReceiptID) || l.ReceiptLineNo < 1 || qerr != nil || !qty.IsPositive() || aerr != nil || amount.IsNegative() {
			failed.add(what, eventing.Permanent(errors.New("needs receipt_id, receipt_line_no, a positive qty and a non-negative amount")))
			continue
		}
		rows := bookings[receiptLineSourceID(evt.TenantID, l.ReceiptID, l.ReceiptLineNo)]
		if len(rows) == 0 {
			failed.add(what, fmt.Errorf("%s: receipt line %s/%d is not booked yet", CodePostingOriginalMissing, strings.ToLower(l.ReceiptID), l.ReceiptLineNo))
			continue
		}
		n, err := stepInvoiceLine(ctx, s, d, l.LineNo, l.ReceiptID, qty, amount, rows, evt)
		stepped += n
		if err != nil {
			failed.add(what, err)
		}
	}
	if err := failed.err(evt); err != nil {
		slog.WarnContext(ctx, "invoice lines not stepped", "invoiceId", d.InvoiceID, "stepped", stepped, "err", err, "eventId", evt.ID)
		return err
	}
	return nil
}
