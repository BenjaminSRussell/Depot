package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/cxmlmap"
	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

// The vendor 810 (#35, 35-5): an X12 810 a vendor sends us is its invoice to us. It is not the self-billed invoice of a customer, so
// it has its own route (POST /inbound/x12/vendor) and its partner its own profile (PUT /partners/{id}/vendor-profile: the vendor and
// the legal entity it invoices). Each 810 lands in payables' vendor-invoice intake (source X12_810) for an AP clerk to match to the
// purchase order and the goods received; nothing is posted. A partner with a vendor profile has all its 810s read this way, whichever
// route or transport brought them.
const (
	// CodeNoVendorProfile refuses a vendor 810 of a partner that has no vendor profile.
	CodeNoVendorProfile = "EDI-0043"
	// CodeVendorOrSelfBilling refuses a vendor profile on a self-billing partner, and self-billing on a vendor partner.
	CodeVendorOrSelfBilling = "EDI-0044"

	vendorInvoiceKind = "VENDOR_INVOICE"
)

// VendorInvoices takes an invoice into payables' intake; sources.Client implements it (WithPayables). Nil leaves a vendor 810 recorded
// and MAPPED, for POST /messages/{id}:post once payables is configured (EDI_PAYABLES_URL).
type VendorInvoices interface {
	ReceiveVendorInvoice(ctx context.Context, legalEntityID, key string, body sources.VendorInvoiceRequest) (sources.VendorInvoiceRef, error)
}

type vendorProfileBody struct {
	LegalEntityID string `json:"legalEntityId"`
	VendorID      string `json:"vendorId"`
}

func (a *API) putVendorProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	var body vendorProfileBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !validID(body.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	} else if caller, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(caller, body.LegalEntityID) {
		fields["legalEntityId"] = "must be a legal entity of the caller"
	}
	if !validID(body.VendorID) {
		fields["vendorId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	p := store.VendorProfile{LegalEntityID: strings.ToLower(body.LegalEntityID), VendorID: strings.ToLower(body.VendorID)}
	// The check sees every legal entity: a partner that self-bills in another one is still a self-billing partner.
	if self, err := a.store.HasSelfBillConfig(a.tenantWide(r.Context()), id); err != nil {
		httpx.Fail(w, r, err)
		return
	} else if self {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeVendorOrSelfBilling, "the partner has self-billing settings, so its 810s are self-billed invoices of a customer; a partner is a customer or a vendor for its 810s, not both"))
		return
	}
	if err := a.store.PutVendorProfile(r.Context(), id, p); errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("partner"))
		return
	} else if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

func (a *API) getVendorProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	p, err := a.store.GetVendorProfile(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("vendor profile"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

// receiveVendorX12 serves POST /inbound/x12/vendor: an interchange of a vendor, whose 810s are vendor invoices. The partner must have a
// vendor profile (409 EDI-0043 otherwise, nothing is recorded).
func (a *API) receiveVendorX12(w http.ResponseWriter, r *http.Request) {
	a.receiveRouteWith(w, r, domain.SyntaxX12, true)
}

// vendorPartner reports whether the partner's 810s are vendor invoices: it has a vendor profile.
func (a *API) vendorPartner(ctx context.Context, partnerID string) (bool, error) {
	// Routing must not depend on the operator's legal-entity scope, so the profile is read across entities.
	_, err := a.store.GetVendorProfile(a.tenantWide(ctx), partnerID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// vendorMessages are the ids of the MAPPED 810 messages of an interchange that are vendor invoices.
func vendorMessages(messages []store.Message) []string {
	var out []string
	for _, m := range messages {
		if m.TransactionSet == "810" && m.Status == "MAPPED" && strings.Contains(string(m.Canonical), `"vendor":true`) {
			out = append(out, m.ID)
		}
	}
	return out
}

// postVendorReceived puts the vendor invoices an interchange just brought in into payables' intake, best effort: the vendor already has
// its 997, so a failure is only logged and leaves the message MAPPED for POST /messages/{id}:post.
func (a *API) postVendorReceived(ctx context.Context, ids []string) {
	if a.VendorInvoices == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for _, id := range ids {
		if _, err := a.postVendor810(ctx, id); err != nil {
			slog.InfoContext(ctx, "vendor 810 not put into the intake on receipt", "messageId", id, "err", err)
		}
	}
}

// VendorPostResult is where a vendor 810 went.
type VendorPostResult struct {
	MessageID string `json:"messageId"`
	Status    string `json:"status"`
	Kind      string `json:"downstreamKind,omitempty"`
	Ref       string `json:"downstreamRef,omitempty"`
}

// vendorParked is a vendor 810 parked instead of posted: FAILED with the code and detail until the cause is fixed and :post is called.
type vendorParked struct{ domain.Park }

var errVendorNotConfigured = errors.New("payables is not configured")

// notVendorInvoice says the message is not an inbound vendor 810.
type notVendorInvoice struct{ detail string }

func (e *notVendorInvoice) Error() string { return e.detail }

// postVendor810 puts one inbound vendor 810 into payables' vendor-invoice intake as source X12_810, for the vendor and legal entity of the
// partner's vendor profile. The message id is the sourceRef and the idempotency key, so posting it again returns the intake row already
// created. A document that cannot be mapped (no currency, no lines, a quantity of zero) is parked.
func (a *API) postVendor810(ctx context.Context, id string) (VendorPostResult, error) {
	if a.VendorInvoices == nil {
		return VendorPostResult{}, errVendorNotConfigured
	}
	st, err := a.store.MessageForPost(ctx, id)
	if err != nil {
		return VendorPostResult{}, err
	}
	var inv domain.Invoice
	if st.Direction != "IN" || st.Standard != "X12" || st.TransactionSet != "810" || json.Unmarshal(st.Canonical, &inv) != nil || !inv.Vendor {
		return VendorPostResult{}, &notVendorInvoice{"only an inbound vendor 810 is put into the vendor-invoice intake"}
	}
	if st.Status == "POSTED" {
		kind, ref, err := a.store.DownstreamOf(ctx, id)
		return VendorPostResult{MessageID: id, Status: st.Status, Kind: kind, Ref: ref}, err
	}
	if !st.Postable() {
		return VendorPostResult{}, &notVendorInvoice{"message is " + st.Status + "; only a mapped or parked document is posted (reprocess a failed mapping first)"}
	}
	profile, err := a.store.GetVendorProfile(a.tenantWide(ctx), st.PartnerID)
	if err != nil {
		return VendorPostResult{}, err
	}
	req, park := vendorInvoiceRequest(profile, id, inv)
	if park != nil {
		if err := a.store.ParkMessage(ctx, id, park.Code, park.Detail); err != nil {
			return VendorPostResult{}, err
		}
		return VendorPostResult{}, &vendorParked{*park}
	}
	actor, ok := tenant.FromContext(ctx)
	if !ok {
		return VendorPostResult{}, fmt.Errorf("%w: no tenant in context", sources.ErrForbidden)
	}
	sp, err := tenant.ServicePrincipal(cxmlmap.ServicePrincipal, actor.TenantID, profile.LegalEntityID)
	if err != nil {
		return VendorPostResult{}, fmt.Errorf("%w: %v", sources.ErrForbidden, err)
	}
	ref, err := a.VendorInvoices.ReceiveVendorInvoice(tenant.With(ctx, sp), profile.LegalEntityID, id, req)
	var refused *sources.DownstreamRefusedError
	if errors.As(err, &refused) {
		park := domain.Park{Code: domain.CodeCXMLDownstream, Detail: "payables " + refused.Code + ": " + refused.Detail}
		if err := a.store.ParkMessage(ctx, id, park.Code, park.Detail); err != nil {
			return VendorPostResult{}, err
		}
		return VendorPostResult{}, &vendorParked{park}
	}
	if err != nil {
		return VendorPostResult{}, err
	}
	if err := a.store.MarkMapped(ctx, id, vendorInvoiceKind, ref.ID); err != nil {
		return VendorPostResult{}, fmt.Errorf("record the vendor invoice %s: %w", ref.ID, err)
	}
	return VendorPostResult{MessageID: id, Status: "POSTED", Kind: vendorInvoiceKind, Ref: ref.ID}, nil
}

// vendorInvoiceRequest turns the canonical vendor 810 into payables' intake request, or says why it cannot be (a park).
func vendorInvoiceRequest(p store.VendorProfile, messageID string, inv domain.Invoice) (sources.VendorInvoiceRequest, *domain.Park) {
	park := func(detail string) (sources.VendorInvoiceRequest, *domain.Park) {
		return sources.VendorInvoiceRequest{}, &domain.Park{Code: domain.CodeCXMLInvoiceTarget, Detail: detail}
	}
	date, err := time.Parse("20060102", inv.Date)
	switch {
	case inv.Number == "":
		return park("the 810 has no invoice number (BIG02)")
	case err != nil:
		return park("the 810 has no usable invoice date (BIG01 " + inv.Date + ")")
	case len(inv.Currency) != 3:
		return park("invoice " + inv.Number + " names no currency (CUR02); payables needs one")
	case len(inv.Lines) == 0:
		return park("invoice " + inv.Number + " has no lines")
	}
	req := sources.VendorInvoiceRequest{LegalEntityID: p.LegalEntityID, Source: "X12_810", SourceRef: messageID, VendorID: p.VendorID, InvoiceNumber: inv.Number,
		InvoiceDate: date.Format("2006-01-02"), Purpose: "standard", PurchaseOrderNumber: inv.PONumber, Currency: inv.Currency}
	if inv.CreditNote {
		req.Purpose = "creditMemo"
	}
	if inv.Total.IsPositive() {
		req.Gross = inv.Total.String()
	}
	for _, l := range inv.Lines {
		if l.Quantity.IsZero() || l.UnitPrice.IsNegative() {
			return park("line " + l.LineNo + " of invoice " + inv.Number + " has no quantity or a negative price")
		}
		req.Lines = append(req.Lines, sources.VendorInvoiceLine{LineNo: l.LineNo, Item: vendorItem(l), Quantity: l.Quantity.String(), Unit: l.UOM, UnitPrice: l.UnitPrice.String()})
	}
	return req, nil
}

// vendorItem picks the item a vendor's line names: our buyer's part (BP), else the vendor's part (VP), else its first reference.
func vendorItem(l domain.InvoiceLine) string {
	for _, want := range []string{"BP", "VP"} {
		for _, r := range l.ItemRefs {
			if r.Qualifier == want {
				return r.Value
			}
		}
	}
	if len(l.ItemRefs) > 0 {
		return l.ItemRefs[0].Value
	}
	return ""
}

// postVendorMessage is POST /messages/{id}:post for a vendor 810.
func (a *API) postVendorMessage(w http.ResponseWriter, r *http.Request, id string) {
	res, err := a.postVendor810(r.Context(), id)
	var parked *vendorParked
	var notVendor *notVendorInvoice
	switch {
	case err == nil:
		httpx.JSON(w, http.StatusOK, res)
	case errors.Is(err, errVendorNotConfigured):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeSourceDown, "payables is not configured (EDI_PAYABLES_URL)"))
	case errors.As(err, &notVendor):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNotPostable, notVendor.detail))
	case errors.As(err, &parked):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, parked.Code, "invoice parked: "+parked.Detail))
	case errors.Is(err, sources.ErrForbidden):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, CodeSourceForbidden, "the caller may not act on this partner's legal entity in payables"))
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNoVendorProfile, "partner has no vendor profile; PUT /partners/{id}/vendor-profile first"))
	default:
		slog.ErrorContext(r.Context(), "edi vendor 810 post failed", "err", err, "messageId", id)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodeSourceDown, "payables could not take the invoice; post again"))
	}
}

// tenantWide is ctx with the caller's tenant and every legal entity, for the checks that decide how a partner's documents are routed.
func (a *API) tenantWide(ctx context.Context) context.Context {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return ctx
	}
	return store.SystemContext(ctx, p.TenantID)
}
