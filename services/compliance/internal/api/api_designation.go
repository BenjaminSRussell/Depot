package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/domain"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/store"
)

const DutyDataDesignationWrite = domain.DutyDataDesignationWrite

func (a *API) registerDesignations(r chi.Router) {
	r.With(authz.RequireDuty(DutyDataDesignationWrite)).Post("/v1/compliance/data-designations", a.createDesignation)
}

func designationFields(in *store.DesignationInput) map[string]string {
	fields := map[string]string{}
	in.LegalEntityID = strings.ToLower(strings.TrimSpace(in.LegalEntityID))
	switch in.Scope {
	case domain.ScopeTenant:
		if in.LegalEntityID != "" {
			fields["legalEntityId"] = "must be absent for TENANT scope"
		}
	case domain.ScopeLegalEntity:
		if !ids.Valid(in.LegalEntityID) || in.LegalEntityID == ids.Nil {
			fields["legalEntityId"] = "is required for LEGAL_ENTITY scope and must be a UUID"
		}
	case domain.ScopeRecord:
		fields["scope"] = "record scope is not available yet"
	default:
		fields["scope"] = "must be TENANT or LEGAL_ENTITY"
	}
	if in.Class != domain.ClassCUI {
		fields["class"] = "must be CUI"
	}
	if !domain.ValidDesignationReason(in.Reason) {
		fields["reason"] = fmt.Sprintf("is required (1..%d characters)", domain.MaxDesignationReason)
	}
	if utf8.RuneCountInString(strings.TrimSpace(in.Category)) > domain.MaxDesignationCategory {
		fields["category"] = fmt.Sprintf("is at most %d characters", domain.MaxDesignationCategory)
	}
	return fields
}

func (a *API) createDesignation(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var in store.DesignationInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := designationFields(&in); len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	if a.cfg.Boundary != domain.BoundaryCUI {
		p, _ := tenant.FromContext(r.Context())
		slog.WarnContext(r.Context(), "CUI designation refused outside the CUI deployment", "tenantId", p.TenantID, "actor", p.UserID, "scope", in.Scope)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, domain.CodeCUIBoundary, "CUI data belongs in the isolated CUI deployment"))
		return
	}
	if in.Scope == domain.ScopeLegalEntity && !legalEntityInScope(w, r, in.LegalEntityID) {
		return
	}
	out, err := a.store.Designate(r.Context(), in, key, a.now())
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusCreated, out)
}
