package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/intercompany/internal/domain"
)

type LinkStore interface {
	CreateLink(ctx context.Context, l domain.DocumentLink) (domain.DocumentLink, bool, error)
	ListLinks(ctx context.Context, pairID string) ([]domain.DocumentLink, error)
	CompensateLink(ctx context.Context, id string) (domain.DocumentLink, error)
}

func (a *API) WithLinks(s LinkStore) *API {
	a.links = s
	return a
}

func (a *API) linksReady(w http.ResponseWriter, r *http.Request) bool {
	if !guard(w, r) {
		return false
	}
	if a.links == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, domain.CodeNotConfigured, "document links are not configured for this service"))
		return false
	}
	return true
}

type linkBody struct {
	ID           string    `json:"id,omitempty"`
	PairID       string    `json:"pairId"`
	SourceEntity string    `json:"sourceEntity"`
	SourceType   string    `json:"sourceType"`
	SourceID     string    `json:"sourceId"`
	MirrorEntity string    `json:"mirrorEntity"`
	MirrorType   string    `json:"mirrorType"`
	MirrorID     string    `json:"mirrorId"`
	PricingBasis string    `json:"pricingBasis"`
	Status       string    `json:"status,omitempty"`
	CreatedBy    string    `json:"createdBy,omitempty"`
	CreatedAt    time.Time `json:"createdAt,omitzero"`
}

func toLinkBody(l domain.DocumentLink) linkBody {
	return linkBody{ID: l.ID, PairID: l.PairID, SourceEntity: l.SourceEntity, SourceType: l.SourceType, SourceID: l.SourceID, MirrorEntity: l.MirrorEntity,
		MirrorType: l.MirrorType, MirrorID: l.MirrorID, PricingBasis: l.PricingBasis, Status: l.Status, CreatedBy: l.CreatedBy, CreatedAt: l.CreatedAt}
}

func (a *API) createLink(w http.ResponseWriter, r *http.Request) {
	if !a.linksReady(w, r) {
		return
	}
	var body linkBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	l, created, err := a.links.CreateLink(r.Context(), domain.DocumentLink{PairID: strings.TrimSpace(body.PairID), SourceEntity: strings.TrimSpace(body.SourceEntity),
		SourceType: body.SourceType, SourceID: strings.TrimSpace(body.SourceID), MirrorEntity: strings.TrimSpace(body.MirrorEntity), MirrorType: body.MirrorType,
		MirrorID: strings.TrimSpace(body.MirrorID), PricingBasis: strings.TrimSpace(body.PricingBasis)})
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	status := http.StatusOK
	if created {
		a.linksCreated.Add(1)
		status = http.StatusCreated
	}
	httpx.JSON(w, status, toLinkBody(l))
}

func (a *API) listLinks(w http.ResponseWriter, r *http.Request) {
	if !a.linksReady(w, r) {
		return
	}
	list, err := a.links.ListLinks(r.Context(), strings.TrimSpace(r.URL.Query().Get("pairId")))
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := struct {
		Items []linkBody `json:"items"`
	}{Items: make([]linkBody, 0, len(list))}
	for _, l := range list {
		out.Items = append(out.Items, toLinkBody(l))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) compensateLink(w http.ResponseWriter, r *http.Request) {
	if !a.linksReady(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeLinkNotFound, "no intercompany document link with id "+id))
		return
	}
	l, err := a.links.CompensateLink(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.linksCompensated.Add(1)
	httpx.JSON(w, http.StatusOK, toLinkBody(l))
}
