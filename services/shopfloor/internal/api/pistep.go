package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// DutyPIStepMaintain maintains the PI step library: drafting, publishing and versioning step templates (FR-25.15, D7).
// Reading needs the view duty.
const DutyPIStepMaintain = "shopfloor.pistep.maintain"

// registerPISteps mounts the PI step library's routes.
func (a *API) registerPISteps(r chi.Router) {
	r.With(authz.RequireDuty(DutyPIStepMaintain)).Post("/v1/shopfloor/pi-step-templates", a.createPIStep)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/pi-step-templates", a.listPISteps)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/pi-step-templates/{id}", a.getPIStep)
	r.With(authz.RequireDuty(DutyPIStepMaintain)).Put("/v1/shopfloor/pi-step-templates/{id}", a.updatePIStep)
	r.With(authz.RequireDuty(DutyPIStepMaintain)).Post("/v1/shopfloor/pi-step-templates/{id}:publish", a.publishPIStep)
	r.With(authz.RequireDuty(DutyPIStepMaintain)).Post("/v1/shopfloor/pi-step-templates/{id}:new-version", a.newPIStepVersion)
}

type piStepRequest struct {
	Code       string             `json:"code"`
	Name       string             `json:"name"`
	CategoryID *string            `json:"categoryId"`
	Spec       domain.Instruction `json:"spec"`
}

func (req piStepRequest) input(creating bool) (store.PIStepInput, map[string]string) {
	in := store.PIStepInput{Code: strings.TrimSpace(req.Code), Name: strings.TrimSpace(req.Name), CategoryID: req.CategoryID, Spec: req.Spec}
	f := domain.ValidateStepTemplate(req.Spec)
	if creating && !piCode.MatchString(in.Code) {
		f["code"] = "must be 1 to 40 characters: upper-case letters, digits, '_', '.' or '-'"
	}
	if n := len(in.Name); n < 1 || n > 120 {
		f["name"] = "must be 1 to 120 characters"
	}
	if in.CategoryID != nil && !ids.Valid(*in.CategoryID) {
		f["categoryId"] = "must be a PI category's UUID"
	}
	return in, f
}

func piStepID(w http.ResponseWriter, r *http.Request) (string, bool) {
	return pathID(w, r, store.CodePIStepNotFound, "PI step template")
}

func (a *API) createPIStep(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "drafting a PI step template")
	if !ok {
		return
	}
	var req piStepRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, f := req.input(true)
	if len(f) > 0 {
		validationFailed(w, r, f)
		return
	}
	out, err := a.store.CreatePIStep(r.Context(), in, user)
	if err != nil {
		a.fail(w, r, "create PI step template", err)
		return
	}
	w.Header().Set("ETag", etag(out.Rev))
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) listPISteps(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r, 200, 1000)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := map[string]string{}
	if s := q.Get("status"); s != "" && s != "draft" && s != "published" && s != "superseded" {
		f["status"] = "must be draft, published or superseded"
	}
	version := 0
	if v := q.Get("version"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			f["version"] = "must be a version number of 1 or more"
		}
		version = n
	}
	if c := q.Get("categoryId"); c != "" && !ids.Valid(c) {
		f["categoryId"] = "must be a PI category's UUID"
	}
	if k := q.Get("kind"); k != "" {
		known := false
		for _, kind := range domain.StepTemplateKinds() {
			known = known || kind == k
		}
		if !known {
			f["kind"] = "must be one of the instruction categories"
		}
	}
	if len(f) > 0 {
		validationFailed(w, r, f)
		return
	}
	out, err := a.store.ListPISteps(r.Context(), store.PIStepFilter{Status: q.Get("status"), Code: q.Get("code"), Version: version, CategoryID: strings.ToLower(q.Get("categoryId")), Kind: q.Get("kind"), Limit: limit})
	if err != nil {
		a.fail(w, r, "list PI step templates", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.PIStepTemplate]{Items: out})
}

func (a *API) getPIStep(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := piStepID(w, r)
	if !ok {
		return
	}
	out, err := a.store.GetPIStep(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get PI step template", err)
		return
	}
	w.Header().Set("ETag", etag(out.Rev))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) updatePIStep(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "editing a PI step template")
	if !ok {
		return
	}
	id, ok := piStepID(w, r)
	if !ok {
		return
	}
	rev, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req piStepRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, f := req.input(false)
	if len(f) > 0 {
		validationFailed(w, r, f)
		return
	}
	out, err := a.store.UpdatePIStep(r.Context(), id, rev, in, user)
	if err != nil {
		a.fail(w, r, "update PI step template", err)
		return
	}
	w.Header().Set("ETag", etag(out.Rev))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) publishPIStep(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "publishing a PI step template")
	if !ok {
		return
	}
	id, ok := piStepID(w, r)
	if !ok {
		return
	}
	rev, ok := ifMatch(w, r)
	if !ok {
		return
	}
	out, err := a.store.PublishPIStep(r.Context(), id, rev, user)
	if err != nil {
		a.fail(w, r, "publish PI step template", err)
		return
	}
	w.Header().Set("ETag", etag(out.Rev))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) newPIStepVersion(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "versioning a PI step template")
	if !ok {
		return
	}
	id, ok := piStepID(w, r)
	if !ok {
		return
	}
	out, err := a.store.NewPIStepVersion(r.Context(), id, user)
	if err != nil {
		a.fail(w, r, "new PI step template version", err)
		return
	}
	w.Header().Set("ETag", etag(out.Rev))
	httpx.JSON(w, http.StatusCreated, out)
}

// templateRefFields checks the step library references of a document against the library: each names a template
// version that exists and is published or superseded (a draft is not final), of the same kind as the step that carries
// it. A superseded version stays valid: the sheets that were made from it keep it, and it never changes.
func (a *API) templateRefFields(ctx context.Context, doc domain.RecipeDoc, f map[string]string) error {
	var refs []domain.TemplateRef
	for _, ref := range doc.TemplateRefs() {
		if piCode.MatchString(ref.Code) && ref.Version >= 1 { // a malformed reference is reported by the document check
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		return nil
	}
	found, err := a.store.ResolvePISteps(ctx, refs)
	if err != nil {
		return err
	}
	doc.WalkInstructions(func(path string, in domain.Instruction) {
		if in.Template == nil || !piCode.MatchString(in.Template.Code) || in.Template.Version < 1 {
			return
		}
		t, ok := found[*in.Template]
		switch {
		case !ok:
			f[path+".template"] = fmt.Sprintf("step template %s version %d does not exist", in.Template.Code, in.Template.Version)
		case t.Status == "draft":
			f[path+".template"] = fmt.Sprintf("step template %s version %d is a draft; publish it before a sheet uses it", t.Code, t.Version)
		case t.Kind != in.Category:
			f[path+".template"] = fmt.Sprintf("step template %s is a %s step, not a %s step", t.Code, t.Kind, in.Category)
		default:
			var spec domain.Instruction
			if err := json.Unmarshal(t.Spec, &spec); err == nil && domain.TemplateDrift(in, spec) {
				f[path+".template"] = fmt.Sprintf("the step differs from step template %s version %d; take the template as it is, or remove the reference and keep your own step", t.Code, t.Version)
			}
		}
	})
	return nil
}
