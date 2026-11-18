package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/numbering/internal/domain"
)

const maxText = 200

type reserveRequest struct {
	LeaseSeconds *int `json:"leaseSeconds"`
}

type confirmRequest struct {
	DocumentRef string `json:"documentRef"`
}

type voidRequest struct {
	Reason string `json:"reason"`
}

// RegisterReservations mounts the reserve, confirm, void and voided-log routes.
func (a *API) RegisterReservations(r chi.Router) {
	write := authz.RequireAnyDuty(dutyWrite, dutyAllocate)
	r.With(write).Post("/v1/numbering/sequences/{code}/reserve", a.reserve)
	r.With(write).Post("/v1/numbering/reservations/{id}/confirm", a.confirm)
	r.With(write).Post("/v1/numbering/reservations/{id}/void", a.void)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/numbering/sequences/{code}/voided", a.voidedLog)
}

func invalid(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
	p.Errors = fields
	httpx.Fail(w, r, p)
}

func (a *API) storeFailed(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.failed.Add(1)
	slog.Error(what+" failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	httpx.Fail(w, r, err)
}

func requiredText(value string) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(value))
	return n >= 1 && n <= maxText
}

func (a *API) reserve(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req reserveRequest
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	lease, ok := domain.LeaseFor(req.LeaseSeconds)
	if !ok {
		invalid(w, r, map[string]string{"leaseSeconds": "must be 1 to 3600"})
		return
	}
	res, err := a.store.Reserve(r.Context(), chi.URLParam(r, "code"), r.Header.Get(idempotencyKey), lease)
	if err != nil {
		a.storeFailed(w, r, "reserve", err)
		return
	}
	a.reserved.Add(1)
	httpx.JSON(w, http.StatusCreated, res)
}

func (a *API) confirm(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	var req confirmRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(id) {
		fields["id"] = "must be a UUID"
	}
	if !requiredText(req.DocumentRef) {
		fields["documentRef"] = fmt.Sprintf("must be 1 to %d characters", maxText)
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	res, err := a.store.Confirm(r.Context(), id, strings.TrimSpace(req.DocumentRef))
	if err != nil {
		a.storeFailed(w, r, "confirm", err)
		return
	}
	a.confirmed.Add(1)
	httpx.JSON(w, http.StatusOK, res)
}

func (a *API) void(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	var req voidRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(id) {
		fields["id"] = "must be a UUID"
	}
	if !requiredText(req.Reason) {
		fields["reason"] = fmt.Sprintf("must be 1 to %d characters", maxText)
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	res, err := a.store.Void(r.Context(), id, strings.TrimSpace(req.Reason))
	if err != nil {
		a.storeFailed(w, r, "void", err)
		return
	}
	a.voided.Add(1)
	httpx.JSON(w, http.StatusOK, res)
}

func (a *API) voidedLog(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	limit := defaultPage
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPage {
			fields["limit"] = fmt.Sprintf("must be 1 to %d", maxPage)
		} else {
			limit = n
		}
	}
	after := q.Get("after")
	if after == "" {
		after = zeroCursor
	} else if !ids.Valid(after) {
		fields["after"] = "must be a cursor from a previous page"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	items, err := a.store.Voided(r.Context(), chi.URLParam(r, "code"), after, limit)
	if err != nil {
		a.storeFailed(w, r, "voided log", err)
		return
	}
	next := ""
	if len(items) == limit {
		next = items[len(items)-1].ID
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "next": next})
}
