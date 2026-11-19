package api

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/receivables/internal/store"
)

const (
	dutyFXSetting      = "receivables.fxsetting.manage"
	dutyPaymentReverse = "receivables.payment.reverse"
)

// fxRoutes mounts the realized-FX profile of a legal entity (BR-30.4): whether it settles across currencies, its functional
// currency and the gain and loss accounts.
func (a *API) fxRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyView)).Get("/fx-settings", a.getFXSetting)
	r.With(authz.RequireDuty(dutyFXSetting)).Put("/fx-settings", a.putFXSetting)
}

func (a *API) getFXSetting(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if !ids.Valid(le) {
		badQuery(w, r, "legalEntityId", "must be a UUID")
		return
	}
	if !scopeAllows(w, r, le) {
		return
	}
	out, err := a.store.GetFXSetting(r.Context(), le)
	if err != nil {
		a.fail("get fx setting", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) putFXSetting(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var in store.FXSetting
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(in.LegalEntityID) {
		writeFields(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	if !scopeAllows(w, r, in.LegalEntityID) {
		return
	}
	if err := store.ValidateFXSetting(&in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := a.store.PutFXSetting(r.Context(), in)
	if err != nil {
		a.fail("save fx setting", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type unapplyRequest struct {
	Reason string `json:"reason"`
	// SettlementID limits the reversal to one settlement of the payment; omitted, every live application is reversed.
	SettlementID string `json:"settlementId"`
}

// unapplyPayment reverses every live application of a payment (#30): the items it settled are open again by what it took off them
// and the cash is unapplied; If-Match carries the payment version the user reviewed.
func (a *API) unapplyPayment(w http.ResponseWriter, r *http.Request, id string) {
	if !require(w, r, dutyPaymentReverse) {
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var in unapplyRequest
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	if len(in.Reason) > 500 {
		writeFields(w, r, map[string]string{"reason": "must be at most 500 characters"})
		return
	}
	var out store.UnapplyResult
	if in.SettlementID != "" {
		if !ids.Valid(in.SettlementID) {
			writeFields(w, r, map[string]string{"settlementId": "must be a UUID"})
			return
		}
		out, err = a.store.UnapplySettlement(r.Context(), id, in.SettlementID, version, in.Reason)
	} else {
		out, err = a.store.UnapplyPayment(r.Context(), id, version, in.Reason)
	}
	if err != nil {
		a.fail("unapply payment", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Payment.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// reversePayment undoes the cash receipt of a payment that was never applied; the journal it queues mirrors the receipt's own
// journal, so the bank account the receipt was booked to is the one that is credited back.
func (a *API) reversePayment(w http.ResponseWriter, r *http.Request, id string) {
	if !require(w, r, dutyPaymentReverse) {
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := a.store.ReversePayment(r.Context(), id, version)
	if err != nil {
		a.fail("reverse payment", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

type reverseApplicationRequest struct {
	ApplicationID string `json:"applicationId"`
	Reason        string `json:"reason"`
}

func (a *API) reverseApplication(w http.ResponseWriter, r *http.Request, id string) {
	if !require(w, r, dutyPayment) {
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key, ok := requiredKey(w, r)
	if !ok {
		return
	}
	var in reverseApplicationRequest
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	applicationID, validID := ids.Canonical(in.ApplicationID)
	if !validID {
		fields["applicationId"] = "must be a UUID"
	}
	if n := utf8.RuneCountInString(in.Reason); strings.TrimSpace(in.Reason) == "" || n > 500 {
		fields["reason"] = "must be 1 to 500 characters"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	out, err := a.store.ReverseApplication(r.Context(), id, version, store.ReverseApplicationRequest{ApplicationID: applicationID, Reason: in.Reason}, key)
	if err != nil {
		a.fail("reverse payment application", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Payment.Version))
	httpx.JSON(w, http.StatusOK, out)
}
