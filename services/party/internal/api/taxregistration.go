package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/party/internal/store"
)

const (
	// dutyTaxRegistrationRead is the service-only duty fulfillment and receiving read partners' tax registrations with (#13).
	dutyTaxRegistrationRead = "party.tax-registration.read"
	maxTaxRegistrationIDs   = 200
)

// WithTaxRegistrations sets the store of partner tax registrations; without it the routes are not served.
func (a *API) WithTaxRegistrations(s store.TaxRegistrationStore) *API {
	a.taxRegs = s
	return a
}

func (a *API) registerTaxRegistrations(r chi.Router) {
	r.With(authz.RequireDuty(dutyWrite)).Put("/v1/party/partners/{id}/tax-registration", a.putTaxRegistration)
	r.With(authz.RequireAnyDuty(dutyRead, dutyTaxRegistrationRead)).Get("/v1/party/partners:tax-registrations", a.listTaxRegistrations)
}

type taxRegistrationRequest struct {
	Country string `json:"country"`
	VATID   string `json:"vatId"`
}

// putTaxRegistration sets the country a customer or vendor role is VAT registered in (the partner country of its goods movements).
func (a *API) putTaxRegistration(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req taxRegistrationRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := a.taxRegs.SetTaxRegistration(r.Context(), chi.URLParam(r, "id"), strings.ToUpper(strings.TrimSpace(req.Country)), strings.ToUpper(strings.ReplaceAll(req.VATID, " ", "")))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// listTaxRegistrations answers the registrations of the roles named in ids (at most 200); a role with none is left out.
func (a *API) listTaxRegistrations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var wanted []string
	for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			if !ids.Valid(id) {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "ids must be a comma-separated list of customer or vendor ids"))
				return
			}
			wanted = append(wanted, id)
		}
	}
	if len(wanted) == 0 || len(wanted) > maxTaxRegistrationIDs {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", fmt.Sprintf("name 1 to %d ids", maxTaxRegistrationIDs)))
		return
	}
	out, err := a.taxRegs.TaxRegistrations(r.Context(), wanted)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}
