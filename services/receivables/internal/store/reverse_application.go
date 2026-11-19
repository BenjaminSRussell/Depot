package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	rcvevents "github.com/BenjaminSRussell/depot/services/receivables/internal/events"
)

type ReverseApplicationRequest struct {
	ApplicationID string
	Reason        string
}

type ReverseApplicationResult struct {
	ApplicationID          string                `json:"applicationId"`
	ReversalJournalEntryID string                `json:"reversalJournalEntryId"`
	Payment                Payment               `json:"payment"`
	Reopened               []UnappliedSettlement `json:"reopened"`
}

type appReversal struct {
	id, postingDate, entryID string
}

func (s *Postgres) ReverseApplication(ctx context.Context, paymentID string, version int, req ReverseApplicationRequest, idempotencyKey string) (ReverseApplicationResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out ReverseApplicationResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		pay, err := scanPayment(tx.QueryRow(ctx, `select `+paymentColumns+` from payment where id = $1 for update`, paymentID))
		if err != nil {
			return notFound(err, CodePaymentNotFound, "payment not found")
		}
		hash := reverseApplicationHash(paymentID, version, req)
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if pay.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "payment changed since it was read")
		}
		app, err := reverseGuards(ctx, tx, p, pay, req.ApplicationID)
		if err != nil {
			return err
		}
		var res UnapplyResult
		if err := s.unapplyRows(ctx, tx, p, pay, req.Reason, "", &app, &res); err != nil {
			return err
		}
		out = ReverseApplicationResult{ApplicationID: app.id, ReversalJournalEntryID: app.entryID, Payment: res.Payment, Reopened: res.Reversed}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, mapWriteErr(err)
}

func reverseApplicationHash(paymentID string, version int, req ReverseApplicationRequest) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{"reverse-application", paymentID, strconv.Itoa(version), req.ApplicationID, req.Reason}, "|")))
	return hex.EncodeToString(sum[:])
}

func reverseGuards(ctx context.Context, tx pgx.Tx, p tenant.Principal, pay Payment, applicationID string) (appReversal, error) {
	app := appReversal{id: applicationID}
	var status string
	if err := tx.QueryRow(ctx, `select status, posting_date::text from payment_application where tenant_id = $1 and id = $2 and payment_id = $3`,
		p.TenantID, applicationID, pay.ID).Scan(&status, &app.postingDate); err != nil {
		return app, notFound(err, CodePaymentNotFound, "payment application not found")
	}
	if status != "APPLIED" {
		return app, httpx.NewProblem(http.StatusConflict, CodePaymentState, "the application is already reversed")
	}
	if pay.Status != "APPLIED" && pay.Status != "ON_ACCOUNT" {
		return app, httpx.NewProblem(http.StatusConflict, CodePaymentState, "payment in status "+pay.Status+" has no application to reverse")
	}
	var reconciled bool
	if err := tx.QueryRow(ctx, `select bank_reconciled_at is not null from payment where tenant_id = $1 and id = $2`, p.TenantID, pay.ID).Scan(&reconciled); err != nil || reconciled {
		return app, httpx.NewProblem(http.StatusConflict, CodeReversalReconciled, "the payment is bank-reconciled or its reconciliation state cannot be determined")
	}
	return app, nil
}

func (s *Postgres) postApplicationReversal(ctx context.Context, pay Payment, app *appReversal, lines []journalLine) error {
	lines = slices.DeleteFunc(lines, func(l journalLine) bool { return !l.Amount.IsPositive() })
	ledgerID := s.journalLedger(pay.LegalEntityID)
	if s.ledger == nil || ledgerID == "" {
		slog.ErrorContext(ctx, "payment application reversal not posted: ledger not configured", "application", app.id, "correlationId", middleware.GetReqID(ctx))
		return httpx.NewProblem(http.StatusServiceUnavailable, CodeReversalLedgerUnavailable, "the ledger is not available; retry with the same Idempotency-Key")
	}
	id, err := s.postLedgerEntry(ctx, ledgerEntry{legalEntityID: pay.LegalEntityID, ledgerID: ledgerID, documentID: "PAY-" + pay.ID + "-APP-REVERSAL",
		postingDate: app.postingDate, currency: pay.Currency, key: "post:receivables:paymentapplication:" + app.id + ":reversal",
		sourceType: "receivables-payment_application_reversal"}, lines)
	if err != nil {
		slog.ErrorContext(ctx, "payment application reversal not posted", "application", app.id, "err", err, "correlationId", middleware.GetReqID(ctx))
		if ledgerclient.CodeOf(err) == ledgerclient.CodePeriodClosed {
			return httpx.NewProblem(http.StatusConflict, CodeReversalPeriodClosed, "the posting period of the application is closed; the reversal is not re-dated")
		}
		return httpx.NewProblem(http.StatusServiceUnavailable, CodeReversalLedgerUnavailable, "the ledger refused or could not be reached; retry with the same Idempotency-Key")
	}
	app.entryID = id
	return nil
}

func finishApplicationReversal(ctx context.Context, tx pgx.Tx, p tenant.Principal, pay Payment, app *appReversal, reason string, reopened []UnappliedSettlement) error {
	if _, err := tx.Exec(ctx, `update payment_application set status = 'REVERSED', reversed_by = $3, reversed_at = now(), reversal_reason = $4,
		reversal_journal_entry_id = $5 where tenant_id = $1 and id = $2 and status = 'APPLIED'`, p.TenantID, app.id, p.UserID, reason, app.entryID); err != nil {
		return err
	}
	itemIDs := make([]string, 0, len(reopened))
	for _, r := range reopened {
		itemIDs = append(itemIDs, r.OpenItemID)
	}
	return enqueue(ctx, tx, "payment", pay.ID, rcvevents.PaymentApplicationReversed, p.TenantID, rcvevents.PaymentApplicationReversedData{
		PaymentID: pay.ID, ApplicationID: app.id, LegalEntityID: pay.LegalEntityID, ReversalJournalEntryID: app.entryID, ReopenedItemIDs: itemIDs, Reason: reason,
	})
}
