package ledgerclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/shopspring/decimal"
)

const partnerBalancesPath = "/v1/ledger/balances/by-partner"

// PartnerBalanceQuery selects the intercompany balances of one ledger by account and trading partner (#40).
type PartnerBalanceQuery struct {
	LegalEntityID string
	LedgerID      string
	// Account and AccountTo bound the accounts read; both empty reads every account that has a partner line.
	Account          string
	AccountTo        string
	PeriodFrom       string
	PeriodTo         string
	TradingPartnerID string
}

// PartnerBalance is one account's balance with one counterparty.
type PartnerBalance struct {
	Account          string
	TradingPartnerID string
	Debit            decimal.Decimal
	Credit           decimal.Decimal
}

func (b PartnerBalance) Net() decimal.Decimal { return b.Debit.Sub(b.Credit) }

// PartnerBalances is what the ledger answered: the ledger currency and one row per account and partner.
type PartnerBalances struct {
	Currency string
	Rows     []PartnerBalance
}

// PartnerReader reads the intercompany balances by trading partner; *Client and *Fake implement it.
type PartnerReader interface {
	PartnerBalances(ctx context.Context, q PartnerBalanceQuery) (PartnerBalances, error)
}

type partnerBalancesBody struct {
	Currency string `json:"currency"`
	Rows     []struct {
		Account          string `json:"account"`
		TradingPartnerID string `json:"tradingPartnerId"`
		Debit            string `json:"debit"`
		Credit           string `json:"credit"`
	} `json:"rows"`
}

// PartnerBalances returns the lines that carry a trading partner, summed per account and partner.
func (c *Client) PartnerBalances(ctx context.Context, q PartnerBalanceQuery) (PartnerBalances, error) {
	query := url.Values{"ledger": {q.LedgerID}}
	for k, v := range map[string]string{"account_from": q.Account, "account_to": q.AccountTo, "period_from": q.PeriodFrom, "period_to": q.PeriodTo, "trading_partner": q.TradingPartnerID} {
		if v != "" {
			query.Set(k, v)
		}
	}
	raw, err := c.do(ctx, "partner-balance", q.LedgerID, http.MethodGet, partnerBalancesPath, query, nil, "", q.LegalEntityID, DutyBalanceRead)
	if err != nil {
		return PartnerBalances{}, err
	}
	var body partnerBalancesBody
	if err := json.Unmarshal(raw, &body); err != nil || body.Currency == "" {
		return PartnerBalances{}, errors.New("ledger partner balance: response has no currency")
	}
	out := PartnerBalances{Currency: body.Currency}
	for _, r := range body.Rows {
		d, derr := decimal.NewFromString(r.Debit)
		cr, cerr := decimal.NewFromString(r.Credit)
		if err := errors.Join(derr, cerr); err != nil {
			return PartnerBalances{}, fmt.Errorf("ledger partner balance: %w", err)
		}
		out.Rows = append(out.Rows, PartnerBalance{Account: r.Account, TradingPartnerID: r.TradingPartnerID, Debit: d, Credit: cr})
	}
	return out, nil
}

// PartnerBalances sums the posted partner lines per account and partner, as the ledger does (an empty bound is open).
func (f *Fake) PartnerBalances(_ context.Context, q PartnerBalanceQuery) (PartnerBalances, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	type key struct{ account, partner string }
	sums := map[key]*PartnerBalance{}
	out := PartnerBalances{Currency: "USD"}
	for _, s := range f.entries {
		period := s.PostingDate[:min(len(s.PostingDate), len("2006-01"))]
		if s.LedgerID != q.LedgerID || (q.PeriodTo != "" && period > q.PeriodTo) || (q.PeriodFrom != "" && period < q.PeriodFrom) {
			continue
		}
		out.Currency = s.Currency
		for _, l := range s.Lines {
			if l.TradingPartnerID == "" || (q.TradingPartnerID != "" && l.TradingPartnerID != q.TradingPartnerID) ||
				(q.Account != "" && l.Account < q.Account) || (q.AccountTo != "" && l.Account > q.AccountTo) {
				continue
			}
			k := key{l.Account, l.TradingPartnerID}
			b := sums[k]
			if b == nil {
				b = &PartnerBalance{Account: l.Account, TradingPartnerID: l.TradingPartnerID}
				sums[k] = b
			}
			if l.Credit {
				b.Credit = b.Credit.Add(l.Amount)
			} else {
				b.Debit = b.Debit.Add(l.Amount)
			}
		}
	}
	for _, b := range sums {
		out.Rows = append(out.Rows, *b)
	}
	slices.SortFunc(out.Rows, func(a, b PartnerBalance) int {
		if a.Account != b.Account {
			return map[bool]int{true: -1, false: 1}[a.Account < b.Account]
		}
		return map[bool]int{true: -1, false: 1}[a.TradingPartnerID < b.TradingPartnerID]
	})
	return out, nil
}
