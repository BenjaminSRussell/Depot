package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/domain"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/store"
)

const needToKnowPath = "/v1/compliance/need-to-know"

func (a *API) registerNeedToKnow(r chi.Router) {
	r.With(authz.RequireDuty(domain.DutyCUIPolicyRead)).Get(needToKnowPath, a.getNeedToKnow)
	r.With(authz.RequireDuty(domain.DutyCUIPolicyAdmin)).Put(needToKnowPath, a.setNeedToKnow)
}

type needToKnowView struct {
	domain.NeedToKnowState
	store.NeedToKnowSettings
}

type needToKnowBody struct {
	LegalEntityID string `json:"legalEntityId"`
	Enabled       *bool  `json:"enabled"`
	Reason        string `json:"reason"`
}

func (a *API) getNeedToKnow(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntityID := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("legalEntityId")))
	if legalEntityID != "" {
		if !ids.Valid(legalEntityID) || legalEntityID == ids.Nil {
			httpx.Fail(w, r, invalid(map[string]string{"legalEntityId": "must be a UUID"}))
			return
		}
		p, _ := tenant.FromContext(r.Context())
		if !(tenant.IsTenantAdmin(p) && p.HasDuty(domain.DutyCUIPolicyRead)) && !legalEntityInScope(w, r, legalEntityID) {
			return
		}
	}
	settings, err := a.store.GetNeedToKnow(r.Context())
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	tenantFlag, overrideFlag := settings.Flags(legalEntityID)
	setETag(w, settings.VersionOf(legalEntityID))
	httpx.JSON(w, http.StatusOK, needToKnowView{domain.ResolveNeedToKnow(a.cfg.Boundary, tenantFlag, overrideFlag), settings})
}

func (a *API) setNeedToKnow(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var body needToKnowBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	body.LegalEntityID = strings.ToLower(strings.TrimSpace(body.LegalEntityID))
	fields := map[string]string{}
	if body.Enabled == nil {
		fields["enabled"] = "is required (true or false)"
	}
	if !domain.ValidDesignationReason(body.Reason) {
		fields["reason"] = fmt.Sprintf("is required (1..%d characters)", domain.MaxDesignationReason)
	}
	if body.LegalEntityID != "" && (!ids.Valid(body.LegalEntityID) || body.LegalEntityID == ids.Nil) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	in := store.NeedToKnowInput{LegalEntityID: body.LegalEntityID, Enabled: *body.Enabled, Reason: body.Reason, ExpectedVersion: version}
	p, _ := tenant.FromContext(r.Context())
	if refusal := store.NeedToKnowRefusal(p, in); refusal != nil {
		slog.WarnContext(r.Context(), "need-to-know change refused", "tenantId", p.TenantID, "actor", p.UserID, "legalEntityId", in.LegalEntityID, "enabled", in.Enabled)
		httpx.Fail(w, r, refusal)
		return
	}
	out, err := a.store.SetNeedToKnow(r.Context(), in, a.now())
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}
