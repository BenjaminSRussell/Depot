package api

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/billing/internal/domain"
)

var taxCodeText = regexp.MustCompile(`^[A-Z0-9_-]{1,20}$`)

var ErrTaxUnavailable = errors.New("tax engine unavailable")

type UnknownTaxCodeError struct{ Code string }

func (e *UnknownTaxCodeError) Error() string {
	return "tax code " + e.Code + " is not known to the tax engine"
}

type QuoteInput struct {
	LegalEntityID string
	Currency      string
	Date          time.Time
	ShipTo        *domain.ShipTo
	Lines         []domain.Line
}

type QuoteResult struct {
	Taxes []decimal.Decimal
	Codes []string
}

// TaxEngine quotes one tax amount per line (FR-29.6). Implementations return
// ErrTaxUnavailable rather than zero when they cannot answer.
type TaxEngine interface {
	Quote(ctx context.Context, in QuoteInput) (QuoteResult, error)
}

// UnavailableTax is the engine used when TAX_URL is unset: every quote fails,
// so no invoice is proposed or posted with silent zero tax.
type UnavailableTax struct{}

func (UnavailableTax) Quote(context.Context, QuoteInput) (QuoteResult, error) {
	return QuoteResult{}, ErrTaxUnavailable
}

func applyCodes(lines []domain.Line, codes []string) {
	for i, code := range codes {
		if taxCodeText.MatchString(code) {
			lines[i].TaxCode = code
		}
	}
}

func allSupplied(lines []domain.Line) bool {
	for _, l := range lines {
		if l.SuppliedTax == nil {
			return false
		}
	}
	return true
}

// quote takes the caller's tax only when every line carries one and no ship-to was sent; otherwise the engine answers, with an empty party when there is no ship-to.
func (a *API) quote(ctx context.Context, in QuoteInput) (QuoteResult, error) {
	if in.ShipTo != nil || !allSupplied(in.Lines) {
		return a.tax.Quote(ctx, in)
	}
	out := QuoteResult{Taxes: make([]decimal.Decimal, len(in.Lines)), Codes: make([]string, len(in.Lines))}
	for i, l := range in.Lines {
		out.Taxes[i] = *l.SuppliedTax
	}
	return out, nil
}

// suppliedTaxTrusted reports whether the caller may state the tax itself: only a verified service
// principal (sales feeding billing), never a person or a portal. Everyone else gets the engine's answer.
func suppliedTaxTrusted(ctx context.Context) bool {
	p, ok := tenant.FromContext(ctx)
	return ok && p.Service != ""
}

// dropUntrustedTax removes caller-supplied line tax from a request that did not come from a service
// principal, so the tax engine decides (or the request fails closed when it is unavailable).
func dropUntrustedTax(ctx context.Context, lines []domain.Line) {
	if suppliedTaxTrusted(ctx) {
		return
	}
	for i := range lines {
		lines[i].SuppliedTax = nil
	}
}
