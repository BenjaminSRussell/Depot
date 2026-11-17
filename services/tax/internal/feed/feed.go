// Package feed is the tax service's first Kafka consumer (#13): it maps fulfillment's packingslip.posted and receiving's receipt.posted
// into staged Intrastat movements (the store behind POST /v1/tax/intrastat/movements, same domain validation), so a shipment to or a
// receipt from another EU member state reaches the Intrastat journal without anyone keying it. Only EU cross-border movements are staged:
// the partner country (the ship-to country of a dispatch, the consignment country of an arrival, not the VAT registration country) must be an EU member state other than the declarant's own (the legal entity's Intrastat home country);
// anything else is acknowledged and skipped. Each event is staged in the same transaction that records it in consumer_inbox, so a
// redelivery stages nothing twice (and the movement's source reference is unique per legal entity besides).
package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/eventing"
	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/tax/internal/domain"
	"github.com/BenjaminSRussell/depot/services/tax/internal/store"
)

const (
	// ConsumerName is the consumer (and Kafka group) and the service principal it runs as, service:tax-intrastat.
	ConsumerName = "tax-intrastat"
	// PackingSlipTopic and ReceiptTopic are the source topics (the outbox connectors of fulfillment and receiving create them).
	PackingSlipTopic = "depot.fulfillment.packingslip"
	ReceiptTopic     = "depot.receiving.receipt"
	// DeadLetterTopic parks the events the consumer could not map or stage.
	DeadLetterTopic = "depot.tax.intrastat.dlq"

	PackingSlipPostedType = "dev.depot.fulfillment.packingslip.posted.v1"
	ReceiptPostedType     = "dev.depot.receiving.receipt.posted.v1"
	packingSlipSource     = "fulfillment"
	receiptSource         = "/depot/receiving/receiving"

	dutyFeed   = "tax.intrastat.feed"
	dateLayout = "2006-01-02"
)

// Stager is what the consumer needs of the store: the declarant country of the legal entity in ctx and staging inside the inbox transaction.
type Stager interface {
	HomeCountry(ctx context.Context, tx pgx.Tx) (string, error)
	StageMovementsTx(ctx context.Context, tx pgx.Tx, movements []domain.Movement) (int, error)
}

var _ Stager = (*store.Store)(nil)

// Handlers serves both events. Register them with a consumer named ConsumerName and Idempotent false: the inbox row and the staged
// movement commit together in the transaction the consumer hands in.
func Handlers(s Stager) map[string]eventing.Handler {
	return map[string]eventing.Handler{
		PackingSlipPostedType: eventing.HandlerFunc(func(ctx context.Context, tx pgx.Tx, evt events.Event) error {
			return handle(ctx, tx, evt, s, packingSlipSource, fromPackingSlip)
		}),
		ReceiptPostedType: eventing.HandlerFunc(func(ctx context.Context, tx pgx.Tx, evt events.Event) error {
			return handle(ctx, tx, evt, s, receiptSource, fromReceipt)
		}),
	}
}

// movementOf maps one event's data to a movement dated and referenced by the document; skip is why an event is no movement at all.
type movementOf func(data json.RawMessage) (m domain.Movement, legalEntityID string, skip string, err error)

func handle(ctx context.Context, tx pgx.Tx, evt events.Event, s Stager, source string, mapper movementOf) error {
	if tx == nil {
		return eventing.Permanent(errors.New("the Intrastat feed needs the inbox transaction"))
	}
	actor, ok := tenant.FromContext(ctx)
	if !ok || !actor.HasDuty(dutyFeed) {
		return eventing.Permanent(errors.New("Intrastat consumer has no principal with " + dutyFeed))
	}
	if evt.Source != source {
		return eventing.Permanent(fmt.Errorf("%s from source %q, want %q", evt.Type, evt.Source, source))
	}
	m, legalEntityID, skip, err := mapper(evt.Data)
	if err != nil {
		return eventing.Permanent(fmt.Errorf("%s: %w", evt.Type, err))
	}
	if !ids.Valid(legalEntityID) {
		return eventing.Permanent(fmt.Errorf("%s has no legal_entity_id", evt.Type))
	}
	legalEntityID = strings.ToLower(legalEntityID)
	if skip != "" {
		slog.WarnContext(ctx, "event is no Intrastat movement", "eventId", evt.ID, "type", evt.Type, "reason", skip)
		return nil
	}
	scoped, err := tenant.ServicePrincipal(ConsumerName, actor.TenantID, legalEntityID)
	if err != nil {
		return eventing.Permanent(err)
	}
	ctx = tenant.With(ctx, scoped)
	// The inbox transaction was opened for the consumer's tenant-level principal, which reaches no legal entity: narrow it to the event's
	// legal entity (and no other) so row-level security lets the entity setting and the staged movement through.
	if _, err := tx.Exec(ctx, `select set_config('depot.legal_entity_scopes', $1, true)`, legalEntityID); err != nil {
		return fmt.Errorf("scope the transaction to legal entity %s: %w", legalEntityID, err)
	}
	home, err := s.HomeCountry(ctx, tx)
	if err != nil {
		if errors.Is(err, store.ErrNoEntitySettings) {
			return eventing.Permanent(fmt.Errorf("legal entity %s has no Intrastat entity setting (home country), so the movement %s cannot be classified", legalEntityID, m.SourceRef))
		}
		return err
	}
	if !CrossBorder(home, m.PartnerCountry) {
		slog.InfoContext(ctx, "movement is not an EU cross-border movement", "eventId", evt.ID, "sourceRef", m.SourceRef, "partner", m.PartnerCountry, "home", home)
		return nil
	}
	if err := domain.ValidateMovement(m); err != nil {
		return eventing.Permanent(fmt.Errorf("movement %s: %w", m.SourceRef, err))
	}
	if _, err := s.StageMovementsTx(ctx, tx, []domain.Movement{m}); err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			return eventing.Permanent(err)
		}
		return err
	}
	return nil
}

// CrossBorder reports whether a movement between the declarant's country and the partner's is Intrastat business: both are EU member
// states and they differ.
func CrossBorder(home, partner string) bool {
	home, partner = normalizeCountry(home), normalizeCountry(partner)
	return home != partner && intrastatMember(home) && intrastatMember(partner)
}

// normalizeCountry reads the Greek VAT prefix EL as the ISO code GR.
func normalizeCountry(c string) string {
	if c = strings.ToUpper(strings.TrimSpace(c)); c == "EL" {
		return "GR"
	}
	return c
}

// intrastatMember reports whether a country is a member state for Intrastat: the EU member states plus XI, Northern Ireland, which the
// EU rules treat as a member state for trade in goods.
func intrastatMember(c string) bool { return domain.IsEUMember(c) || c == "XI" }

type slipLine struct {
	ProductID        string `json:"product_id"`
	Qty              string `json:"qty"`
	Weight           string `json:"weight"`
	CostAmount       string `json:"cost_amount"`
	CommodityCode    string `json:"commodity_code"`
	OriginCountry    string `json:"origin_country"`
	NetWeight        string `json:"net_weight"`
	StatisticalValue string `json:"statistical_value"`
}

// fromPackingSlip maps packingslip.posted to a dispatch: the customer's VAT country is the partner, the posting date the movement date,
// the slip number the reference. A line's value is its statistical value, else its cost.
func fromPackingSlip(raw json.RawMessage) (domain.Movement, string, string, error) {
	var d struct {
		PackingSlipNumber string     `json:"packing_slip_number"`
		LegalEntityID     string     `json:"legal_entity_id"`
		PartnerCountry    string     `json:"partner_country"`
		PostingDate       string     `json:"posting_date"`
		LookupFailed      bool       `json:"customs_lookup_failed"`
		Lines             []slipLine `json:"lines"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return domain.Movement{}, "", "", fmt.Errorf("data: %w", err)
	}
	if d.LookupFailed {
		return domain.Movement{}, d.LegalEntityID, "", errLookupFailed(d.PackingSlipNumber)
	}
	if d.PackingSlipNumber == "" {
		return domain.Movement{}, d.LegalEntityID, "", errors.New("packing_slip_number is required")
	}
	lines := make([]rawLine, len(d.Lines))
	for i, l := range d.Lines {
		lines[i] = rawLine{ProductID: l.ProductID, Qty: l.Qty, Value: first(l.StatisticalValue, l.CostAmount), CommodityCode: l.CommodityCode,
			OriginCountry: l.OriginCountry, NetWeight: l.NetWeight}
	}
	return movement(domain.FlowDispatch, "FUL:"+d.PackingSlipNumber, d.PostingDate, d.PartnerCountry, d.LegalEntityID, lines)
}

type receiptLine struct {
	ProductID        string `json:"product_id"`
	Qty              string `json:"qty"`
	AmountAcct       string `json:"amount_acct"`
	CommodityCode    string `json:"commodity_code"`
	OriginCountry    string `json:"origin_country"`
	NetWeight        string `json:"net_weight"`
	StatisticalValue string `json:"statistical_value"`
}

// fromReceipt maps receipt.posted to an arrival: the vendor's VAT country is the partner. A line's value is its statistical value, else
// its amount in the accounting currency.
func fromReceipt(raw json.RawMessage) (domain.Movement, string, string, error) {
	var d struct {
		ReceiptNumber  string        `json:"receipt_number"`
		LegalEntityID  string        `json:"legal_entity_id"`
		PartnerCountry string        `json:"partner_country"`
		PostingDate    string        `json:"posting_date"`
		LookupFailed   bool          `json:"customs_lookup_failed"`
		Lines          []receiptLine `json:"lines"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return domain.Movement{}, "", "", fmt.Errorf("data: %w", err)
	}
	if d.LookupFailed {
		return domain.Movement{}, d.LegalEntityID, "", errLookupFailed(d.ReceiptNumber)
	}
	if d.ReceiptNumber == "" {
		return domain.Movement{}, d.LegalEntityID, "", errors.New("receipt_number is required")
	}
	lines := make([]rawLine, len(d.Lines))
	for i, l := range d.Lines {
		lines[i] = rawLine{ProductID: l.ProductID, Qty: l.Qty, Value: first(l.StatisticalValue, l.AmountAcct), CommodityCode: l.CommodityCode,
			OriginCountry: l.OriginCountry, NetWeight: l.NetWeight}
	}
	return movement(domain.FlowArrival, "RCV:"+d.ReceiptNumber, d.PostingDate, d.PartnerCountry, d.LegalEntityID, lines)
}

// errLookupFailed parks an event whose producer could not read the party or product master when the document posted: its Intrastat data is
// incomplete, so it must not be mistaken for a partner without a registration. The dead-letter record keeps it for a person to stage.
func errLookupFailed(document string) error {
	return fmt.Errorf("document %s was posted while the party or product master could not be read (customs_lookup_failed); stage its movement by hand with POST /v1/tax/intrastat/movements", document)
}

type rawLine struct{ ProductID, Qty, Value, CommodityCode, OriginCountry, NetWeight string }

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func movement(flow, ref, postingDate, partner, legalEntityID string, lines []rawLine) (domain.Movement, string, string, error) {
	if partner == "" {
		// The party master has no VAT country for the partner (or party could not be read when the document posted): nothing to classify.
		return domain.Movement{}, legalEntityID, "the event carries no partner country", nil
	}
	date, err := time.Parse(dateLayout, postingDate)
	if err != nil {
		return domain.Movement{}, legalEntityID, "", fmt.Errorf("posting_date %q is not a date", postingDate)
	}
	m := domain.Movement{Flow: flow, SourceRef: ref, MovementDate: domain.Date{Time: date}, PartnerCountry: normalizeCountry(partner)}
	for i, l := range lines {
		qty, err := decimal.NewFromString(l.Qty)
		if err != nil {
			return domain.Movement{}, legalEntityID, "", fmt.Errorf("line %d: qty %q is not a decimal", i+1, l.Qty)
		}
		value, err := decimal.NewFromString(first(l.Value, "0"))
		if err != nil {
			return domain.Movement{}, legalEntityID, "", fmt.Errorf("line %d: value %q is not a decimal", i+1, l.Value)
		}
		if !qty.IsPositive() {
			// A zero or negative quantity is no movement of goods (a reversal is corrected in the journal).
			continue
		}
		ml := domain.MovementLine{LineNo: len(m.Lines) + 1, ProductID: strings.ToLower(l.ProductID), Qty: domain.Dec{Decimal: qty}, Value: domain.Dec{Decimal: value},
			CommodityCode: l.CommodityCode, OriginCountry: l.OriginCountry}
		if l.NetWeight != "" {
			w, err := decimal.NewFromString(l.NetWeight)
			if err != nil {
				return domain.Movement{}, legalEntityID, "", fmt.Errorf("line %d: net_weight %q is not a decimal", i+1, l.NetWeight)
			}
			ml.NetMass = &domain.Dec{Decimal: w}
		}
		m.Lines = append(m.Lines, ml)
	}
	if len(m.Lines) == 0 {
		return domain.Movement{}, legalEntityID, "the event has no line with a positive quantity", nil
	}
	return m, legalEntityID, "", nil
}
