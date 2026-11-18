package store

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/money"
	"github.com/BenjaminSRussell/depot/services/ledger/internal/domain"
)

const (
	DutyRateOverride       = "ledger.journal.rateoverride"
	CodeRateOverrideReason = "LEDGER_RATE_OVERRIDE_REASON"
	rateScale              = 12
)

func requireRateOverride(in PostInput) error {
	if in.RateOverride == nil {
		return nil
	}
	if !in.CanOverrideRate {
		return httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "a rate override needs duty "+DutyRateOverride)
	}
	if strings.TrimSpace(in.RateOverrideReason) == "" {
		return setViolation(CodeRateOverrideReason, "rateOverride.reason", "a rate override needs a reason")
	}
	return nil
}

func validCurrency(c string) bool { return len(c) == 3 && c == strings.ToUpper(c) }

func reportingCurrency(ledger Ledger) string {
	if ledger.ReportingCurrency == nil || *ledger.ReportingCurrency == "" {
		return ledger.Currency
	}
	return *ledger.ReportingCurrency
}

func foreignCurrencies(ledger Ledger, in PostInput) []string {
	seen := map[string]bool{}
	collect := func(lines []domain.LineInput) {
		for _, l := range lines {
			if l.Currency != ledger.Currency && validCurrency(l.Currency) {
				seen[l.Currency] = true
			}
		}
	}
	collect(in.Lines)
	for _, lines := range in.LedgerLines {
		collect(lines)
	}
	return slices.Sorted(maps.Keys(seen))
}

func (s *Postgres) resolveLineRates(ctx context.Context, in PostInput) context.Context {
	src, _ := ctx.Value(rateSourceKey{}).(RateSource)
	p, err := principalOf(ctx)
	if src == nil || err != nil {
		return ctx
	}
	var ledger Ledger
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		ledger, err = loadLedger(ctx, tx, p.TenantID, in.LedgerID, false)
		return err
	}); err != nil {
		slog.WarnContext(ctx, "exchange rates not resolved before the transaction", "err", err, "correlationId", middleware.GetReqID(ctx))
		return ctx
	}
	resolved := map[string]resolvedRate{}
	lookup := func(from, to string) {
		key := rateKey(from, to, in.PostingDate)
		if _, done := resolved[key]; !done {
			rate, err := src.Rate(ctx, ledger.LegalEntityID, from, to, in.PostingDate)
			resolved[key] = resolvedRate{rate, err}
		}
	}
	if in.RateOverride == nil {
		for _, c := range foreignCurrencies(ledger, in) {
			lookup(c, ledger.Currency)
		}
	}
	if rc := reportingCurrency(ledger); rc != ledger.Currency {
		lookup(ledger.Currency, rc)
	}
	if len(resolved) == 0 {
		return ctx
	}
	return context.WithValue(ctx, resolvedRatesKey{}, resolved)
}

func lineRate(ctx context.Context, ledger Ledger, in PostInput, from string) (decimal.Decimal, error) {
	if in.RateOverride != nil {
		return in.RateOverride.Round(rateScale), nil
	}
	rate, err := rateFor(ctx, ledger, from, in.PostingDate)
	return rate.Round(rateScale), err
}

func applyFXStage(ctx context.Context, ledger Ledger, in PostInput) ([]domain.LineInput, error) {
	out := slices.Clone(in.Lines)
	minor := money.MinorUnits(ledger.Currency)
	rates := map[string]decimal.Decimal{}
	txnNet := map[string]decimal.Decimal{}
	var violations []domain.Violation
	for i, l := range out {
		txnNet[l.Currency] = txnNet[l.Currency].Add(signed(l.Side, l.Amount))
		if l.Currency == ledger.Currency || !validCurrency(l.Currency) {
			continue
		}
		rate, ok := rates[l.Currency]
		if !ok {
			var err error
			if rate, err = lineRate(ctx, ledger, in, l.Currency); err != nil {
				return nil, err
			}
			rates[l.Currency] = rate
		}
		if !l.Amount.Equal(l.Amount.Round(money.MinorUnits(l.Currency))) {
			idx := i
			violations = append(violations, domain.Violation{Code: "LEDGER_AMOUNT_SCALE", Field: "lines.amount.value", LineIndex: &idx,
				Message: fmt.Sprintf("amount %s has more decimals than %s allows", l.Amount.String(), l.Currency)})
			continue
		}
		out[i].CurrencyTxn, out[i].AmountTxn, out[i].RateTxnAcct = l.Currency, l.Amount, rate
		out[i].Amount, out[i].Currency = l.Amount.Mul(rate).Round(minor), ledger.Currency
		if in.RateOverride != nil {
			out[i].OverrideReason = in.RateOverrideReason
		}
	}
	if len(rates) == 0 && len(violations) == 0 {
		return out, nil
	}
	for _, c := range slices.Sorted(maps.Keys(txnNet)) {
		if !txnNet[c].IsZero() {
			violations = append(violations, domain.Violation{Code: "LEDGER_UNBALANCED", Field: "lines",
				Message: fmt.Sprintf("entry does not balance in %s: net %s", c, txnNet[c].String())})
		}
	}
	if len(violations) > 0 {
		return nil, &domain.RejectedError{Violations: violations}
	}
	net := decimal.Zero
	for _, l := range out {
		net = net.Add(signed(l.Side, l.Amount))
	}
	absorbRounding(out, ledger.Currency, net.Neg(), minor)
	return out, nil
}

func reportingRate(ctx context.Context, ledger Ledger, date time.Time) (decimal.Decimal, bool, error) {
	rc := reportingCurrency(ledger)
	if rc == ledger.Currency {
		return decimal.NewFromInt(1), true, nil
	}
	resolved, hasMap := ctx.Value(resolvedRatesKey{}).(map[string]resolvedRate)
	_, known := resolved[rateKey(ledger.Currency, rc, date)]
	src, _ := ctx.Value(rateSourceKey{}).(RateSource)
	if hasMap && !known || !hasMap && src == nil {
		return decimal.Zero, false, nil
	}
	rate, err := rateBetween(ctx, ledger, ledger.Currency, rc, date)
	return rate.Round(rateScale), err == nil, err
}

func applyReporting(ctx context.Context, ledger Ledger, date time.Time, lines []domain.ValidLine) error {
	rate, ok, err := reportingRate(ctx, ledger, date)
	if err != nil && !slices.ContainsFunc(lines, func(l domain.ValidLine) bool { return l.CurrencyTxn != "" }) {
		slog.WarnContext(ctx, "reporting amount left empty: no reporting rate for a domestic entry", "err", err, "ledger", ledger.Code, "correlationId", middleware.GetReqID(ctx))
		return nil
	}
	if err != nil || !ok {
		return err
	}
	rc := reportingCurrency(ledger)
	minor := money.MinorUnits(rc)
	net := decimal.Zero
	for i := range lines {
		lines[i].CurrencyRpt, lines[i].RateAcctRpt = rc, rate
		lines[i].AmountRpt = lines[i].Amount.Mul(rate).Round(minor)
		net = net.Add(signed(lines[i].Side, lines[i].AmountRpt))
	}
	if net.IsZero() {
		return nil
	}
	reduce, enlarge := domain.SideDebit, domain.SideCredit
	if net.IsNegative() {
		reduce, enlarge = enlarge, reduce
	}
	amount := net.Abs()
	if i := largestReporting(lines, reduce); i >= 0 && lines[i].AmountRpt.Sub(amount).IsPositive() {
		lines[i].AmountRpt = lines[i].AmountRpt.Sub(amount)
	} else if j := largestReporting(lines, enlarge); j >= 0 {
		lines[j].AmountRpt = lines[j].AmountRpt.Add(amount)
	}
	return nil
}

func largestReporting(lines []domain.ValidLine, side string) int {
	target := -1
	for i, l := range lines {
		if l.Side == side && l.AmountRpt.IsPositive() && (target < 0 || l.AmountRpt.GreaterThan(lines[target].AmountRpt)) {
			target = i
		}
	}
	return target
}

func txnOf(v domain.ValidLine, ledgerCurrency string) (string, decimal.Decimal) {
	if v.CurrencyTxn == "" {
		return ledgerCurrency, v.Amount
	}
	return v.CurrencyTxn, v.AmountTxn
}

func rateText(d decimal.Decimal) string {
	if d.IsZero() {
		d = decimal.NewFromInt(1)
	}
	return d.StringFixed(rateScale)
}

func moneyView(text, currency string) string {
	if text == "" {
		return ""
	}
	return decimal.RequireFromString(text).StringFixed(money.MinorUnits(currency))
}

func rateView(text string) string {
	if text == "" {
		return ""
	}
	return decimal.RequireFromString(text).StringFixed(rateScale)
}

func optionalDecimal(text string) (decimal.Decimal, error) {
	if text == "" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(text)
}
