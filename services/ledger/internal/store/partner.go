package store

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

// partnerViolations refuses lines whose trading partner (#40) is not a legal entity of the tenant. A legal entity is known to the
// ledger by owning a ledger of the tenant. The check reads the ledger table across the caller's legal-entity scope, because an
// entity-scoped poster (an intercompany service principal posts for one side) cannot see the partner's ledger rows; it only learns
// whether the id exists in its tenant, never any data of the partner. The scope is restored before the posting continues.
func partnerViolations(ctx context.Context, tx pgx.Tx, tenantID string, lines []domain.ValidLine) ([]domain.Violation, error) {
	var partners []string
	for _, l := range lines {
		if l.TradingPartner != "" && !slices.Contains(partners, l.TradingPartner) {
			partners = append(partners, l.TradingPartner)
		}
	}
	if len(partners) == 0 {
		return nil, nil
	}
	var scopes string
	if err := tx.QueryRow(ctx, `select coalesce(current_setting('passage.legal_entity_scopes', true), '')`).Scan(&scopes); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `select set_config('passage.legal_entity_scopes', '*', true)`); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `select distinct legal_entity_id::text from ledger where tenant_id = $1 and legal_entity_id = any($2::text[]::uuid[])`, tenantID, partners)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		known[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `select set_config('passage.legal_entity_scopes', $1, true)`, scopes); err != nil {
		return nil, err
	}
	var out []domain.Violation
	for _, l := range lines {
		if l.TradingPartner != "" && !known[l.TradingPartner] {
			idx := l.Index
			out = append(out, domain.Violation{Code: domain.CodeTradingPartnerInvalid, Field: "lines.tradingPartnerId", LineIndex: &idx,
				Message: fmt.Sprintf("trading partner %s is not a legal entity of this tenant", l.TradingPartner)})
		}
	}
	return out, nil
}

// PartnerBalanceFilter selects the lines PartnerBalances sums.
type PartnerBalanceFilter struct {
	LedgerID, PeriodFrom, PeriodTo, AccountFrom, AccountTo string
	// TradingPartnerID keeps one counterparty; empty keeps every counterparty.
	TradingPartnerID string
}

// PartnerBalanceRow is one account's balance with one counterparty, in the ledger currency.
type PartnerBalanceRow struct {
	Account          string `json:"account"`
	TradingPartnerID string `json:"tradingPartnerId"`
	Debit            string `json:"debit"`
	Credit           string `json:"credit"`
	Net              string `json:"net"`
	LineCount        int64  `json:"lineCount"`
}

// PartnerBalances is the intercompany balances of a ledger by account and trading partner.
type PartnerBalances struct {
	LedgerID string              `json:"ledgerId"`
	Currency string              `json:"currency"`
	Rows     []PartnerBalanceRow `json:"rows"`
}

// PartnerBalances sums the lines that carry a trading partner per account and partner (#40). It reads the journal (only
// intercompany lines have a partner, and a partial index covers them) with the same sign rule as the balance projection: a negative
// posting reduces the original side. Rows sum to a subset of the account's balance in the trial balance, never more.
func (s *Postgres) PartnerBalances(ctx context.Context, f PartnerBalanceFilter) (PartnerBalances, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return PartnerBalances{}, err
	}
	out := PartnerBalances{LedgerID: f.LedgerID, Rows: []PartnerBalanceRow{}}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		l, err := loadLedger(ctx, tx, p.TenantID, f.LedgerID, false)
		if err != nil {
			return err
		}
		out.Currency = l.Currency
		rows, err := tx.Query(ctx, `select a.account_number, l.trading_partner_id::text,
				sum(case when l.debit_credit = 'D' and not l.negative_posting then l.amount_acct
					when l.debit_credit = 'C' and l.negative_posting then -l.amount_acct else 0 end)::text,
				sum(case when l.debit_credit = 'C' and not l.negative_posting then l.amount_acct
					when l.debit_credit = 'D' and l.negative_posting then -l.amount_acct else 0 end)::text,
				count(*)::bigint
			from journal_line l
			join journal_entry e on e.tenant_id = l.tenant_id and e.id = l.entry_id
			join main_account a on a.tenant_id = l.tenant_id and a.id = l.main_account_id
			where l.tenant_id = $1 and e.ledger_id = $2 and l.trading_partner_id is not null
				and ($3::text = '' or e.period_key >= $3::text) and ($4::text = '' or e.period_key <= $4::text)
				and ($5::text = '' or a.account_number >= $5::text) and ($6::text = '' or a.account_number <= $6::text)
				and ($7::text = '' or l.trading_partner_id::text = $7::text)
			group by a.account_number, l.trading_partner_id order by a.account_number, l.trading_partner_id`,
			p.TenantID, f.LedgerID, f.PeriodFrom, f.PeriodTo, f.AccountFrom, f.AccountTo, f.TradingPartnerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		minor := money.MinorUnits(l.Currency)
		for rows.Next() {
			var r PartnerBalanceRow
			var d, c string
			if err := rows.Scan(&r.Account, &r.TradingPartnerID, &d, &c, &r.LineCount); err != nil {
				return err
			}
			dv, cv := decimal.RequireFromString(d), decimal.RequireFromString(c)
			r.Debit, r.Credit, r.Net = dv.StringFixed(minor), cv.StringFixed(minor), dv.Sub(cv).StringFixed(minor)
			out.Rows = append(out.Rows, r)
		}
		return rows.Err()
	})
	return out, err
}
