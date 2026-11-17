// Package iccharge raises the lender's sales invoice of an intercompany charge in Invoice settlement mode (#40): intercompany
// publishes dev.depot.interco.chargeinvoice.requested.v1 instead of posting the charge, and this consumer creates a FREE_TEXT
// invoice proposal in the lender's legal entity for the borrower (the pairing's customer account): one line at the charge's price,
// on the configured revenue account, carrying the tax intercompany worked out for the charge (the same tax payables books on the
// borrower's side) under the charge's tax code. A person posts it from the billing workspace, as every proposal; the post-time
// re-quote then refuses it with BIL-0002 if billing's engine prices that tax code differently, so the mismatch is reviewed.
//
// The consumer runs as the service principal billing-iccharge, scoped to the lender's legal entity, with the one flag duty
// billing.iccharge.from-event. The proposal is keyed by the charge and its version, so a redelivered event creates nothing new.
package iccharge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/eventing"
	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/money"
	"github.com/BenjaminSRussell/depot/platform/tenant"

	"github.com/BenjaminSRussell/depot/services/billing/internal/domain"
	"github.com/BenjaminSRussell/depot/services/billing/internal/store"
)

const (
	// ConsumerName is the consumer (and Kafka group) and the service principal it runs as, service:billing-iccharge.
	ConsumerName = "billing-iccharge"
	// Duty is the only duty of that principal.
	Duty = "billing.iccharge.from-event"
	// ChargeInvoiceRequestedType is the event this consumer handles; Topic is where intercompany's outbox routes it.
	ChargeInvoiceRequestedType = "dev.depot.interco.chargeinvoice.requested.v1"
	// Source is the CloudEvents source intercompany publishes with (services/intercompany/internal/events.Source).
	Source          = "/depot/intercompany"
	Topic           = "depot.interco.chargeinvoice"
	DeadLetterTopic = "depot.billing.iccharge.dlq"
	maxDescription  = domain.MaxDescription
)

// Store creates the proposal; *store.Postgres implements it.
type Store interface {
	FindByIdempotencyKey(ctx context.Context, key string) (store.Invoice, bool, error)
	CreateProposal(ctx context.Context, inv store.Invoice) (store.Invoice, error)
	PostProposal(ctx context.Context, id string, postingDate time.Time, expectedVersion int) (store.Invoice, error)
}

// Config is what an intercompany invoice is booked with: the revenue account of the line, the tax code when the charge names none,
// and the payment term in days.
type Config struct {
	RevenueAccount  string
	TaxCode         string
	PaymentTermDays int
	// Periods and Ledgers let an opted-in charge post only into an open receivables period (close decides): Ledgers maps the lender's
	// legal entity to its ledger. Without them nothing is auto-posted and the proposal waits for review. Now is the clock (default
	// time.Now).
	Periods PeriodChecker
	Ledgers map[string]string
	Now     func() time.Time
}

// PeriodChecker says whether the receivables period of a date is open; internal/periods.Client (close) implements it.
type PeriodChecker interface {
	Open(ctx context.Context, legalEntityID, ledgerID string, date time.Time) (bool, error)
}

// Data is the part of the event this consumer reads.
type Data struct {
	ID                  string `json:"id"`
	SourceID            string `json:"sourceId"`
	Version             int    `json:"version"`
	Lender              string `json:"lender"`
	Borrower            string `json:"borrower"`
	Category            string `json:"category"`
	Date                string `json:"transactionDate"`
	Currency            string `json:"currency"`
	Price               string `json:"price"`
	Tax                 string `json:"tax"`
	TaxCode             string `json:"taxCode"`
	LenderLegalEntityID string `json:"lenderLegalEntityId"`
	// BorrowerLegalEntityID is the other side of the invoice; the GL posting names it as the trading partner (#40).
	BorrowerLegalEntityID string `json:"borrowerLegalEntityId"`
	CustomerAccount       string `json:"customerAccount"`
	// AutoPostInvoice is the pairing's opt-in to post the proposal without review.
	AutoPostInvoice bool `json:"autoPostInvoice"`
}

// keyOf is the Idempotency-Key of one version of a charge.
func keyOf(id string, version int) string {
	return store.ICChargeKeyPrefix + id + ":" + strconv.Itoa(version)
}

// earlierPosted reports whether an earlier version of the charge has a posted invoice. A later version is then never auto-posted: it
// would bill the borrower again for the same charge while version 1 stays on the books, so it waits for a person, who credits the
// earlier invoice first.
func earlierPosted(ctx context.Context, s Store, d Data) (bool, error) {
	for v := 1; v < d.Version; v++ {
		inv, found, err := s.FindByIdempotencyKey(ctx, keyOf(d.ID, v))
		if err != nil {
			return false, err
		}
		if found && inv.Status != domain.StatusProposal {
			return true, nil
		}
	}
	return false, nil
}

const reviewEarlierPosted = " [Review: an earlier version of this charge is already posted; credit it before posting this one]"

// Key is the Idempotency-Key of the proposal of a charge version.
func Key(d Data) string { return keyOf(d.ID, d.Version) }

// Proposal builds the invoice proposal of a charge and the line's tax (the charge's, rounded to the currency), or the reason it
// cannot be raised (no retry changes it).
func Proposal(d Data, cfg Config) (store.Invoice, domain.Line, decimal.Decimal, error) {
	switch {
	case !ids.Valid(d.ID) || d.Version < 1:
		return store.Invoice{}, domain.Line{}, decimal.Zero, errors.New("the charge has no id or version")
	case !ids.Valid(d.LenderLegalEntityID):
		return store.Invoice{}, domain.Line{}, decimal.Zero, fmt.Errorf("lender %q has no legal entity in the intercompany registry", d.Lender)
	case !ids.Valid(d.CustomerAccount):
		return store.Invoice{}, domain.Line{}, decimal.Zero, fmt.Errorf("the pairing %s -> %s names no customer account that is a customer id", d.Lender, d.Borrower)
	case len(d.Currency) != 3:
		return store.Invoice{}, domain.Line{}, decimal.Zero, errors.New("the charge has no currency")
	}
	date, err := time.Parse(time.DateOnly, d.Date)
	if err != nil {
		return store.Invoice{}, domain.Line{}, decimal.Zero, errors.New("the charge has no transaction date")
	}
	price, err := decimal.NewFromString(d.Price)
	if err != nil || price.IsNegative() {
		return store.Invoice{}, domain.Line{}, decimal.Zero, errors.New("the charge has no price")
	}
	tax := decimal.Zero
	if d.Tax != "" {
		if tax, err = decimal.NewFromString(d.Tax); err != nil || tax.IsNegative() {
			return store.Invoice{}, domain.Line{}, decimal.Zero, errors.New("the charge's tax is not an amount")
		}
	}
	taxCode := d.TaxCode
	if taxCode == "" {
		taxCode = cfg.TaxCode
	}
	desc := fmt.Sprintf("Intercompany %s charge %s from %s to %s", d.Category, d.SourceID, d.Lender, d.Borrower)
	if r := []rune(desc); len(r) > maxDescription {
		desc = string(r[:maxDescription])
	}
	line := domain.Line{No: 1, Description: desc, Qty: decimal.NewFromInt(1), UnitPrice: price.Round(money.MinorUnits(d.Currency)),
		RevenueAccount: cfg.RevenueAccount, TaxCode: taxCode}
	if err := line.Validate(); err != nil {
		return store.Invoice{}, domain.Line{}, decimal.Zero, err
	}
	inv := store.Invoice{ID: ids.New(), LegalEntityID: d.LenderLegalEntityID, CustomerID: d.CustomerAccount, InvoiceAccountID: d.CustomerAccount,
		Type: domain.TypeFreeText, Status: domain.StatusProposal, Currency: d.Currency, InvoiceDate: date,
		DueDate: date.AddDate(0, 0, cfg.PaymentTermDays), PaymentTermDays: cfg.PaymentTermDays, IdempotencyKey: Key(d), RequestHash: Key(d)}
	if ids.Valid(d.BorrowerLegalEntityID) && d.BorrowerLegalEntityID != d.LenderLegalEntityID {
		inv.TradingPartnerID = d.BorrowerLegalEntityID
	}
	return inv, line, tax.RoundBank(money.MinorUnits(d.Currency)), nil
}

// Handlers returns the consumer's handler. Register it with a consumer named ConsumerName with Idempotent set.
func Handlers(s Store, cfg Config) map[string]eventing.Handler {
	return map[string]eventing.Handler{
		ChargeInvoiceRequestedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			_, err := Handle(ctx, s, cfg, evt)
			return err
		}),
	}
}

// Handle raises the proposal of one event and returns it (the one already raised on a redelivery).
func Handle(ctx context.Context, s Store, cfg Config, evt events.Event) (store.Invoice, error) {
	actor, ok := tenant.FromContext(ctx)
	if !ok || !actor.HasDuty(Duty) {
		return store.Invoice{}, eventing.Permanent(errors.New("ic charge consumer has no principal with " + Duty))
	}
	if evt.Source != Source {
		return store.Invoice{}, eventing.Permanent(fmt.Errorf("%s from source %q, want %q", evt.Type, evt.Source, Source))
	}
	var d Data
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return store.Invoice{}, eventing.Permanent(fmt.Errorf("chargeinvoice.requested data: %w", err))
	}
	inv, line, tax, err := Proposal(d, cfg)
	if err != nil {
		return store.Invoice{}, eventing.Permanent(err)
	}
	scoped, err := tenant.ServicePrincipal(ConsumerName, actor.TenantID, d.LenderLegalEntityID)
	if err != nil {
		return store.Invoice{}, eventing.Permanent(err)
	}
	ctx = tenant.With(ctx, scoped)
	earlier := false
	if d.AutoPostInvoice && d.Version > 1 {
		if earlier, err = earlierPosted(ctx, s, d); err != nil {
			return store.Invoice{}, err
		}
	}
	if existing, found, err := s.FindByIdempotencyKey(ctx, inv.IdempotencyKey); err != nil || found {
		if err == nil && d.AutoPostInvoice && !earlier {
			return autoPost(ctx, s, cfg, d, existing)
		}
		return existing, err
	}
	if earlier {
		// Left for review with the reason on the line.
		line.Description = strings.TrimSpace(truncate(line.Description, maxDescription-len(reviewEarlierPosted))) + reviewEarlierPosted
	}
	nets, totals, err := domain.Compute(inv.Currency, []domain.Line{line}, []decimal.Decimal{tax}, decimal.Zero)
	if err != nil {
		return store.Invoice{}, eventing.Permanent(err)
	}
	inv.Lines = []store.Line{{Line: line, Net: nets[0], Tax: tax}}
	inv.Totals = totals
	created, err := s.CreateProposal(ctx, inv)
	if err != nil {
		return store.Invoice{}, err
	}
	slog.InfoContext(ctx, "intercompany invoice proposal raised", "charge", d.ID, "version", d.Version, "invoice", created.ID, "legalEntity", d.LenderLegalEntityID)
	if d.AutoPostInvoice && !earlier {
		return autoPost(ctx, s, cfg, d, created)
	}
	if earlier {
		slog.WarnContext(ctx, "intercompany invoice left for review: an earlier version of the charge is posted", "charge", d.ID, "version", d.Version, "invoice", created.ID)
	}
	return created, nil
}

// autoPost posts the proposal of a charge whose pairing opted in. The proposal must be exactly the charge: the currency, and a
// gross of price plus tax; anything else is left as a proposal for review and reported as a permanent failure. An invoice that is
// already posted (a redelivery) is returned as it is.
func autoPost(ctx context.Context, s Store, cfg Config, d Data, inv store.Invoice) (store.Invoice, error) {
	if inv.Status == domain.StatusPosted {
		return inv, nil
	}
	if inv.Status != domain.StatusProposal {
		return inv, eventing.Permanent(fmt.Errorf("the invoice of charge %s is %s and cannot be auto-posted", d.ID, inv.Status))
	}
	price, err := decimal.NewFromString(d.Price)
	if err != nil {
		return inv, eventing.Permanent(errors.New("the charge has no price"))
	}
	tax := decimal.Zero
	if d.Tax != "" {
		if tax, err = decimal.NewFromString(d.Tax); err != nil {
			return inv, eventing.Permanent(errors.New("the charge's tax is not an amount"))
		}
	}
	places := money.MinorUnits(d.Currency)
	want := price.Round(places).Add(tax.RoundBank(places))
	if inv.Currency != d.Currency || !inv.Totals.Gross.Equal(want) {
		return inv, eventing.Permanent(fmt.Errorf("the proposal %s totals %s %s, not the charge's %s %s: left for review", inv.ID, inv.Totals.Gross, inv.Currency, want, d.Currency))
	}
	date, err := time.Parse(time.DateOnly, d.Date)
	if err != nil {
		return inv, eventing.Permanent(errors.New("the charge has no transaction date"))
	}
	posting, ok, err := postingDate(ctx, cfg, d, date)
	if err != nil {
		return inv, err
	}
	if !ok {
		slog.WarnContext(ctx, "intercompany invoice left for review: no open receivables period to post it into", "charge", d.ID, "version", d.Version, "invoice", inv.ID)
		return inv, nil
	}
	posted, err := s.PostProposal(ctx, inv.ID, posting, inv.Version)
	if err != nil {
		return inv, err
	}
	slog.InfoContext(ctx, "intercompany invoice posted", "charge", d.ID, "version", d.Version, "invoice", posted.ID, "legalEntity", d.LenderLegalEntityID)
	return posted, nil
}

// postingDate is the charge's date when its receivables period is open, else today (the first open date), the charge date staying the
// invoice date. A date after today posts today. ok is false when close or the lender's ledger is not configured, or today's period is
// closed too: the proposal then stays for review. A failure to ask close is an error, so the event is retried.
func postingDate(ctx context.Context, cfg Config, d Data, day time.Time) (time.Time, bool, error) {
	ledger := cfg.Ledgers[strings.ToLower(d.LenderLegalEntityID)]
	if cfg.Periods == nil || ledger == "" {
		return time.Time{}, false, nil
	}
	now := time.Now
	if cfg.Now != nil {
		now = cfg.Now
	}
	today := now().UTC().Truncate(24 * time.Hour)
	if day.After(today) {
		day = today
	}
	for _, candidate := range []time.Time{day, today} {
		open, err := cfg.Periods.Open(ctx, d.LenderLegalEntityID, ledger, candidate)
		if err != nil {
			return time.Time{}, false, err
		}
		if open {
			return candidate, true, nil
		}
	}
	return time.Time{}, false, nil
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
