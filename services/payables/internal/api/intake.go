package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/money"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/payables/internal/store"
)

// The intake of vendor invoices received electronically (#35, 35-4): the EDI engine posts a cXML InvoiceDetailRequest here under the
// service-only duty of its grant, and an AP clerk reads it with open-item.read. Nothing is posted to the ledger.
const (
	dutyIntake     = "payables.vendor-invoice.intake"
	dutyIntakeRead = "open-item.read"
	// dutyIntakeReview lets an AP clerk decide a received invoice: record the vendor invoice posted from it, or reject it with a reason.
	dutyIntakeReview = "payables.vendor-invoice.review"
	pathIntake       = "/v1/payables/vendor-invoice-intake"

	CodeIntakeReplay    = "AP-VII-001"
	CodeIntakeDuplicate = "AP-VII-002"
	// CodeIntakeDecided is a received invoice already decided otherwise.
	CodeIntakeDecided = "AP-VII-003"
	// CodeIntakeInvoiceUnknown is a vendor invoice that is not posted for the received invoice's vendor and legal entity.
	CodeIntakeInvoiceUnknown = "AP-VII-004"
	// CodeIntakeInvoiceTaken is a vendor invoice already recorded against another received invoice.
	CodeIntakeInvoiceTaken = "AP-VII-005"
	maxDecisionNote        = 1000

	maxIntakeLines = 500
)

var intakePurposes = map[string]bool{"standard": true, "creditMemo": true, "debitMemo": true, "lineLevelCreditMemo": true}

// VendorInvoiceIntakeStore stores and reads received vendor invoices; *store.Postgres implements it.
type VendorInvoiceIntakeStore interface {
	ReceiveVendorInvoice(ctx context.Context, in store.VendorInvoiceIntake) (store.VendorInvoiceIntake, bool, error)
	ListVendorInvoiceIntake(ctx context.Context, f store.IntakeFilter, limit int) ([]store.VendorInvoiceIntake, error)
	GetVendorInvoiceIntake(ctx context.Context, id string) (store.VendorInvoiceIntake, error)
	DecideVendorInvoiceIntake(ctx context.Context, id string, d store.IntakeDecision) (store.VendorInvoiceIntake, bool, error)
}

var _ VendorInvoiceIntakeStore = (*store.Postgres)(nil)

// WithVendorInvoiceIntake mounts the vendor-invoice intake on s. Without it, the intake mounts on the vendor-payments store when that
// store also implements it (the service's Postgres store does).
func (a *API) WithVendorInvoiceIntake(s VendorInvoiceIntakeStore) *API {
	a.intake = s
	return a
}

func (a *API) intakeStore() VendorInvoiceIntakeStore {
	if a.intake != nil {
		return a.intake
	}
	s, _ := a.vendorPayments.(VendorInvoiceIntakeStore)
	return s
}

func (a *API) registerVendorInvoiceIntake(r chi.Router) {
	if a.intakeStore() == nil {
		return
	}
	r.With(authz.RequireDuty(dutyIntake)).Post(pathIntake, a.receiveVendorInvoice)
	r.With(authz.RequireDuty(dutyIntakeRead)).Get(pathIntake, a.listVendorInvoiceIntake)
	r.With(authz.RequireDuty(dutyIntakeRead)).Get(pathIntake+"/{id}", a.getVendorInvoiceIntake)
	r.With(authz.RequireDuty(dutyIntakeReview)).Post(pathIntake+"/{id}:resolve", a.resolveVendorInvoiceIntake)
	r.With(authz.RequireDuty(dutyIntakeReview)).Post(pathIntake+"/{id}:reject", a.rejectVendorInvoiceIntake)
}

type intakeLineRequest struct {
	LineNo    string `json:"lineNo"`
	Item      string `json:"item"`
	Quantity  string `json:"quantity"`
	Unit      string `json:"unit"`
	UnitPrice string `json:"unitPrice"`
}

type intakeRequest struct {
	LegalEntityID       string              `json:"legalEntityId"`
	Source              string              `json:"source"`
	SourceRef           string              `json:"sourceRef"`
	VendorID            string              `json:"vendorId"`
	InvoiceNumber       string              `json:"invoiceNumber"`
	InvoiceDate         string              `json:"invoiceDate"`
	Purpose             string              `json:"purpose"`
	PurchaseOrderID     string              `json:"purchaseOrderId"`
	PurchaseOrderNumber string              `json:"purchaseOrderNumber"`
	Currency            string              `json:"currency"`
	Gross               string              `json:"gross"`
	Lines               []intakeLineRequest `json:"lines"`
}

// receiveVendorInvoice stores a vendor invoice a procurement network sent. The same document again (source, sourceRef) is answered
// with the stored row (200); with other content it is 409 AP-VII-001; a vendor invoice number the legal entity already received from
// another document is 409 AP-VII-002.
func (a *API) receiveVendorInvoice(w http.ResponseWriter, r *http.Request) {
	if !a.validTenant(w, r) {
		return
	}
	caller, _ := tenant.FromContext(r.Context())
	var req intakeRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	le, ok := a.resolveEntity(w, r, req.LegalEntityID)
	if !ok {
		return
	}
	in, fields := intakeFromRequest(req)
	if len(fields) > 0 {
		a.invalid(w, r, fields)
		return
	}
	in.LegalEntityID, in.ReceivedBy = le, caller.UserID
	if in.ReceivedBy == "" {
		in.ReceivedBy = "service:" + caller.Service
	}
	out, created, err := a.intakeStore().ReceiveVendorInvoice(r.Context(), in.WithContentHash(intakeHash(le, req)))
	switch {
	case errors.Is(err, store.ErrIntakeReplayConflict):
		a.problem(w, r, http.StatusConflict, CodeIntakeReplay, "document "+req.SourceRef+" was already received with different content")
	case errors.Is(err, store.ErrIntakeDuplicateInvoice):
		a.problem(w, r, http.StatusConflict, CodeIntakeDuplicate, "invoice "+req.InvoiceNumber+" of this vendor was already received from another document")
	case err != nil:
		a.fault(w, r, "vendor invoice intake failed", err)
	case created:
		slog.Info("vendor invoice received", "legalEntityId", le, "source", in.Source, "sourceRef", in.SourceRef, "intakeId", out.ID, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusCreated, out)
	default:
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, out)
	}
}

func intakeFromRequest(req intakeRequest) (store.VendorInvoiceIntake, map[string]string) {
	fields := map[string]string{}
	in := store.VendorInvoiceIntake{Source: req.Source, SourceRef: req.SourceRef, InvoiceNumber: strings.TrimSpace(req.InvoiceNumber),
		InvoiceDate: req.InvoiceDate, Purpose: req.Purpose, PurchaseOrderNumber: strings.TrimSpace(req.PurchaseOrderNumber), Currency: req.Currency}
	if in.Source != "CXML" && in.Source != "X12_810" {
		fields["source"] = "must be CXML or X12_810"
	}
	if in.SourceRef == "" || len(in.SourceRef) > 200 {
		fields["sourceRef"] = "is required, at most 200 characters"
	}
	var ok bool
	if in.VendorID, ok = ids.Canonical(req.VendorID); !ok {
		fields["vendorId"] = "must be a UUID"
	}
	if in.InvoiceNumber == "" || len(in.InvoiceNumber) > 100 {
		fields["invoiceNumber"] = "is required, at most 100 characters"
	}
	parseDay(fields, "invoiceDate", req.InvoiceDate, true)
	if in.Purpose == "" {
		in.Purpose = "standard"
	} else if !intakePurposes[in.Purpose] {
		fields["purpose"] = "must be standard, creditMemo, debitMemo or lineLevelCreditMemo"
	}
	if req.PurchaseOrderID != "" {
		if in.PurchaseOrderID, ok = ids.Canonical(req.PurchaseOrderID); !ok {
			fields["purchaseOrderId"] = "must be a UUID"
		}
	}
	if len(in.PurchaseOrderNumber) > 100 {
		fields["purchaseOrderNumber"] = "must be at most 100 characters"
	}
	if !currencyPattern.MatchString(in.Currency) {
		fields["currency"] = "must be a 3-letter currency code"
	}
	if n := len(req.Lines); n == 0 || n > maxIntakeLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", maxIntakeLines)
	}
	places := int32(money.MinorUnits(in.Currency))
	in.Net = decimal.Zero
	for i, l := range req.Lines {
		at := func(f string) string { return "lines[" + strconv.Itoa(i) + "]." + f }
		line := store.IntakeLine{LineNo: strings.TrimSpace(l.LineNo), Item: strings.TrimSpace(l.Item), Unit: strings.TrimSpace(l.Unit)}
		if line.LineNo == "" || len(line.LineNo) > 20 {
			fields[at("lineNo")] = "is required, at most 20 characters"
		}
		if len(line.Item) > 100 || len(line.Unit) > 20 {
			fields[at("item")] = "item at most 100 and unit at most 20 characters"
		}
		qty, err := decimal.NewFromString(l.Quantity)
		if err != nil || qty.IsZero() {
			fields[at("quantity")] = "must be a non-zero decimal"
		}
		price, err := decimal.NewFromString(l.UnitPrice)
		if err != nil || price.IsNegative() {
			fields[at("unitPrice")] = "must be a decimal of at least 0"
		}
		line.Quantity, line.UnitPrice = qty, price
		line.Amount = qty.Mul(price).Round(places)
		in.Net = in.Net.Add(line.Amount)
		in.Lines = append(in.Lines, line)
	}
	in.Gross = in.Net
	if req.Gross != "" {
		g, err := decimal.NewFromString(req.Gross)
		if err != nil {
			fields["gross"] = "must be a decimal"
		}
		in.Gross = g
	}
	if in.Net.Abs().GreaterThan(decimal.New(1, 14)) || in.Gross.Abs().GreaterThan(decimal.New(1, 14)) {
		fields["gross"] = "the invoice amounts are out of range"
	}
	return in, fields
}

// intakeHash is the fingerprint of the sender's document the replay check compares: the legal entity and the request as sent.
func intakeHash(le string, req intakeRequest) string {
	req.LegalEntityID = le
	raw, _ := json.Marshal(req)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (a *API) listVendorInvoiceIntake(w http.ResponseWriter, r *http.Request) {
	if !a.validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	le, ok := a.resolveEntity(w, r, q.Get("legalEntityId"))
	if !ok {
		return
	}
	fields := map[string]string{}
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			fields["limit"] = "must be a whole number from 1 to 200"
		}
		limit = n
	}
	vendor := q.Get("vendorId")
	if vendor != "" && !ids.Valid(vendor) {
		fields["vendorId"] = "must be a UUID"
	}
	status := q.Get("status")
	if status != "" && status != "RECEIVED" && status != "POSTED" && status != "REJECTED" {
		fields["status"] = "must be RECEIVED, POSTED or REJECTED"
	}
	afterAt, afterID, ok := parseOpeningCursor(q.Get("cursor"))
	if !ok {
		fields["cursor"] = "is not a cursor this list returned"
	}
	if len(fields) > 0 {
		a.invalid(w, r, fields)
		return
	}
	items, err := a.intakeStore().ListVendorInvoiceIntake(r.Context(), store.IntakeFilter{LegalEntityID: le, VendorID: vendor, Status: status, AfterAt: afterAt, AfterID: afterID}, limit+1)
	if err != nil {
		a.fault(w, r, "vendor invoice intake list failed", err)
		return
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		next = base64.RawURLEncoding.EncodeToString([]byte(last.ReceivedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": next})
}

func (a *API) getVendorInvoiceIntake(w http.ResponseWriter, r *http.Request) {
	if !a.validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		a.problem(w, r, http.StatusNotFound, codeNotFound, "vendor invoice not found")
		return
	}
	out, err := a.intakeStore().GetVendorInvoiceIntake(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		a.problem(w, r, http.StatusNotFound, codeNotFound, "vendor invoice not found")
		return
	}
	if err != nil {
		a.fault(w, r, "vendor invoice intake read failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type intakeDecisionRequest struct {
	VendorInvoiceID string `json:"vendorInvoiceId"`
	Reason          string `json:"reason"`
}

// resolveVendorInvoiceIntake records the vendor invoice an AP clerk posted from a received invoice (in receiving, against the receipt
// lines): the received invoice is then POSTED and leaves the worklist. The vendor invoice must be an open item of the vendor in the
// received invoice's legal entity, so it has to be posted first (422 AP-VII-004), and closes one received invoice only (409 AP-VII-005).
// The same call again replays; another decision after one is 409 AP-VII-003.
func (a *API) resolveVendorInvoiceIntake(w http.ResponseWriter, r *http.Request) {
	var req intakeDecisionRequest
	id, ok := a.intakeDecisionRequest(w, r, &req)
	if !ok {
		return
	}
	invoice, ok := ids.Canonical(req.VendorInvoiceID)
	if !ok {
		a.invalid(w, r, map[string]string{"vendorInvoiceId": "must be the vendor invoice posted from this received invoice (a UUID)"})
		return
	}
	a.decideIntake(w, r, id, store.IntakeDecision{Status: "POSTED", VendorInvoiceID: invoice})
}

// rejectVendorInvoiceIntake rejects a received invoice with a reason; it leaves the worklist as REJECTED.
func (a *API) rejectVendorInvoiceIntake(w http.ResponseWriter, r *http.Request) {
	var req intakeDecisionRequest
	id, ok := a.intakeDecisionRequest(w, r, &req)
	if !ok {
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || len([]rune(reason)) > maxDecisionNote {
		a.invalid(w, r, map[string]string{"reason": "is required, at most 1000 characters"})
		return
	}
	a.decideIntake(w, r, id, store.IntakeDecision{Status: "REJECTED", Note: reason})
}

func (a *API) intakeDecisionRequest(w http.ResponseWriter, r *http.Request, into *intakeDecisionRequest) (string, bool) {
	if !a.validTenant(w, r) {
		return "", false
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		a.problem(w, r, http.StatusNotFound, codeNotFound, "vendor invoice not found")
		return "", false
	}
	if err := httpx.Decode(r, into); err != nil {
		a.reject(w, r, err)
		return "", false
	}
	return id, true
}

func (a *API) decideIntake(w http.ResponseWriter, r *http.Request, id string, d store.IntakeDecision) {
	caller, _ := tenant.FromContext(r.Context())
	d.By = caller.UserID
	out, changed, err := a.intakeStore().DecideVendorInvoiceIntake(r.Context(), id, d)
	switch {
	case errors.Is(err, store.ErrNotFound):
		a.problem(w, r, http.StatusNotFound, codeNotFound, "vendor invoice not found")
	case errors.Is(err, store.ErrIntakeDecided):
		a.problem(w, r, http.StatusConflict, CodeIntakeDecided, "the received invoice was already decided otherwise")
	case errors.Is(err, store.ErrIntakeInvoiceUnknown):
		a.problem(w, r, http.StatusUnprocessableEntity, CodeIntakeInvoiceUnknown, "the vendor invoice is not posted for this vendor in this legal entity; post it in receiving first")
	case errors.Is(err, store.ErrIntakeInvoiceTaken):
		a.problem(w, r, http.StatusConflict, CodeIntakeInvoiceTaken, "that vendor invoice already closes another received invoice")
	case err != nil:
		a.fault(w, r, "vendor invoice intake decision failed", err)
	default:
		if !changed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}
