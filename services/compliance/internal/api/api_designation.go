package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/domain"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/store"
)

const (
	DutyDataDesignationWrite   = domain.DutyDataDesignationWrite
	DutyDataDesignationRead    = domain.DutyDataDesignationRead
	DutyDataDesignationResolve = domain.DutyDataDesignationResolve

	DutyDataDesignationMarkingRead = domain.DutyDataDesignationMarkingRead

	resolveConcurrency = 8
)

var resolveDeadline = 8 * time.Second

func (a *API) registerDesignations(r chi.Router) {
	r.With(authz.RequireDuty(DutyDataDesignationWrite)).Post("/v1/compliance/data-designations", a.createDesignation)
	r.With(authz.RequireDuty(DutyDataDesignationWrite)).Patch("/v1/compliance/data-designations/{id}", a.changeDesignation)
	r.With(authz.RequireAnyDuty(DutyDataDesignationRead, DutyDataDesignationResolve, DutyDataDesignationMarkingRead)).Post("/v1/compliance/data-designations:resolve", a.resolveDesignations)
	r.With(authz.RequireDuty(DutyDataDesignationRead)).Get("/v1/compliance/data-designations", a.listDesignations)
	r.With(authz.RequireDuty(DutyDataDesignationRead)).Get("/v1/compliance/data-designations/{id}", a.getDesignation)
	r.With(authz.RequireDuty(DutyDataDesignationRead)).Get("/v1/compliance/data-designations/{id}/history", a.designationHistory)
}

func tenantWideCaller(w http.ResponseWriter, r *http.Request) bool {
	p, _ := tenant.FromContext(r.Context())
	if tenant.IsTenantAdmin(p) && p.HasDuty(DutyDataDesignationWrite) {
		return true
	}
	slog.WarnContext(r.Context(), "tenant data designation refused", "tenantId", p.TenantID, "actor", p.UserID)
	httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "a tenant-wide designation needs the tenant.administer duty, the designation duty and no legal-entity scope"))
	return false
}

func (a *API) changeDesignation(w http.ResponseWriter, r *http.Request) {
	id, ok := conflictID(w, r)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var in store.DesignationChange
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if in.Class != domain.ClassCUI && in.Class != domain.ClassNONE {
		fields["class"] = "must be CUI or NONE"
	}
	if !domain.ValidDesignationReason(in.Reason) {
		fields["reason"] = fmt.Sprintf("is required (1..%d characters)", domain.MaxDesignationReason)
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	if in.Class == domain.ClassCUI && a.cfg.Boundary != domain.BoundaryCUI {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, domain.CodeCUIBoundary, "CUI data belongs in the isolated CUI deployment"))
		return
	}
	out, err := a.store.ChangeDesignation(r.Context(), id, version, in, a.now())
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getDesignation(w http.ResponseWriter, r *http.Request) {
	id, ok := conflictID(w, r)
	if !ok {
		return
	}
	out, err := a.store.GetDesignation(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) designationHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := conflictID(w, r)
	if !ok {
		return
	}
	items, err := a.store.DesignationHistory(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) listDesignations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.DesignationFilter{Scope: strings.ToUpper(strings.TrimSpace(q.Get("scope"))), EntityType: strings.ToLower(strings.TrimSpace(q.Get("entityType"))), RecordID: strings.ToLower(strings.TrimSpace(q.Get("recordId")))}
	fields := map[string]string{}
	if f.Scope != "" && f.Scope != domain.ScopeTenant && f.Scope != domain.ScopeLegalEntity && f.Scope != domain.ScopeRecord {
		fields["scope"] = "must be TENANT, LEGAL_ENTITY or RECORD"
	}
	if f.EntityType != "" && !domain.SupportedEntityTypes[f.EntityType] {
		fields["entityType"] = "is not a designatable entity type"
	}
	if f.RecordID != "" && !ids.Valid(f.RecordID) {
		fields["recordId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	items, err := a.store.ListDesignations(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func designationFields(in *store.DesignationInput) map[string]string {
	fields := map[string]string{}
	in.LegalEntityID = strings.ToLower(strings.TrimSpace(in.LegalEntityID))
	entityType, supported := domain.ParseEntityType(in.EntityType)
	in.EntityType, in.RecordID = entityType, strings.ToLower(strings.TrimSpace(in.RecordID))
	if in.Scope != domain.ScopeRecord && (in.EntityType != "" || in.RecordID != "") {
		fields["entityType"] = "is only allowed for RECORD scope"
	}
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
		if in.LegalEntityID != "" {
			fields["legalEntityId"] = "is resolved by the server for RECORD scope"
		}
		if !supported {
			fields["entityType"] = "is not a designatable entity type"
		}
		if !ids.Valid(in.RecordID) || in.RecordID == ids.Nil {
			fields["recordId"] = "is required for RECORD scope and must be a UUID"
		}
	default:
		fields["scope"] = "must be TENANT, LEGAL_ENTITY or RECORD"
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
	if in.Scope == domain.ScopeTenant && !tenantWideCaller(w, r) {
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
	if in.Scope == domain.ScopeRecord && !a.recordLegalEntity(w, r, &in) {
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

func (a *API) ownerLegalEntity(ctx context.Context, entityType, recordID string) (string, error) {
	if a.cfg.Owner == nil {
		return "", errOwnerUnavailable
	}
	return a.cfg.Owner.LegalEntity(ctx, entityType, recordID)
}

func (a *API) recordLegalEntity(w http.ResponseWriter, r *http.Request, in *store.DesignationInput) bool {
	p, _ := tenant.FromContext(r.Context())
	le, err := a.ownerLegalEntity(r.Context(), in.EntityType, in.RecordID)
	if errors.Is(err, ErrOwnerNotFound) || (err == nil && !tenant.InLegalEntityScope(p, le)) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeRecordNotFound, "no such record"))
		return false
	}
	if err != nil {
		slog.WarnContext(r.Context(), "owner lookup failed, designation refused", "tenantId", p.TenantID, "actor", p.UserID, "entityType", in.EntityType, "err", err)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, domain.CodeOwnerUnavailable, "the owning service could not be reached"))
		return false
	}
	in.LegalEntityID = le
	return true
}

type resolveRef struct {
	EntityType string `json:"entityType"`
	RecordID   string `json:"recordId"`
}

type resolveKey struct{ entityType, recordID string }

func (a *API) ownerLegalEntities(ctx context.Context, keys []resolveKey) map[resolveKey]string {
	p, _ := tenant.FromContext(ctx)
	found := make(map[resolveKey]string, len(keys))
	var mu sync.Mutex
	var wg sync.WaitGroup
	var failed atomic.Int64
	ctx, cancel := context.WithTimeout(withOwnerBudget(ctx), resolveDeadline)
	defer cancel()
	slots := make(chan struct{}, resolveConcurrency)
	for _, key := range keys {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			failed.Add(1)
			continue
		}
		wg.Add(1)
		go func() {
			defer func() { <-slots; wg.Done() }()
			le, err := a.ownerLegalEntity(ctx, key.entityType, key.recordID)
			if errors.Is(err, ErrOwnerNotFound) || (err == nil && !tenant.InLegalEntityScope(p, le)) {
				return
			}
			if err != nil {
				failed.Add(1)
				return
			}
			mu.Lock()
			found[key] = le
			mu.Unlock()
		}()
	}
	wg.Wait()
	if n := failed.Load(); n > 0 {
		slog.WarnContext(ctx, "owner lookups failed, refs resolve UNKNOWN", "tenantId", p.TenantID, "actor", p.UserID, "failed", n)
	}
	return found
}

func (a *API) resolveDesignations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var in struct {
		Refs []resolveRef `json:"refs"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(in.Refs) == 0 {
		httpx.Fail(w, r, invalid(map[string]string{"refs": fmt.Sprintf("is required (1..%d refs)", domain.MaxResolveRefs)}))
		return
	}
	if len(in.Refs) > domain.MaxResolveRefs {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeTooManyRefs, fmt.Sprintf("at most %d refs per call", domain.MaxResolveRefs)))
		return
	}
	keyOf := make([]*resolveKey, len(in.Refs))
	var distinct []resolveKey
	seen := map[resolveKey]bool{}
	for i, ref := range in.Refs {
		entityType, supported := domain.ParseEntityType(ref.EntityType)
		recordID, valid := ids.Canonical(strings.TrimSpace(ref.RecordID))
		if !supported || !valid || recordID == ids.Nil {
			continue
		}
		key := resolveKey{entityType, recordID}
		keyOf[i] = &key
		if !seen[key] {
			seen[key] = true
			distinct = append(distinct, key)
		}
	}
	p, _ := tenant.FromContext(r.Context())
	claimless := len(tenant.LegalEntityScopeList(p)) == 0
	owners := map[resolveKey]string{}
	if claimless {
		for _, key := range distinct {
			owners[key] = ids.Nil
		}
	} else {
		owners = a.ownerLegalEntities(r.Context(), distinct)
	}
	var resolvable []store.ResolveRef
	for _, key := range distinct {
		if le, ok := owners[key]; ok {
			resolvable = append(resolvable, store.ResolveRef{EntityType: key.entityType, RecordID: key.recordID, LegalEntityID: le})
		}
	}
	answers := map[resolveKey]store.ResolvedRef{}
	if len(resolvable) > 0 {
		resolved, err := a.store.Resolve(r.Context(), resolvable)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		for _, item := range resolved {
			answers[resolveKey{item.EntityType, item.RecordID}] = item
		}
	}
	items := make([]store.ResolvedRef, len(in.Refs))
	for i, ref := range in.Refs {
		items[i] = store.ResolvedRef{EntityType: ref.EntityType, RecordID: ref.RecordID, Class: domain.ClassUNKNOWN, Source: domain.SourceUnresolved}
		if keyOf[i] == nil {
			continue
		}
		if answer, ok := answers[*keyOf[i]]; ok && (!claimless || answer.Class == domain.ClassCUI) {
			items[i].Class, items[i].Source = answer.Class, answer.Source
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
