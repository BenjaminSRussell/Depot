package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/receivables/internal/domain"
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
	id, postingDate string
}

type postedLeg struct {
	entryID, key, currency, suffix string
	lines                          []journalLine
}

const (
	reversalLedgerBudget = 4 * time.Second

	JournalReversalCompensation = "payment_application_reversal_compensation"
)

type ItemChange struct {
	OpenItemID string `json:"openItemId"`
	Cause      string `json:"cause"`
}

type ReverseEligibility struct {
	Eligible bool         `json:"eligible"`
	Code     string       `json:"code,omitempty"`
	Reason   string       `json:"reason,omitempty"`
	Items    []ItemChange `json:"items,omitempty"`
}

func (e ReverseEligibility) problem() *httpx.Problem {
	status := http.StatusConflict
	if e.Code == CodeReversalLedgerUnavailable {
		status = http.StatusServiceUnavailable
	}
	pr := httpx.NewProblem(status, e.Code, e.Reason)
	if len(e.Items) > 0 {
		pr.Errors = make(map[string]string, len(e.Items))
		for _, it := range e.Items {
			pr.Errors[it.OpenItemID] = it.Cause
		}
	}
	return pr
}

func (s *Postgres) ReverseApplication(ctx context.Context, paymentID string, version int, req ReverseApplicationRequest, idempotencyKey string) (ReverseApplicationResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out ReverseApplicationResult
	var pay Payment
	var app appReversal
	var posted []postedLeg
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		posted = nil
		var err error
		pay, err = scanPayment(tx.QueryRow(ctx, `select `+paymentColumns+` from payment where id = $1 for update`, paymentID))
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
		var eligibility ReverseEligibility
		app, eligibility, err = reverseGuards(ctx, tx, p, pay, req.ApplicationID)
		if err != nil {
			return err
		}
		if !eligibility.Eligible {
			return eligibility.problem()
		}
		u, err := reverseApplicationRows(ctx, tx, p, pay, app.id)
		if err != nil {
			return err
		}
		ledgerCtx, cancel := context.WithTimeout(ctx, reversalLedgerBudget)
		defer cancel()
		posted, err = s.postApplicationLegs(ledgerCtx, u, pay, app)
		if err != nil {
			return err
		}
		if err := finishApplicationReversal(ctx, tx, p, u.Payment, app, posted[0].entryID, req.Reason, u.Reversed); err != nil {
			return err
		}
		out = ReverseApplicationResult{ApplicationID: app.id, ReversalJournalEntryID: posted[0].entryID, Payment: u.Payment, Reopened: u.Reversed}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	if err != nil && len(posted) > 0 {
		s.compensateReversalLegs(ctx, p, pay, app, posted)
	}
	return out, mapWriteErr(err)
}

func (s *Postgres) compensateReversalLegs(ctx context.Context, p tenant.Principal, pay Payment, app appReversal, legs []postedLeg) {
	detached := context.WithoutCancel(ctx)
	for _, leg := range legs {
		flipped := make([]journalLine, len(leg.lines))
		for i, l := range leg.lines {
			l.Credit = !l.Credit
			flipped[i] = l
		}
		err := dbx.WithTenantTx(detached, s.pool, func(tx pgx.Tx) error {
			return insertJournal(detached, tx, p.TenantID, journalSpec{LegalEntityID: pay.LegalEntityID, SourceType: JournalReversalCompensation, SourceID: pay.ID,
				SourceKey: "reversal-compensation:" + leg.entryID, DocumentID: "PAY-" + pay.ID + "-APP-REVERSAL-COMPENSATION" + leg.suffix,
				PostingDate: app.postingDate, Currency: leg.currency, Lines: flipped}, flipped)
		})
		if err != nil {
			slog.ErrorContext(ctx, "payment application reversal ledger entry left behind and its compensation not recorded; reverse it by hand",
				"application", app.id, "entry", leg.entryID, "ledgerKey", leg.key, "err", err, "correlationId", middleware.GetReqID(ctx))
			continue
		}
		slog.WarnContext(ctx, "payment application reversal failed after its ledger entry posted; compensating journal queued",
			"application", app.id, "entry", leg.entryID, "ledgerKey", leg.key, "correlationId", middleware.GetReqID(ctx))
	}
}

func reverseApplicationHash(paymentID string, version int, req ReverseApplicationRequest) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{"reverse-application", paymentID, strconv.Itoa(version), req.ApplicationID, req.Reason}, "|")))
	return hex.EncodeToString(sum[:])
}

func (s *Postgres) CheckReverseApplication(ctx context.Context, paymentID string, req ReverseApplicationRequest) (ReverseEligibility, error) {
	p, _ := tenant.FromContext(ctx)
	var out ReverseEligibility
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		pay, err := scanPayment(tx.QueryRow(ctx, `select `+paymentColumns+` from payment where id = $1`, paymentID))
		if err != nil {
			return notFound(err, CodePaymentNotFound, "payment not found")
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		if lockErr := lockApplicationItems(ctx, sp, p.TenantID, pay.ID, req.ApplicationID); lockErr != nil {
			_ = sp.Rollback(ctx)
			var pr *httpx.Problem
			if errors.As(lockErr, &pr) && pr.Code == domain.CodeItemLocked {
				out = ReverseEligibility{Code: pr.Code, Reason: pr.Detail}
				return nil
			}
			return lockErr
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
		_, out, err = reverseGuards(ctx, tx, p, pay, req.ApplicationID)
		return err
	})
	return out, mapWriteErr(err)
}

func lockApplicationItems(ctx context.Context, tx pgx.Tx, tenantID, paymentID, applicationID string) error {
	rows, err := tx.Query(ctx, `select oi.id from open_item oi where oi.tenant_id = $1 and oi.id in
		(select s.open_item_id from settlement s where s.tenant_id = $1 and s.payment_id = $2 and s.application_id = $3 and s.reversal_of_id is null)
		order by oi.id for update of oi nowait`, tenantID, paymentID, applicationID)
	if err == nil {
		for rows.Next() {
		}
		err = rows.Err()
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == lockNotAvailable {
		return domainProblem(domain.CodeItemLocked, "an open item of the application is locked by another user")
	}
	return err
}

func reverseGuards(ctx context.Context, tx pgx.Tx, p tenant.Principal, pay Payment, applicationID string) (appReversal, ReverseEligibility, error) {
	if err := lockApplicationItems(ctx, tx, p.TenantID, pay.ID, applicationID); err != nil {
		return appReversal{id: applicationID}, ReverseEligibility{}, err
	}
	app := appReversal{id: applicationID}
	var status string
	if err := tx.QueryRow(ctx, `select status, posting_date::text from payment_application where tenant_id = $1 and id = $2 and payment_id = $3`,
		p.TenantID, applicationID, pay.ID).Scan(&status, &app.postingDate); err != nil {
		return app, ReverseEligibility{}, notFound(err, CodePaymentNotFound, "payment application not found")
	}
	refuse := func(code, reason string, items []ItemChange) (appReversal, ReverseEligibility, error) {
		return app, ReverseEligibility{Code: code, Reason: reason, Items: items}, nil
	}
	if status != "APPLIED" {
		return refuse(CodePaymentState, "the application is already reversed", nil)
	}
	if pay.Status != "APPLIED" && pay.Status != "ON_ACCOUNT" {
		return refuse(CodePaymentState, "payment in status "+pay.Status+" has no application to reverse", nil)
	}
	if bankReconciledOrUnknown(ctx, tx, p.TenantID, pay.ID) {
		return refuse(CodeReversalReconciled, "the payment is bank-reconciled or its reconciliation state cannot be determined", nil)
	}
	items, err := changedItems(ctx, tx, p.TenantID, pay.ID, applicationID)
	if err != nil {
		return app, ReverseEligibility{}, err
	}
	if len(items) > 0 {
		return refuse(CodeReversalItemChanged, "an item of the application was touched since it was applied", items)
	}
	unposted, err := applyJournalsUnposted(ctx, tx, p.TenantID, pay.ID)
	if err != nil {
		return app, ReverseEligibility{}, err
	}
	if unposted {
		return refuse(CodeReversalLedgerUnavailable, "the application's own journal is not posted to the ledger yet; retry with the same Idempotency-Key", nil)
	}
	return app, ReverseEligibility{Eligible: true}, nil
}

func bankReconciledOrUnknown(ctx context.Context, tx pgx.Tx, tenantID, paymentID string) bool {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return true
	}
	var reconciled bool
	if err := sp.QueryRow(ctx, `select bank_reconciled_at is not null from payment where tenant_id = $1 and id = $2`, tenantID, paymentID).Scan(&reconciled); err != nil {
		slog.ErrorContext(ctx, "payment reconciliation state not readable", "payment", paymentID, "err", err, "correlationId", middleware.GetReqID(ctx))
		_ = sp.Rollback(ctx)
		return true
	}
	return sp.Commit(ctx) != nil || reconciled
}

func changedItems(ctx context.Context, tx pgx.Tx, tenantID, paymentID, applicationID string) ([]ItemChange, error) {
	rows, err := tx.Query(ctx, `
		with ap as (select applied_at from payment_application where tenant_id = $1 and id = $3),
		it as (select distinct open_item_id from settlement where tenant_id = $1 and application_id = $3 and reversal_of_id is null)
		select it.open_item_id::text, cause.name from it cross join ap
		cross join lateral (values
		  ('DUNNED', exists (select 1 from dunning_letter l where l.tenant_id = $1 and l.status = 'ISSUED' and l.issued_at > ap.applied_at
		    and l.items @> jsonb_build_array(jsonb_build_object('openItemId', it.open_item_id::text)))),
		  ('CLEARED', exists (select 1 from down_payment_clearing c where c.tenant_id = $1 and c.open_item_id = it.open_item_id and c.cleared_at > ap.applied_at)
		    or exists (select 1 from credit_settlement c where c.tenant_id = $1 and c.debit_item_id = it.open_item_id and c.settled_at > ap.applied_at)
		    or exists (select 1 from deduction d where d.tenant_id = $1 and d.open_item_id = it.open_item_id and d.created_at > ap.applied_at)),
		  ('FURTHER_SETTLED', exists (select 1 from settlement s where s.tenant_id = $1 and s.open_item_id = it.open_item_id and s.reversal_of_id is null
		    and s.payment_id <> $2 and s.settled_at > ap.applied_at)),
		  ('LATER_APPLICATION', exists (select 1 from payment_application a where a.tenant_id = $1 and a.payment_id = $2 and a.id <> $3
		    and a.status = 'APPLIED' and a.applied_at > ap.applied_at
		    and exists (select 1 from settlement ls where ls.tenant_id = $1 and ls.application_id = a.id and ls.reversal_of_id is null
		      and not exists (select 1 from settlement lr where lr.tenant_id = ls.tenant_id and lr.reversal_of_id = ls.id))))
		) as cause(name, hit)
		where cause.hit
		order by it.open_item_id, cause.name`, tenantID, paymentID, applicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ItemChange
	for rows.Next() {
		var c ItemChange
		if err := rows.Scan(&c.OpenItemID, &c.Cause); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func applyJournalsUnposted(ctx context.Context, tx pgx.Tx, tenantID, paymentID string) (bool, error) {
	var pending bool
	err := tx.QueryRow(ctx, `select exists (select 1 from gl_journal where tenant_id = $1 and source_id = $2 and status = 'PENDING'
		and source_key ~ '^(apply|fx):')`, tenantID, paymentID).Scan(&pending)
	return pending, err
}

func (s *Postgres) postApplicationLegs(ctx context.Context, u unapplied, pay Payment, app appReversal) ([]postedLeg, error) {
	attempt := ids.New()
	main, err := s.postApplicationReversal(ctx, pay, app, pay.Currency, "", attempt, u.main)
	if err != nil {
		return nil, err
	}
	legs := []postedLeg{main}
	if u.fxLines == nil {
		return legs, nil
	}
	fx, err := s.postApplicationReversal(ctx, pay, app, u.fxCurrency, "-fx", attempt, u.fxLines)
	if err != nil {
		slog.ErrorContext(ctx, "payment application reversal fx leg not posted after the main leg", "application", app.id, "postedEntry", main.entryID,
			"err", err, "correlationId", middleware.GetReqID(ctx))
		return legs, err
	}
	return append(legs, fx), nil
}

func (s *Postgres) postApplicationReversal(ctx context.Context, pay Payment, app appReversal, currency, suffix, attempt string, lines []journalLine) (postedLeg, error) {
	lines = slices.DeleteFunc(lines, func(l journalLine) bool { return !l.Amount.IsPositive() })
	ledgerID := s.journalLedger(pay.LegalEntityID)
	if s.ledger == nil || ledgerID == "" {
		slog.ErrorContext(ctx, "payment application reversal not posted: ledger not configured", "application", app.id, "correlationId", middleware.GetReqID(ctx))
		return postedLeg{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeReversalLedgerUnavailable, "the ledger is not available; retry with the same Idempotency-Key")
	}
	started := time.Now()
	key := "post:receivables:paymentapplication:" + app.id + ":reversal" + suffix + ":" + attempt
	id, err := s.postLedgerEntry(ctx, ledgerEntry{legalEntityID: pay.LegalEntityID, ledgerID: ledgerID, documentID: "PAY-" + pay.ID + "-APP-REVERSAL" + suffix,
		postingDate: app.postingDate, currency: currency, key: key,
		sourceType: "receivables-payment_application_reversal"}, lines)
	if err != nil {
		slog.ErrorContext(ctx, "payment application reversal not posted", "application", app.id, "leg", suffix, "ledgerKey", key, "durationMs", time.Since(started).Milliseconds(),
			"err", err, "correlationId", middleware.GetReqID(ctx))
		if ledgerclient.CodeOf(err) == ledgerclient.CodePeriodClosed {
			return postedLeg{}, httpx.NewProblem(http.StatusConflict, CodeReversalPeriodClosed, "the posting period of the application is closed; the reversal is not re-dated")
		}
		return postedLeg{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeReversalLedgerUnavailable, "the ledger refused or could not be reached; retry with the same Idempotency-Key")
	}
	slog.InfoContext(ctx, "payment application reversal posted", "application", app.id, "leg", suffix, "ledgerKey", key, "entry", id, "durationMs", time.Since(started).Milliseconds(),
		"correlationId", middleware.GetReqID(ctx))
	return postedLeg{entryID: id, key: key, currency: currency, suffix: suffix, lines: lines}, nil
}

func finishApplicationReversal(ctx context.Context, tx pgx.Tx, p tenant.Principal, pay Payment, app appReversal, entryID, reason string, reopened []UnappliedSettlement) error {
	if _, err := tx.Exec(ctx, `update payment_application set status = 'REVERSED', reversed_by = $3, reversed_at = now(), reversal_reason = $4,
		reversal_journal_entry_id = $5 where tenant_id = $1 and id = $2 and status = 'APPLIED'`, p.TenantID, app.id, p.UserID, reason, entryID); err != nil {
		return err
	}
	itemIDs := make([]string, 0, len(reopened))
	for _, r := range reopened {
		itemIDs = append(itemIDs, r.OpenItemID)
	}
	return enqueue(ctx, tx, "payment", pay.ID, rcvevents.PaymentApplicationReversed, p.TenantID, rcvevents.PaymentApplicationReversedData{
		PaymentID: pay.ID, ApplicationID: app.id, LegalEntityID: pay.LegalEntityID, ReversalJournalEntryID: entryID, ReopenedItemIDs: itemIDs, Reason: reason,
	})
}
