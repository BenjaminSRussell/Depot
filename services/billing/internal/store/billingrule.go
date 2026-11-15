package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	billingevents "github.com/BenjaminSRussell/passage/services/billing/internal/events"
)

// Billing rules of a customer (#35, 35-5).
const (
	RuleStandard   = "STANDARD"
	RuleSelfBilled = "SELF_BILLED"
)

// ErrRuleVersion is a rule changed by someone else since the caller read it.
var ErrRuleVersion = errors.New("the billing rule was changed since it was read")

// BillingRule is how a customer of a legal entity is invoiced. SELF_BILLED, between ValidFrom and ValidTo (inclusive, open-ended
// without ValidTo), means the customer self-bills under AgreementRef and billing proposes no invoice for its shipments.
type BillingRule struct {
	LegalEntityID string     `json:"legalEntityId"`
	CustomerID    string     `json:"customerId"`
	Rule          string     `json:"rule"`
	AgreementRef  string     `json:"agreementRef"`
	ValidFrom     *time.Time `json:"validFrom"`
	ValidTo       *time.Time `json:"validTo"`
	Version       int        `json:"version"`
	UpdatedBy     string     `json:"updatedBy"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// SelfBilledOn reports whether the rule makes the customer self-billing on the day.
func (r BillingRule) SelfBilledOn(day time.Time) bool {
	if r.Rule != RuleSelfBilled || r.ValidFrom == nil || day.Before(*r.ValidFrom) {
		return false
	}
	return r.ValidTo == nil || !day.After(*r.ValidTo)
}

const ruleColumns = `legal_entity_id::text, customer_id::text, rule, agreement_ref, valid_from, valid_to, version, updated_by, updated_at`

func scanRule(row pgx.Row) (BillingRule, error) {
	var r BillingRule
	err := row.Scan(&r.LegalEntityID, &r.CustomerID, &r.Rule, &r.AgreementRef, &r.ValidFrom, &r.ValidTo, &r.Version, &r.UpdatedBy, &r.UpdatedAt)
	return r, err
}

// PutBillingRule sets the rule. ifMatch is the version the caller read, 0 for a customer without a rule yet; another version is
// ErrRuleVersion.
func (s *Postgres) PutBillingRule(ctx context.Context, r BillingRule, ifMatch int) (BillingRule, error) {
	p, _ := tenant.FromContext(ctx)
	var out BillingRule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if ifMatch == 0 {
			out, err = scanRule(tx.QueryRow(ctx, `insert into billing_rule (tenant_id, legal_entity_id, customer_id, rule, agreement_ref, valid_from, valid_to, updated_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8) on conflict (tenant_id, legal_entity_id, customer_id) do nothing returning `+ruleColumns,
				p.TenantID, r.LegalEntityID, r.CustomerID, r.Rule, r.AgreementRef, r.ValidFrom, r.ValidTo, r.UpdatedBy))
		} else {
			out, err = scanRule(tx.QueryRow(ctx, `update billing_rule set rule = $3, agreement_ref = $4, valid_from = $5, valid_to = $6, updated_by = $7,
				version = version + 1, updated_at = now() where legal_entity_id = $1 and customer_id = $2 and version = $8 returning `+ruleColumns,
				r.LegalEntityID, r.CustomerID, r.Rule, r.AgreementRef, r.ValidFrom, r.ValidTo, r.UpdatedBy, ifMatch))
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRuleVersion
		}
		return err
	})
	return out, err
}

// GetBillingRule returns the customer's rule; a customer without one is STANDARD with found false.
func (s *Postgres) GetBillingRule(ctx context.Context, legalEntityID, customerID string) (BillingRule, bool, error) {
	out := BillingRule{LegalEntityID: legalEntityID, CustomerID: customerID, Rule: RuleStandard}
	found := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := scanRule(tx.QueryRow(ctx, `select `+ruleColumns+` from billing_rule where legal_entity_id = $1 and customer_id = $2`, legalEntityID, customerID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err == nil {
			out, found = r, true
		}
		return err
	})
	return out, found, err
}

// SelfBilledKeyPrefix starts the Idempotency-Key of the legal invoice billing posts from a self-billed document the customer sent
// (#35, 35-5): the key is the sender's document (the EDI message), so the same document posts once.
const SelfBilledKeyPrefix = "self-billed:"

// ErrSelfBilledKeyReuse is a self-billed document received again with different content.
var ErrSelfBilledKeyReuse = errors.New("the self-billed document was already posted with different content")

// PostSelfBilled stores inv and posts it in one transaction as the legal invoice of the customer's own document: its number is the
// document's number (the customer's, under the self-billing agreement), not one drawn from billing's sequence. The same
// IdempotencyKey with the same RequestHash returns the posted invoice with created false; with another hash it is
// ErrSelfBilledKeyReuse. inv carries the lines with their nets and taxes, the totals and its PostingDate; invoice.posted is enqueued
// in the same transaction, so receivables and the ledger see it like any other posted invoice.
func (s *Postgres) PostSelfBilled(ctx context.Context, inv Invoice, number string) (Invoice, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out Invoice
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "billing:self-billed:"+p.TenantID+":"+inv.IdempotencyKey); err != nil {
			return err
		}
		prev, err := loadInvoice(ctx, tx, `where idempotency_key = $1`, inv.IdempotencyKey)
		switch {
		case err == nil:
			if prev.RequestHash != inv.RequestHash {
				return ErrSelfBilledKeyReuse
			}
			out = prev
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		if err := writeProposal(ctx, tx, inv); err != nil {
			return writeProblem(err)
		}
		inv.Number, inv.FiscalYear, inv.Status = number, inv.PostingDate.Year(), domain.StatusPosted
		if _, err := tx.Exec(ctx, `update invoice set status = 'POSTED', invoice_number = $2, fiscal_year = $3, posting_date = $4, version = version + 1 where id = $1`,
			inv.ID, number, inv.FiscalYear, inv.PostingDate.Format(dateLayout)); err != nil {
			return writeProblem(err)
		}
		inv.Version = 2
		evt, err := events.New(billingevents.InvoicePosted, eventSource, inv.ID, p.TenantID, postedData(inv))
		if err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, tx, "invoice", inv.ID, evt); err != nil {
			return err
		}
		if err := queueSalesReports(ctx, tx, p.TenantID, inv); err != nil {
			return err
		}
		out, created = inv, true
		return nil
	})
	return out, created, err
}
