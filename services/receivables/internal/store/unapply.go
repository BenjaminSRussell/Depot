package store

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/depot/services/receivables/internal/events"
)

// JournalPaymentUnapply is the source_type of the journal that undoes a payment's applications.
const JournalPaymentUnapply = "payment_unapply"

// UnapplyResult is the payment after its applications were reversed and the items they reopened.
type UnapplyResult struct {
	Payment  Payment               `json:"payment"`
	Reversed []UnappliedSettlement `json:"reversed"`
}

// UnappliedSettlement is one reversed settlement: what goes back on the item and the item after it.
type UnappliedSettlement struct {
	SettlementID string          `json:"settlementId"`
	OpenItemID   string          `json:"openItemId"`
	Amount       decimal.Decimal `json:"amount"`
	ItemAmount   decimal.Decimal `json:"itemAmount"`
	OpenAfter    decimal.Decimal `json:"openAfter"`
	ItemStatus   string          `json:"itemStatus"`
}

type liveSettlement struct {
	id, openItemID             string
	amount, discount, writeOff decimal.Decimal
	itemCurrency               *string
	itemAmount, fxGainLoss     decimal.Decimal
	crossCurrency              bool
}

// UnapplyPayment reverses every live application of a payment (#30): each settlement not yet reversed gets a reversal row with its
// amounts negated (settlement.reversal_of_id), each item gets back what the settlement took off it (in the item's currency, so a
// cross-currency settlement restores its item amount), and the payment's cash is unapplied again (RECEIVED). The ledger is owed the
// mirror of the applications, Dr 1200 / Cr 1205, 4100 and 6100 in the payment currency, and of their realized exchange differences
// in the functional currency. An item that was settled by something else since (another payment, a credit) is reopened only by this
// payment's part. payment.unapplied.v1 is published. The cash receipt itself stays; POST :reverse undoes it afterwards.
func (s *Postgres) UnapplyPayment(ctx context.Context, id string, version int, reason string) (UnapplyResult, error) {
	return s.unapply(ctx, id, version, reason, "")
}

// UnapplySettlement reverses one settlement of a payment and leaves the payment's other applications standing (#30): the same reversal
// row, item reopening, journals and event as UnapplyPayment, for that settlement alone. The payment keeps ON_ACCOUNT while another
// application stands and goes back to RECEIVED when none does. A settlement already reversed, or one that belongs to another
// payment, is refused (409 AR-PAYMENT-STATE, 404 AR-SETTLEMENT-NOT-FOUND).
func (s *Postgres) UnapplySettlement(ctx context.Context, paymentID, settlementID string, version int, reason string) (UnapplyResult, error) {
	return s.unapply(ctx, paymentID, version, reason, settlementID)
}

// CodeSettlementNotFound: the payment has no such settlement.
const CodeSettlementNotFound = "AR-SETTLEMENT-NOT-FOUND"

func (s *Postgres) unapply(ctx context.Context, id string, version int, reason, onlySettlement string) (UnapplyResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out UnapplyResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		pay, err := scanPayment(tx.QueryRow(ctx, `select `+paymentColumns+` from payment where id = $1 for update`, id))
		if err != nil {
			return notFound(err, CodePaymentNotFound, "payment not found")
		}
		if pay.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "payment changed since it was read")
		}
		u, err := unapplyRows(ctx, tx, p, pay, onlySettlement)
		if err != nil {
			return err
		}
		out = u.UnapplyResult
		return u.enqueueUnapply(ctx, tx, p, reason)
	})
	return out, mapWriteErr(err)
}

type unapplied struct {
	UnapplyResult
	settled    []rcvevents.SettlementData
	main       []journalLine
	fxLines    []journalLine
	fxCurrency string
}

func unapplyRows(ctx context.Context, tx pgx.Tx, p tenant.Principal, pay Payment, onlySettlement string) (unapplied, error) {
	live, err := liveSettlements(ctx, tx, p.TenantID, pay.ID, onlySettlement, "")
	if err != nil {
		return unapplied{}, err
	}
	if len(live) == 0 {
		return unapplied{}, noLiveSettlement(ctx, tx, p.TenantID, pay, onlySettlement)
	}
	return reverseSettlements(ctx, tx, p, pay, live, "")
}

func reverseApplicationRows(ctx context.Context, tx pgx.Tx, p tenant.Principal, pay Payment, applicationID string) (unapplied, error) {
	live, err := liveSettlements(ctx, tx, p.TenantID, pay.ID, "", applicationID)
	if err != nil {
		return unapplied{}, err
	}
	if len(live) == 0 {
		return unapplied{}, noLiveSettlement(ctx, tx, p.TenantID, pay, "")
	}
	return reverseSettlements(ctx, tx, p, pay, live, applicationID)
}

func liveSettlements(ctx context.Context, tx pgx.Tx, tenantID, paymentID, onlySettlement, applicationID string) ([]liveSettlement, error) {
	rows, err := tx.Query(ctx, `select s.id::text, s.open_item_id::text, s.settled_amount::text, s.cash_discount_taken::text, s.write_off_amount::text,
			s.item_currency::text, coalesce(s.item_amount, s.settled_amount + s.cash_discount_taken + s.write_off_amount)::text, s.fx_gain_loss::text,
			s.item_amount is not null
		from settlement s where s.tenant_id = $1 and s.payment_id = $2 and s.reversal_of_id is null
		  and ($3::text = '' or s.id::text = $3::text) and ($4::text = '' or s.application_id::text = $4::text)
		  and not exists (select 1 from settlement r where r.tenant_id = s.tenant_id and r.reversal_of_id = s.id)
		order by s.settled_at, s.id`, tenantID, paymentID, onlySettlement, applicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var live []liveSettlement
	for rows.Next() {
		var l liveSettlement
		var amount, discount, off, itemAmount, fx string
		if err := rows.Scan(&l.id, &l.openItemID, &amount, &discount, &off, &l.itemCurrency, &itemAmount, &fx, &l.crossCurrency); err != nil {
			return nil, err
		}
		l.amount, l.discount, l.writeOff = decimal.RequireFromString(amount), decimal.RequireFromString(discount), decimal.RequireFromString(off)
		l.itemAmount, l.fxGainLoss = decimal.RequireFromString(itemAmount), decimal.RequireFromString(fx)
		live = append(live, l)
	}
	return live, rows.Err()
}

func noLiveSettlement(ctx context.Context, tx pgx.Tx, tenantID string, pay Payment, onlySettlement string) error {
	if onlySettlement == "" {
		return httpx.NewProblem(http.StatusConflict, CodePaymentState, "the payment has no application to reverse (status "+pay.Status+")")
	}
	var known, reversed bool
	if err := tx.QueryRow(ctx, `select true, exists (select 1 from settlement r where r.tenant_id = $1 and r.reversal_of_id = s.id)
		from settlement s where s.tenant_id = $1 and s.payment_id = $2 and s.id::text = $3 and s.reversal_of_id is null`,
		tenantID, pay.ID, onlySettlement).Scan(&known, &reversed); err != nil || !known {
		return httpx.NewProblem(http.StatusNotFound, CodeSettlementNotFound, "settlement not found on this payment")
	}
	return httpx.NewProblem(http.StatusConflict, CodePaymentState, "the settlement is already reversed")
}

type reversalTotals struct {
	cash, disc, off, gain, loss decimal.Decimal
	order                       []string
	recvBy, gainBy, lossBy      map[string]decimal.Decimal
}

func newReversalTotals() *reversalTotals {
	return &reversalTotals{recvBy: map[string]decimal.Decimal{}, gainBy: map[string]decimal.Decimal{}, lossBy: map[string]decimal.Decimal{}}
}

func (t *reversalTotals) add(l liveSettlement, partner string) {
	t.cash, t.disc, t.off = t.cash.Add(l.amount), t.disc.Add(l.discount), t.off.Add(l.writeOff)
	if _, seen := t.recvBy[partner]; !seen {
		t.order = append(t.order, partner)
	}
	t.recvBy[partner] = t.recvBy[partner].Add(l.amount).Add(l.discount).Add(l.writeOff)
	if l.fxGainLoss.IsPositive() {
		t.gain = t.gain.Add(l.fxGainLoss)
		t.gainBy[partner] = t.gainBy[partner].Add(l.fxGainLoss)
		return
	}
	t.loss = t.loss.Add(l.fxGainLoss.Neg())
	t.lossBy[partner] = t.lossBy[partner].Add(l.fxGainLoss.Neg())
}

func reverseSettlements(ctx context.Context, tx pgx.Tx, p tenant.Principal, pay Payment, live []liveSettlement, applicationID string) (unapplied, error) {
	var out unapplied
	liveIDs := make([]string, 0, len(live))
	for _, l := range live {
		liveIDs = append(liveIDs, l.openItemID)
	}
	partners, err := itemPartners(ctx, tx, liveIDs)
	if err != nil {
		return out, err
	}
	totals := newReversalTotals()
	out.Reversed = []UnappliedSettlement{}
	for _, l := range live {
		reopened, err := reopenItem(ctx, tx, p, pay, l, applicationID)
		if err != nil {
			return out, err
		}
		totals.add(l, partners[l.openItemID])
		out.Reversed = append(out.Reversed, reopened)
		sd := rcvevents.SettlementData{OpenItemID: reopened.OpenItemID, Amount: l.amount, CashDiscount: l.discount, WriteOff: l.writeOff}
		if l.crossCurrency && l.itemCurrency != nil {
			sd.ItemCurrency, sd.ItemAmount = *l.itemCurrency, l.itemAmount
		}
		out.settled = append(out.settled, sd)
	}
	if out.Payment, err = restorePayment(ctx, tx, p, pay, totals.cash, applicationID); err != nil {
		return out, err
	}
	out.main = unapplyLines(totals.order, totals.recvBy, totals.cash, totals.disc, totals.off)
	if totals.gain.IsPositive() || totals.loss.IsPositive() {
		if out.fxLines, out.fxCurrency, err = fxMirrorLines(ctx, tx, p, out.Payment, totals); err != nil {
			return out, err
		}
	}
	return out, nil
}

func reopenItem(ctx context.Context, tx pgx.Tx, p tenant.Principal, pay Payment, l liveSettlement, applicationID string) (UnappliedSettlement, error) {
	item, err := lockAnyOpenItem(ctx, tx, l.openItemID)
	if err != nil {
		return UnappliedSettlement{}, err
	}
	newOpen := item.OpenAmount.Add(l.itemAmount)
	if newOpen.GreaterThan(item.Amount) {
		return UnappliedSettlement{}, httpx.NewProblem(http.StatusConflict, CodePaymentState, fmt.Sprintf("open item %s would be open for more than its amount", item.DocumentNumber))
	}
	status := domain.ApplyStatus(newOpen)
	if newOpen.Equal(item.Amount) {
		status = "OPEN"
	}
	if _, err := tx.Exec(ctx, `update open_item set open_amount = $3::numeric, status = $4, version = version + 1, updated_at = now()
		where tenant_id = $1 and id = $2`, p.TenantID, item.ID, newOpen.String(), status); err != nil {
		return UnappliedSettlement{}, err
	}
	var negItem any
	if l.crossCurrency {
		negItem = l.itemAmount.Neg().String()
	}
	if _, err := tx.Exec(ctx, `insert into settlement (tenant_id, id, legal_entity_id, payment_id, open_item_id, settled_amount, cash_discount_taken,
			write_off_amount, item_currency, item_amount, fx_gain_loss, reversal_of_id, application_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::numeric, $11, $12, nullif($13, '')::uuid)`, p.TenantID, ids.New(), pay.LegalEntityID, pay.ID, item.ID,
		l.amount.Neg().String(), l.discount.Neg().String(), l.writeOff.Neg().String(), l.itemCurrency, negItem, l.fxGainLoss.Neg().String(), l.id, applicationID); err != nil {
		return UnappliedSettlement{}, err
	}
	return UnappliedSettlement{SettlementID: l.id, OpenItemID: item.ID, Amount: l.amount, ItemAmount: l.itemAmount, OpenAfter: newOpen, ItemStatus: status}, nil
}

func restorePayment(ctx context.Context, tx pgx.Tx, p tenant.Principal, pay Payment, cash decimal.Decimal, applicationID string) (Payment, error) {
	var standing int
	if err := tx.QueryRow(ctx, `select count(*) from settlement s where s.tenant_id = $1 and s.payment_id = $2 and s.reversal_of_id is null
		and not exists (select 1 from settlement r where r.tenant_id = s.tenant_id and r.reversal_of_id = s.id)`, p.TenantID, pay.ID).Scan(&standing); err != nil {
		return Payment{}, err
	}
	status := "RECEIVED"
	if standing > 0 {
		status = "ON_ACCOUNT"
		if applicationID != "" && pay.UnappliedAmount.Add(cash).IsZero() {
			status = "APPLIED"
		}
	}
	return scanPayment(tx.QueryRow(ctx, `update payment set unapplied_amount = unapplied_amount + $3::numeric, status = $4, version = version + 1
		where tenant_id = $1 and id = $2 returning `+paymentColumns, p.TenantID, pay.ID, cash.String(), status))
}

func fxMirrorLines(ctx context.Context, tx pgx.Tx, p tenant.Principal, pay Payment, t *reversalTotals) ([]journalLine, string, error) {
	set, err := loadFXSetting(ctx, tx, pay.LegalEntityID)
	if err != nil {
		return nil, "", err
	}
	if set, err = fxSettingAtPosting(ctx, tx, p.TenantID, pay.LegalEntityID, pay.ID, "fx:"+pay.ID+":", set); err != nil {
		return nil, "", err
	}
	var lines []journalLine
	for _, pt := range t.order {
		if g := t.gainBy[pt]; g.IsPositive() {
			lines = append(lines, debit(set.GainAccount, g, "Realized exchange gain reversed"), withPartner(credit(domain.AccountReceivable, g, "Realized exchange gain reversed"), pt))
		}
	}
	for _, pt := range t.order {
		if l := t.lossBy[pt]; l.IsPositive() {
			lines = append(lines, withPartner(debit(domain.AccountReceivable, l, "Realized exchange loss reversed"), pt), credit(set.LossAccount, l, "Realized exchange loss reversed"))
		}
	}
	return lines, set.FunctionalCurrency, nil
}

func (u unapplied) enqueueUnapply(ctx context.Context, tx pgx.Tx, p tenant.Principal, reason string) error {
	pay := u.Payment
	key := fmt.Sprintf("unapply:%s:%d", pay.ID, pay.Version)
	if err := enqueueJournal(ctx, tx, p.TenantID, journalSpec{LegalEntityID: pay.LegalEntityID, SourceType: JournalPaymentUnapply, SourceID: pay.ID,
		SourceKey: key, DocumentID: "PAY-" + pay.ID + "-UNAPPLY", PostingDate: today(), Currency: pay.Currency, Lines: u.main}); err != nil {
		return err
	}
	if u.fxLines != nil {
		if err := enqueueJournal(ctx, tx, p.TenantID, journalSpec{LegalEntityID: pay.LegalEntityID, SourceType: JournalRealizedFX, SourceID: pay.ID,
			SourceKey: "fx-" + key, DocumentID: "PAY-" + pay.ID + "-UNAPPLY", PostingDate: today(), Currency: u.fxCurrency, Lines: u.fxLines}); err != nil {
			return err
		}
	}
	return enqueue(ctx, tx, "payment", pay.ID, rcvevents.PaymentUnapplied, p.TenantID, rcvevents.PaymentUnappliedData{
		PaymentID: pay.ID, LegalEntityID: pay.LegalEntityID, CustomerID: pay.CustomerID, Currency: pay.Currency, Settlements: u.settled, Reason: reason,
	})
}

func withPartner(l journalLine, partner string) journalLine {
	l.TradingPartnerID = partner
	return l
}

// unapplyLines is the journal of an unapply: Dr 1200 for what the settlements took off the items (one line per trading partner of the
// items, #40), Cr 1205 for the cash, Cr the discount and write-off accounts.
func unapplyLines(order []string, recvByPartner map[string]decimal.Decimal, cash, disc, off decimal.Decimal) []journalLine {
	var lines []journalLine
	for _, pt := range order {
		lines = append(lines, withPartner(debit(domain.AccountReceivable, recvByPartner[pt], "Application reversed"), pt))
	}
	return append(lines, credit(domain.AccountUnappliedCash, cash, "Payment unapplied"), credit(domain.AccountCashDiscount, disc, "Cash discount reversed"),
		credit(domain.AccountWriteOff, off, "Write-off reversed"))
}
