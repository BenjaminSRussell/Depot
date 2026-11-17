package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/money"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/billing/internal/domain"
	"github.com/BenjaminSRussell/depot/services/billing/internal/store"
)

// Posting a matched self-billed document as the legal invoice (#35, 35-5). A customer that self-bills under an agreement issues the
// invoice for our deliveries itself; once the EDI engine has matched its document to what we shipped, billing posts it as the legal
// customer invoice of the legal entity, under the document's own number, with the tax the document states (the customer is the
// issuer, so no tax engine is asked), and receivables, the ledger and the tax report see it as any posted invoice.
const (
	dutySelfBilledPost = "billing.self-billed.post"
	pathSelfBilled     = "/v1/billing/self-billed-invoices"

	// CodeNotSelfBilled refuses a self-billed document of a customer with no SELF_BILLED rule in force on the document's date.
	CodeNotSelfBilled = "BIL-0152"
	// CodeSelfBilledAgreement refuses a document that names another agreement than the customer's rule.
	CodeSelfBilledAgreement = "BIL-0153"
	// CodeSelfBilledReplay is a document posted before with other content.
	CodeSelfBilledReplay = "BIL-0154"
	// CodeSelfBilledNumber is a customer document number already posted for this customer.
	CodeSelfBilledNumber = "BIL-0155"
	// CodeSelfBilledFuture refuses a document dated after today.
	CodeSelfBilledFuture = "BIL-0156"
	// CodeSelfBilledPeriod refuses a document no open period can take (the period service is not configured or answers badly, or the
	// document's period and the current one are both closed).
	CodeSelfBilledPeriod = "BIL-0157"

	maxSourceRef = 200
	maxDocNumber = 100
)

// SelfBilledStore posts self-billed documents; *store.Postgres implements it.
type SelfBilledStore interface {
	PostSelfBilled(ctx context.Context, inv store.Invoice, number, documentNumber string) (store.Invoice, bool, error)
}

var _ SelfBilledStore = (*store.Postgres)(nil)

type selfBilledLine struct {
	Description    string `json:"description"`
	Qty            string `json:"qty"`
	UnitPrice      string `json:"unitPrice"`
	DiscountPct    string `json:"discountPct,omitempty"`
	Tax            string `json:"tax"`
	RevenueAccount string `json:"revenueAccount"`
	TaxCode        string `json:"taxCode"`
	OrderID        string `json:"orderId,omitempty"`
	OrderLineID    string `json:"orderLineId,omitempty"`
	ProductID      string `json:"productId,omitempty"`
}

type selfBilledRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	CustomerID    string `json:"customerId"`
	// SourceRef is the sender's document at the caller (the EDI message id): the same one posts once.
	SourceRef string `json:"sourceRef"`
	// DocumentNumber is the customer's number of the document; it becomes the invoice number.
	DocumentNumber  string           `json:"documentNumber"`
	DocumentDate    string           `json:"documentDate"`
	AgreementRef    string           `json:"agreementRef,omitempty"`
	Currency        string           `json:"currency"`
	PaymentTermDays int              `json:"paymentTermDays"`
	Lines           []selfBilledLine `json:"lines"`
}

// postSelfBilled posts the matched self-billed document as a legal invoice. The same sourceRef again with the same content returns
// the posted invoice (200, Idempotent-Replayed); with other content it is 409 BIL-0154. The customer must have a SELF_BILLED rule in
// force on the document's date (409 BIL-0152) and, when the document names an agreement, that one (409 BIL-0153).
// PeriodChecker says whether the receivables period of a date is open; periods.Client implements it (close).
type PeriodChecker interface {
	Open(ctx context.Context, legalEntityID, ledgerID string, date time.Time) (bool, error)
}

// WithPeriods sets the check that keeps a self-billed invoice out of a closed period.
func (a *API) WithPeriods(p PeriodChecker) *API {
	a.periods = p
	return a
}

// selfBilledNumber is the billing-side number of a self-billed invoice: the customer's document number in the customer's own namespace
// (the tail of its id), so two customers' numbers and billing's own sequence never collide.
func selfBilledNumber(customerID, documentNumber string) string {
	id := strings.ReplaceAll(customerID, "-", "")
	return "SB-" + id[len(id)-12:] + "-" + documentNumber
}

func (a *API) postSelfBilled(w http.ResponseWriter, r *http.Request) {
	sb, ok := a.store.(SelfBilledStore)
	rules := a.rules()
	if !ok || rules == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "self-billed invoices need the database store"))
		return
	}
	var req selfBilledRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	inv, fields := a.selfBilledInvoice(req)
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller is scoped to another legal entity"))
		return
	}
	day, _ := time.Parse(dateLayout, req.DocumentDate)
	rule, found, err := rules.GetBillingRule(r.Context(), req.LegalEntityID, req.CustomerID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !found || !rule.SelfBilledOn(day) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNotSelfBilled, "the customer has no SELF_BILLED rule in force on "+req.DocumentDate+", so its document is not our legal invoice"))
		return
	}
	if req.AgreementRef != "" && req.AgreementRef != rule.AgreementRef {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeSelfBilledAgreement, "the document names agreement "+req.AgreementRef+", the customer's rule names "+rule.AgreementRef))
		return
	}
	// The posting date: the document's date when its receivables period is open, else today (the first open date), the customer's date
	// staying the invoice date with a note. A document dated after today is refused: only the future is out of the question.
	today := a.now().UTC().Truncate(24 * time.Hour)
	if day.After(today.AddDate(0, 0, 1)) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeSelfBilledFuture, "the document is dated "+req.DocumentDate+", after today; a customer cannot invoice a delivery in the future"))
		return
	}
	profile, known := a.profiles[req.LegalEntityID]
	if a.periods == nil || !known {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeSelfBilledPeriod, "the period of the document cannot be checked: close and the legal entity's ledger are not configured, so nothing is posted"))
		return
	}
	posting := day
	if day.After(today) {
		posting = today
	}
	open, err := a.periods.Open(r.Context(), req.LegalEntityID, profile.LedgerID, posting)
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeSelfBilledPeriod, "the period of the document could not be checked: "+err.Error()))
		return
	}
	if !open {
		later, err := a.periods.Open(r.Context(), req.LegalEntityID, profile.LedgerID, today)
		if err != nil {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeSelfBilledPeriod, "the current period could not be checked: "+err.Error()))
			return
		}
		if !later {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeSelfBilledPeriod, "the receivables period of the document and the current one are both closed; open a period or ask close"))
			return
		}
		inv.PostingNote = "The customer's document is dated " + req.DocumentDate + ", in a closed receivables period; posted on " + today.Format(dateLayout) + ", the first open date."
		posting = today
	}
	inv.PostingDate = posting
	// Only the document's own content decides whether a repeat is the same document: the partner's account, tax code and terms may change.
	raw, _ := json.Marshal(documentContent(req))
	sum := sha256.Sum256(raw)
	inv.RequestHash = hex.EncodeToString(sum[:])
	inv.IdempotencyKey = store.SelfBilledKeyPrefix + req.SourceRef
	posted, created, err := sb.PostSelfBilled(r.Context(), inv, selfBilledNumber(req.CustomerID, req.DocumentNumber), req.DocumentNumber)
	switch {
	case errors.Is(err, store.ErrSelfBilledKeyReuse):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeSelfBilledReplay, "document "+req.SourceRef+" was already posted with different content"))
		return
	case errors.Is(err, store.ErrSelfBilledNumberTaken):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeSelfBilledNumber, "the customer already sent another document with number "+req.DocumentNumber))
		return
	case err != nil:
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(posted.Version))
	if !created {
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, toDTO(posted))
		return
	}
	a.invoicesPosted.Add(1)
	httpx.JSON(w, http.StatusCreated, toDTO(posted))
}

// selfBilledInvoice checks the request and builds the posted shell: lines with the document's own tax, totals, the document date as
// the invoice and posting date. Problems are returned in fields.
func (a *API) selfBilledInvoice(req selfBilledRequest) (store.Invoice, map[string]string) {
	fields := map[string]string{}
	for name, v := range map[string]string{"legalEntityId": req.LegalEntityID, "customerId": req.CustomerID} {
		if !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if n := len(req.SourceRef); n < 1 || n > maxSourceRef {
		fields["sourceRef"] = fmt.Sprintf("is required, at most %d characters", maxSourceRef)
	}
	if n := len(strings.TrimSpace(req.DocumentNumber)); n < 1 || n > maxDocNumber || req.DocumentNumber != strings.TrimSpace(req.DocumentNumber) {
		fields["documentNumber"] = fmt.Sprintf("is required: the customer's number of the document, 1 to %d characters, no surrounding spaces", maxDocNumber)
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	date, err := time.Parse(dateLayout, req.DocumentDate)
	if err != nil {
		fields["documentDate"] = "must be a YYYY-MM-DD date"
	}
	if req.PaymentTermDays < 0 || req.PaymentTermDays > 365 {
		fields["paymentTermDays"] = "must be 0 to 365"
	}
	if n := len(req.Lines); n == 0 || n > a.maxLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", a.maxLines)
	}
	lines := make([]domain.Line, 0, len(req.Lines))
	taxes := make([]decimal.Decimal, 0, len(req.Lines))
	for i, l := range req.Lines {
		lines = append(lines, toLine(i, lineDTO{Description: l.Description, Qty: l.Qty, UnitPrice: l.UnitPrice, DiscountPct: l.DiscountPct, RevenueAccount: l.RevenueAccount, TaxCode: l.TaxCode}, fields))
		tax := decimal.Zero
		if !decimalText.MatchString(l.Tax) {
			fields[fmt.Sprintf("lines[%d].tax", i)] = "must be the tax the document states: a non-negative decimal with at most 6 fraction digits"
		} else {
			tax = decimal.RequireFromString(l.Tax)
		}
		taxes = append(taxes, tax)
		for name, v := range map[string]string{"orderId": l.OrderID, "orderLineId": l.OrderLineID, "productId": l.ProductID} {
			if v != "" && !ids.Valid(v) {
				fields[fmt.Sprintf("lines[%d].%s", i, name)] = "must be a UUID"
			}
		}
	}
	if len(fields) > 0 {
		return store.Invoice{}, fields
	}
	nets, totals, err := domain.Compute(req.Currency, lines, taxes, decimal.Zero)
	if err != nil {
		fields["lines"] = err.Error()
		return store.Invoice{}, fields
	}
	inv := store.Invoice{ID: ids.New(), LegalEntityID: req.LegalEntityID, CustomerID: req.CustomerID, InvoiceAccountID: req.CustomerID,
		Type: domain.TypeShipment, Status: domain.StatusProposal, Currency: req.Currency, InvoiceDate: date, PostingDate: date,
		DueDate: date.AddDate(0, 0, req.PaymentTermDays), PaymentTermDays: req.PaymentTermDays, Totals: totals}
	exp := money.MinorUnits(req.Currency)
	for i := range lines {
		l := store.Line{Line: lines[i], Net: nets[i], Tax: taxes[i].RoundBank(exp)}
		l.Source = domain.Source{OrderID: req.Lines[i].OrderID, OrderLineID: req.Lines[i].OrderLineID, ProductID: req.Lines[i].ProductID}
		inv.Lines = append(inv.Lines, l)
	}
	return inv, fields
}

// documentContent is what the customer's document says, without the settings the sender adds (accounts, tax code, terms).
func documentContent(req selfBilledRequest) any {
	type line struct{ Qty, UnitPrice, DiscountPct, Tax string }
	lines := make([]line, len(req.Lines))
	for i, l := range req.Lines {
		lines[i] = line{l.Qty, l.UnitPrice, l.DiscountPct, l.Tax}
	}
	return struct {
		LegalEntityID, CustomerID, DocumentNumber, DocumentDate, Currency string
		Lines                                                             []line
	}{req.LegalEntityID, req.CustomerID, req.DocumentNumber, req.DocumentDate, req.Currency, lines}
}
