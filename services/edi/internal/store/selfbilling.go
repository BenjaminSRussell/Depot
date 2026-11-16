package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
)

// SelfBillConfig is a partner's self-billing settings (§6.35.13). With TaxCountry set (and the tax service configured) the VAT
// rate the billed lines are recomputed at (BR-35.15) is determined by the tax service from TaxCountry, TaxGroup and ItemTaxGroup
// on the document date; otherwise TaxRatePct is used.
type SelfBillConfig struct {
	QuantityTolerance decimal.Decimal `json:"quantityTolerance"`
	PriceTolerance    decimal.Decimal `json:"priceTolerance"`
	WaitDays          int             `json:"waitDays"`
	TaxRatePct        decimal.Decimal `json:"taxRatePct"`
	TaxCountry        string          `json:"taxCountry"`
	TaxGroup          string          `json:"taxGroup"`
	ItemTaxGroup      string          `json:"itemTaxGroup"`
	// RevenueAccount, TaxCode and PaymentTermDays are what the legal invoice billing posts from a matched document carries: the
	// ledger account of its lines, their tax code, and the days to its due date. Without a revenue account and a tax code the
	// matched document waits unposted.
	RevenueAccount  string `json:"revenueAccount"`
	TaxCode         string `json:"taxCode"`
	PaymentTermDays int    `json:"paymentTermDays"`
}

// DefaultSelfBillConfig is what a partner without settings gets: tolerance 0 and 0.01, 30 days, tax 0.
func DefaultSelfBillConfig() SelfBillConfig {
	return SelfBillConfig{QuantityTolerance: decimal.Zero, PriceTolerance: decimal.RequireFromString("0.01"),
		WaitDays: domain.SelfBillDefaultWaitDays, TaxRatePct: decimal.Zero, PaymentTermDays: 30}
}

// PutSelfBillConfig stores the settings under the legal entity of the partner's trade profile.
func (s *Store) PutSelfBillConfig(ctx context.Context, partnerID string, c SelfBillConfig) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `insert into self_billing_config (tenant_id, partner_id, legal_entity_id, quantity_tolerance, price_tolerance, wait_days, tax_rate_pct,
			tax_country, tax_group, item_tax_group, revenue_account, tax_code, payment_term_days)
			select $1::uuid, partner_id, legal_entity_id, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12 from partner_trade_profile where partner_id = $2
			on conflict (tenant_id, partner_id) do update set quantity_tolerance = excluded.quantity_tolerance,
			price_tolerance = excluded.price_tolerance, wait_days = excluded.wait_days, tax_rate_pct = excluded.tax_rate_pct,
			tax_country = excluded.tax_country, tax_group = excluded.tax_group, item_tax_group = excluded.item_tax_group,
			revenue_account = excluded.revenue_account, tax_code = excluded.tax_code, payment_term_days = excluded.payment_term_days, updated_at = now()`,
			tid, partnerID, c.QuantityTolerance, c.PriceTolerance, c.WaitDays, c.TaxRatePct, c.TaxCountry, c.TaxGroup, c.ItemTaxGroup,
			c.RevenueAccount, c.TaxCode, c.PaymentTermDays)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// GetSelfBillConfig returns the settings, or the defaults with found false.
func (s *Store) GetSelfBillConfig(ctx context.Context, partnerID string) (SelfBillConfig, bool, error) {
	c := DefaultSelfBillConfig()
	found := true
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select quantity_tolerance, price_tolerance, wait_days, tax_rate_pct, tax_country, tax_group, item_tax_group,
			revenue_account, tax_code, payment_term_days from self_billing_config where partner_id = $1`,
			partnerID).Scan(&c.QuantityTolerance, &c.PriceTolerance, &c.WaitDays, &c.TaxRatePct, &c.TaxCountry, &c.TaxGroup, &c.ItemTaxGroup,
			&c.RevenueAccount, &c.TaxCode, &c.PaymentTermDays)
		if errors.Is(err, pgx.ErrNoRows) {
			found = false
			return nil
		}
		return err
	})
	return c, found, err
}

// RegisterSelfBillShipments records what a shipment shipped so the partner's self-billed lines can be matched to it. A line
// already registered (the same delivery note, PO line and item) is left as it is.
func (s *Store) RegisterSelfBillShipments(ctx context.Context, partnerID, legalEntityID string, lines []domain.SBShipmentLine) error {
	if len(lines) == 0 {
		return nil
	}
	cfg, _, err := s.GetSelfBillConfig(ctx, partnerID)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		for _, l := range lines {
			if _, err := tx.Exec(ctx, `insert into self_billing_shipment (tenant_id, id, partner_id, legal_entity_id, delivery_note, po_line, item,
				quantity, unit_price, tax_rate_pct, shipped_on, source_shipment_id, order_id, order_line_id)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, nullif($12, '')::uuid, nullif($13, ''), nullif($14, ''))
				on conflict (tenant_id, partner_id, delivery_note, po_line, item) do nothing`,
				tid, ids.New(), partnerID, legalEntityID, l.DeliveryNote, l.POLine, l.Item, l.Quantity, l.UnitPrice, cfg.TaxRatePct,
				l.ShippedOn.Format("2006-01-02"), l.ShipmentID, l.OrderID, l.OrderLineID); err != nil {
				return err
			}
		}
		return nil
	})
}

// SelfBillInput is one received self-billed document ready to be matched. TaxRatePct, when set, is the VAT rate the tax service
// determined for the document: every shipment's tax is recomputed at it instead of the rate stored when the shipment registered.
type SelfBillInput struct {
	MessageID  string
	Number     string
	Date       string
	Currency   string
	Lines      []domain.SBLine
	TaxRatePct *decimal.Decimal
}

// SelfBillDocument is the stored result of matching one document.
type SelfBillDocument struct {
	ID            string                `json:"id"`
	PartnerID     string                `json:"partnerId"`
	MessageID     string                `json:"messageId,omitempty"`
	Number        string                `json:"number"`
	DocumentDate  string                `json:"documentDate"`
	Status        string                `json:"status"`
	MatchedLines  int                   `json:"matchedLines"`
	DisputedLines int                   `json:"disputedLines"`
	Lines         []domain.SBLineResult `json:"lines,omitempty"`
	CreatedAt     time.Time             `json:"createdAt"`
	// BillingInvoiceID and BillingNumber name the legal invoice billing posted from a matched document; PostError says why it has not
	// been posted yet (the sweep tries again).
	BillingInvoiceID string            `json:"billingInvoiceId,omitempty"`
	BillingNumber    string            `json:"billingNumber,omitempty"`
	PostError        string            `json:"postError,omitempty"`
	Disputes         []SelfBillDispute `json:"disputes,omitempty"`
	Issues           []domain.Issue    `json:"issues,omitempty"`
	rawLines         map[string]domain.SBLine
}

// SelfBillDispute is one open question of the dispute worklist.
type SelfBillDispute struct {
	ID         string     `json:"id"`
	PartnerID  string     `json:"partnerId"`
	Kind       string     `json:"kind"`
	Code       string     `json:"code"`
	DocumentID string     `json:"documentId,omitempty"`
	Line       string     `json:"line,omitempty"`
	ShipmentID string     `json:"shipmentId,omitempty"`
	Detail     string     `json:"detail"`
	Status     string     `json:"status"`
	Resolution string     `json:"resolution,omitempty"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// ErrSelfBillReconciled means this very message was matched already (by a concurrent run); there is nothing left to do.
var ErrSelfBillReconciled = errors.New("edi: self-billed message already reconciled")

// ErrSelfBillNoProfile means the partner has no trade profile, so no legal entity to match in.
var ErrSelfBillNoProfile = errors.New("edi: partner has no trade profile")

// ReconcileSelfBill matches a received self-billed document against the partner's unbilled shipments (BR-35.15) in one
// transaction: the shipments the lines claim (matched or with a difference) are marked billed by the document, a document
// number is accepted once (ErrDuplicate), and every DIFFERENCE or UNMATCHED line opens a dispute. The message, when given, becomes
// POSTED. Nothing is posted to billing or receivables: that is the self-billed rule of wave 29 (handoff).
func (s *Store) ReconcileSelfBill(ctx context.Context, partnerID string, in SelfBillInput) (SelfBillDocument, error) {
	var out SelfBillDocument
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		var legalEntityID string
		if err := tx.QueryRow(ctx, `select legal_entity_id::text from partner_trade_profile where partner_id = $1`, partnerID).Scan(&legalEntityID); errors.Is(err, pgx.ErrNoRows) {
			return ErrSelfBillNoProfile
		} else if err != nil {
			return err
		}
		cfg := DefaultSelfBillConfig()
		if err := tx.QueryRow(ctx, `select quantity_tolerance, price_tolerance, wait_days, tax_rate_pct from self_billing_config where partner_id = $1`, partnerID).
			Scan(&cfg.QuantityTolerance, &cfg.PriceTolerance, &cfg.WaitDays, &cfg.TaxRatePct); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// The receipt and the sweep can take up the same message at once: one lock per document number makes the second wait and
		// then see the first one's document, so it is neither matched twice nor parked as a duplicate of itself.
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "edi:selfbill:"+tid+":"+partnerID+":"+in.Number); err != nil {
			return err
		}
		var priorMessage string
		err = tx.QueryRow(ctx, `select coalesce(message_id::text, '') from self_billing_document where partner_id = $1 and number = $2`, partnerID, in.Number).Scan(&priorMessage)
		switch {
		case err == nil && in.MessageID != "" && priorMessage == in.MessageID:
			return ErrSelfBillReconciled
		case err == nil:
			return ErrDuplicate
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		rows, err := tx.Query(ctx, `select id::text, delivery_note, po_line, item, quantity, unit_price, tax_rate_pct, shipped_on from self_billing_shipment
			where partner_id = $1 and billed_by is null order by shipped_on, delivery_note, po_line, item for update`, partnerID)
		if err != nil {
			return err
		}
		var ships []domain.SBShipment
		for rows.Next() {
			var sh domain.SBShipment
			if err := rows.Scan(&sh.ID, &sh.DeliveryNote, &sh.POLine, &sh.Item, &sh.Quantity, &sh.UnitPrice, &sh.TaxRatePct, &sh.ShippedOn); err != nil {
				rows.Close()
				return err
			}
			ships = append(ships, sh)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if in.TaxRatePct != nil {
			for i := range ships {
				ships[i].TaxRatePct = *in.TaxRatePct
			}
		}
		res, err := domain.MatchSelfBilling(in.Lines, ships, domain.SBTolerance{Quantity: cfg.QuantityTolerance, Price: cfg.PriceTolerance}, time.Now().UTC(), cfg.WaitDays)
		if err != nil {
			return err
		}
		docID := ids.New()
		matched, disputed := 0, 0
		for _, l := range res.Lines {
			if l.Outcome == domain.SelfBillMatched {
				matched++
			} else {
				disputed++
			}
		}
		status := "MATCHED"
		if disputed > 0 {
			status = "DISPUTED"
		}
		out = SelfBillDocument{ID: docID, PartnerID: partnerID, MessageID: in.MessageID, Number: in.Number, DocumentDate: in.Date, Status: status,
			MatchedLines: matched, DisputedLines: disputed, Lines: res.Lines, CreatedAt: time.Now().UTC()}
		rateSource := "PARTNER"
		if in.TaxRatePct != nil {
			rateSource = "TAX"
		}
		if _, err := tx.Exec(ctx, `insert into self_billing_document (tenant_id, id, partner_id, legal_entity_id, message_id, number, document_date, currency,
			status, matched_lines, disputed_lines, tax_rate_source) values ($1,$2,$3,$4,nullif($5,'')::uuid,$6,$7,nullif($8,''),$9,$10,$11,$12)`,
			tid, docID, partnerID, legalEntityID, in.MessageID, in.Number, in.Date, in.Currency, status, matched, disputed, rateSource); err != nil {
			return err
		}
		byLine := map[string]domain.SBLine{}
		for _, l := range in.Lines {
			byLine[l.Line] = l
		}
		for _, l := range res.Lines {
			raw := byLine[l.Line]
			diffs, _ := json.Marshal(l.Differences)
			if l.Differences == nil {
				diffs = []byte("[]")
			}
			if _, err := tx.Exec(ctx, `insert into self_billing_line (tenant_id, document_id, line, delivery_note, po_line, item, quantity, unit_price, outcome, shipment_id, differences, tax)
				values ($1,$2,$3,$4,$5,$6,$7,$8,$9,nullif($10,'')::uuid,$11,$12)`,
				tid, docID, l.Line, raw.DeliveryNote, raw.POLine, raw.Item, raw.Quantity, raw.UnitPrice, l.Outcome, l.ShipmentID, diffs, l.Tax); err != nil {
				return err
			}
			if l.ShipmentID != "" {
				if _, err := tx.Exec(ctx, `update self_billing_shipment set billed_by = $2 where id = $1`, l.ShipmentID, docID); err != nil {
					return err
				}
			}
			if l.Outcome == domain.SelfBillMatched {
				continue
			}
			code, kind, detail := domain.CodeSelfBillUnmatched, "UNMATCHED", fmt.Sprintf("Self-billed line %s of %s has no matching shipment.", l.Line, in.Number)
			if l.Outcome == domain.SelfBillDifference {
				code, kind = domain.CodeSelfBillDifference, "DIFFERENCE"
				detail = fmt.Sprintf("Self-billed line %s of %s differs from the shipment:", l.Line, in.Number)
				for _, d := range l.Differences {
					detail += fmt.Sprintf(" %s %s (ours %s);", d.Field, d.Theirs, d.Ours)
				}
			}
			d, err := openDispute(ctx, tx, tid, partnerID, legalEntityID, kind, code, docID, l.Line, l.ShipmentID, detail)
			if err != nil {
				return err
			}
			out.Disputes = append(out.Disputes, d)
		}
		out.Issues = res.Issues
		if in.MessageID != "" {
			if _, err := tx.Exec(ctx, `update message set status = 'POSTED' where id = $1 and status = 'MAPPED'`, in.MessageID); err != nil {
				return err
			}
		}
		return enqueueEvent(ctx, tx, tid, ediev.SelfBillingReconciled, "selfbilling", docID, ediev.SelfBillingReconciledData{
			DocumentID: docID, PartnerID: partnerID, LegalEntityID: legalEntityID, Number: in.Number, Status: status, MatchedLines: matched, DisputedLines: disputed})
	})
	return out, err
}

func openDispute(ctx context.Context, tx pgx.Tx, tid, partnerID, legalEntityID, kind, code, documentID, line, shipmentID, detail string) (SelfBillDispute, error) {
	d := SelfBillDispute{ID: ids.New(), PartnerID: partnerID, Kind: kind, Code: code, DocumentID: documentID, Line: line, ShipmentID: shipmentID, Detail: detail,
		Status: "OPEN", CreatedAt: time.Now().UTC()}
	tag, err := tx.Exec(ctx, `insert into self_billing_dispute (tenant_id, id, partner_id, legal_entity_id, kind, code, document_id, line, shipment_id, detail)
		values ($1,$2,$3,$4,$5,$6,nullif($7,'')::uuid,nullif($8,''),nullif($9,'')::uuid,$10) on conflict do nothing`,
		tid, d.ID, partnerID, legalEntityID, kind, code, documentID, line, shipmentID, detail)
	if err != nil {
		return d, err
	}
	if tag.RowsAffected() == 0 {
		return d, errAlreadyDisputed
	}
	return d, enqueueEvent(ctx, tx, tid, ediev.DisputeOpened, "dispute", d.ID, ediev.DisputeData{
		DisputeID: d.ID, PartnerID: partnerID, LegalEntityID: legalEntityID, Kind: kind, Code: code, DocumentID: documentID, Line: line, ShipmentID: shipmentID, Detail: detail})
}

var errAlreadyDisputed = errors.New("edi: already disputed")

// SweepUnbilled opens an UNBILLED dispute (EDI-0138) for every shipment of the caller's partners still not self-billed after the
// partner's wait days. A shipment is disputed once; the sweep is safe to run as often as wanted. It returns the disputes it opened.
func (s *Store) SweepUnbilled(ctx context.Context, asOf time.Time) ([]SelfBillDispute, error) {
	var opened []SelfBillDispute
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select sh.id::text, sh.partner_id::text, sh.legal_entity_id::text, sh.delivery_note, sh.po_line, sh.item, sh.shipped_on, c.wait_days
			from self_billing_shipment sh join self_billing_config c on c.tenant_id = sh.tenant_id and c.partner_id = sh.partner_id
			where sh.billed_by is null and sh.shipped_on + c.wait_days <= $1::date
			and not exists (select 1 from self_billing_dispute d where d.shipment_id = sh.id and d.kind = 'UNBILLED')
			order by sh.shipped_on, sh.id`, asOf.Format("2006-01-02"))
		if err != nil {
			return err
		}
		type due struct {
			id, partner, le, note, line, item string
			shipped                           time.Time
			wait                              int
		}
		var list []due
		for rows.Next() {
			var d due
			if err := rows.Scan(&d.id, &d.partner, &d.le, &d.note, &d.line, &d.item, &d.shipped, &d.wait); err != nil {
				rows.Close()
				return err
			}
			list = append(list, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, d := range list {
			detail := fmt.Sprintf("Shipment %s line %s item %s shipped %s was not self-billed within %d days.", d.note, d.line, d.item, d.shipped.Format("2006-01-02"), d.wait)
			dp, err := openDispute(ctx, tx, tid, d.partner, d.le, "UNBILLED", domain.CodeSelfBillUnbilled, "", "", d.id, detail)
			if errors.Is(err, errAlreadyDisputed) {
				continue
			}
			if err != nil {
				return err
			}
			opened = append(opened, dp)
		}
		return nil
	})
	return opened, err
}

// PendingSelfBilled lists inbound INVOIC and 810 messages that carry a self-billed invoice and were not matched yet.
type PendingSelfBilled struct {
	MessageID string
	PartnerID string
	Canonical []byte
}

func (s *Store) PendingSelfBilled(ctx context.Context, limit int) ([]PendingSelfBilled, error) {
	return s.pendingSelfBilled(ctx, limit, nil)
}

// PendingSelfBilledIn is PendingSelfBilled limited to the given messages (those one interchange just brought in).
func (s *Store) PendingSelfBilledIn(ctx context.Context, messageIDs []string) ([]PendingSelfBilled, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	return s.pendingSelfBilled(ctx, len(messageIDs), messageIDs)
}

func (s *Store) pendingSelfBilled(ctx context.Context, limit int, messageIDs []string) ([]PendingSelfBilled, error) {
	var out []PendingSelfBilled
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select m.id::text, i.partner_id::text, m.canonical::text from message m
			join interchange i on i.tenant_id = m.tenant_id and i.id = m.interchange_id and i.direction = 'IN'
			where m.transaction_set in ('INVOIC', '810') and m.status = 'MAPPED' and m.canonical->>'selfBilled' = 'true'
			and ($2::uuid[] is null or m.id = any($2::uuid[]))
			order by m.created_at limit $1`, limit, messageIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p PendingSelfBilled
			var c string
			if err := rows.Scan(&p.MessageID, &p.PartnerID, &c); err != nil {
				return err
			}
			p.Canonical = []byte(c)
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// ListSelfBillDocuments lists received documents newest first, optionally of one status.
func (s *Store) ListSelfBillDocuments(ctx context.Context, status string, limit int) ([]SelfBillDocument, error) {
	out := []SelfBillDocument{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, partner_id::text, coalesce(message_id::text, ''), number, document_date, status, matched_lines, disputed_lines, created_at,
			coalesce(billing_invoice_id::text, ''), coalesce(billing_number, ''), post_error
			from self_billing_document where ($1 = '' or status = $1) order by created_at desc, id limit $2`, status, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d SelfBillDocument
			if err := rows.Scan(&d.ID, &d.PartnerID, &d.MessageID, &d.Number, &d.DocumentDate, &d.Status, &d.MatchedLines, &d.DisputedLines, &d.CreatedAt, &d.BillingInvoiceID, &d.BillingNumber, &d.PostError); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// ListDisputes is the dispute worklist, oldest first so the longest waiting is worked first.
func (s *Store) ListDisputes(ctx context.Context, status, partnerID string, limit int) ([]SelfBillDispute, error) {
	out := []SelfBillDispute{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, partner_id::text, kind, code, coalesce(document_id::text, ''), coalesce(line, ''), coalesce(shipment_id::text, ''),
			detail, status, coalesce(resolution, ''), resolved_at, created_at from self_billing_dispute
			where ($1 = '' or status = $1) and ($2 = '' or partner_id::text = $2) order by created_at, id limit $3`, status, partnerID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d SelfBillDispute
			if err := rows.Scan(&d.ID, &d.PartnerID, &d.Kind, &d.Code, &d.DocumentID, &d.Line, &d.ShipmentID, &d.Detail, &d.Status, &d.Resolution, &d.ResolvedAt, &d.CreatedAt); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// ErrDisputeResolved means the dispute was decided already.
var ErrDisputeResolved = errors.New("edi: dispute already resolved")

// ResolveDispute decides one open dispute: ACCEPTED takes the customer's line as billed (the shipment of an UNBILLED dispute
// is closed as billed outside the system), REJECTED keeps our figures and leaves the correction to the customer. When the last
// open dispute of a document is decided the document becomes RESOLVED.
func (s *Store) ResolveDispute(ctx context.Context, id, decision, note, userID string) (SelfBillDispute, error) {
	var d SelfBillDispute
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		var le string
		err = tx.QueryRow(ctx, `select id::text, partner_id::text, legal_entity_id::text, kind, code, coalesce(document_id::text, ''), coalesce(line, ''),
			coalesce(shipment_id::text, ''), detail, status, created_at from self_billing_dispute where id = $1 for update`, id).
			Scan(&d.ID, &d.PartnerID, &le, &d.Kind, &d.Code, &d.DocumentID, &d.Line, &d.ShipmentID, &d.Detail, &d.Status, &d.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if d.Status != "OPEN" {
			return ErrDisputeResolved
		}
		now := time.Now().UTC()
		d.Status, d.Resolution, d.ResolvedAt = decision, note, &now
		if _, err := tx.Exec(ctx, `update self_billing_dispute set status = $2, resolution = $3, resolved_by = nullif($4,'')::uuid, resolved_at = $5 where id = $1`,
			id, decision, note, userID, now); err != nil {
			return err
		}
		if d.Kind == "UNBILLED" && decision == "ACCEPTED" && d.ShipmentID != "" {
			if _, err := tx.Exec(ctx, `update self_billing_shipment set billed_by = $1 where id = $2 and billed_by is null`, d.ID, d.ShipmentID); err != nil {
				return err
			}
		}
		if d.DocumentID != "" {
			if _, err := tx.Exec(ctx, `update self_billing_document d set status = 'RESOLVED' where d.id = $1 and d.status = 'DISPUTED'
				and not exists (select 1 from self_billing_dispute x where x.document_id = d.id and x.status = 'OPEN')`, d.DocumentID); err != nil {
				return err
			}
		}
		return enqueueEvent(ctx, tx, tid, ediev.DisputeResolved, "dispute", d.ID, ediev.DisputeData{
			DisputeID: d.ID, PartnerID: d.PartnerID, LegalEntityID: le, Kind: d.Kind, Code: d.Code, DocumentID: d.DocumentID, Line: d.Line,
			ShipmentID: d.ShipmentID, Detail: d.Detail, Decision: decision})
	})
	return d, err
}

// SelfBillDueTenants lists the tenants with self-billing work: a self-billed INVOIC not matched yet, or a shipment past its wait that was
// not billed and has no dispute. It reads across tenants through a SECURITY DEFINER function, like PostDueTenants.
func (s *Store) SelfBillDueTenants(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `select distinct t::text from edi_selfbill_due_tenants() t`)
	if err != nil {
		return nil, fmt.Errorf("edi: due self-billing tenants: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// SelfBillPost is what billing needs to post a matched document: the document with its lines (the tax each carries), the partner's
// legal entity and customer, and the settings of its legal invoice.
type SelfBillPost struct {
	DocumentID    string
	PartnerID     string
	LegalEntityID string
	CustomerID    string
	Number        string
	DocumentDate  string
	Currency      string
	Config        SelfBillConfig
	Lines         []SelfBillPostLine
}

// SelfBillPostLine is one matched line of the document.
type SelfBillPostLine struct {
	Line         string
	DeliveryNote string
	Item         string
	Quantity     decimal.Decimal
	UnitPrice    decimal.Decimal
	Tax          decimal.Decimal
	// OrderID and OrderLineID are the sales order and order line of the shipment the line matched (empty for a shipment registered before
	// they were kept, or a line that matched none).
	OrderID     string
	OrderLineID string
}

// PendingSelfBillPosts lists the documents that have no legal invoice yet and are ready for one, oldest first, at most limit: the ones that
// matched in full, and the ones a dispute held until every dispute of them was decided (RESOLVED).
func (s *Store) PendingSelfBillPosts(ctx context.Context, limit int) ([]string, error) {
	var out []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text from self_billing_document where status in ('MATCHED', 'RESOLVED') and billed_at is null and post_parked_at is null and (next_post_at is null or next_post_at <= now())
			order by next_post_at nulls first, created_at, id limit $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}

// GetSelfBillPost reads a matched or resolved, unposted document for posting; ErrNotFound when it is not that (already posted, still disputed,
// unknown). A resolved document lists the lines that matched and the lines whose dispute was ACCEPTED (the customer's figures stand); a line whose
// dispute was REJECTED keeps our figures and is left out, the correction being the customer's to send as a new document.
func (s *Store) GetSelfBillPost(ctx context.Context, documentID string) (SelfBillPost, error) {
	var p SelfBillPost
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var currency *string
		err := tx.QueryRow(ctx, `select d.id::text, d.partner_id::text, d.legal_entity_id::text, p.customer_id::text, d.number, d.document_date, d.currency
			from self_billing_document d join partner_trade_profile p on p.tenant_id = d.tenant_id and p.partner_id = d.partner_id
			where d.id = $1 and d.status in ('MATCHED', 'RESOLVED') and d.billed_at is null`, documentID).
			Scan(&p.DocumentID, &p.PartnerID, &p.LegalEntityID, &p.CustomerID, &p.Number, &p.DocumentDate, &currency)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if currency != nil {
			p.Currency = *currency
		}
		rows, err := tx.Query(ctx, `select l.line, l.delivery_note, l.item, l.quantity, l.unit_price, coalesce(l.tax, 0), coalesce(sh.order_id, ''), coalesce(sh.order_line_id, '')
			from self_billing_line l left join self_billing_shipment sh on sh.tenant_id = l.tenant_id and sh.id = l.shipment_id
			where l.document_id = $1 and (l.outcome = 'MATCHED' or exists (select 1 from self_billing_dispute x where x.tenant_id = l.tenant_id and x.document_id = l.document_id
				and x.line = l.line and x.status = 'ACCEPTED'))
			order by l.line`, documentID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l SelfBillPostLine
			if err := rows.Scan(&l.Line, &l.DeliveryNote, &l.Item, &l.Quantity, &l.UnitPrice, &l.Tax, &l.OrderID, &l.OrderLineID); err != nil {
				return err
			}
			p.Lines = append(p.Lines, l)
		}
		return rows.Err()
	})
	if err != nil {
		return p, err
	}
	cfg, _, err := s.GetSelfBillConfig(ctx, p.PartnerID)
	p.Config = cfg
	return p, err
}

// MarkSelfBillPosted records the legal invoice billing posted from the document, which ends its wait.
func (s *Store) MarkSelfBillPosted(ctx context.Context, documentID, invoiceID, number string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update self_billing_document set billing_invoice_id = $2, billing_number = $3, billed_at = now(), post_error = '', post_attempted_at = now()
			where id = $1 and billed_at is null`, documentID, invoiceID, number)
		return err
	})
}

// MaxSelfBillPostAttempts is how many times a matched document is posted before it is parked with its reason.
const MaxSelfBillPostAttempts = 8

// MarkSelfBillPostFailed records why the document was not posted this time. The next try waits 5 minutes doubled for each earlier attempt
// (at most a day); after MaxSelfBillPostAttempts, or at once when park is set (a definite refusal), the document is parked: it keeps its
// reason and is not tried again until the partner's self-billing settings are saved.
func (s *Store) MarkSelfBillPostFailed(ctx context.Context, documentID, reason string, park bool) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var attempts int
		if err := tx.QueryRow(ctx, `select post_attempts + 1 from self_billing_document where id = $1 and billed_at is null for update`, documentID).Scan(&attempts); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		if attempts >= MaxSelfBillPostAttempts {
			park = true
		}
		if park {
			reason = fmt.Sprintf("Parked after %d attempt(s): %s", attempts, reason)
		}
		if len(reason) > 500 {
			reason = reason[:500]
		}
		wait := time.Duration(5<<min(attempts-1, 8)) * time.Minute
		if wait > 24*time.Hour {
			wait = 24 * time.Hour
		}
		_, err := tx.Exec(ctx, `update self_billing_document set post_error = $2, post_attempted_at = now(), post_attempts = $3, next_post_at = now() + $4::interval,
			post_parked_at = case when $5 then now() else null end where id = $1`, documentID, reason, attempts, wait.String(), park)
		return err
	})
}

// ResetSelfBillPosts gives the partner's parked and waiting documents a fresh start, as saving its self-billing settings does.
func (s *Store) ResetSelfBillPosts(ctx context.Context, partnerID string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update self_billing_document set post_attempts = 0, next_post_at = null, post_parked_at = null
			where partner_id = $1 and status in ('MATCHED', 'RESOLVED') and billed_at is null and not resolved_before_auto_post`, partnerID)
		return err
	})
}
