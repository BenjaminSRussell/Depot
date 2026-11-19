package api

import (
	"cmp"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
	"github.com/BenjaminSRussell/depot/services/audit/internal/store"
)

const (
	dutyExport     = "audit.export"
	maxFilterChars = 256
)

type entityItem struct {
	Entity       string     `json:"entity"`
	Changes      int64      `json:"changes"`
	LastChangeAt *time.Time `json:"lastChangeAt,omitempty"`
	Recorded     bool       `json:"recorded"`
}

func (a *API) listRecords(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	rf, ok := readFilter(w, r)
	if !ok {
		return
	}
	f := store.RecordFilter{Entity: rf.Entity, RecordID: rf.RecordID, UserID: rf.UserID, Field: r.URL.Query().Get("field"), From: rf.From, To: rf.To}
	for name, v := range map[string]string{"entity": f.Entity, "recordId": f.RecordID, "userId": f.UserID, "field": f.Field} {
		if len(v) > maxFilterChars {
			badQuery(w, r, name, "is too long")
			return
		}
	}
	limit, after, ok := pageParams(w, r)
	if !ok {
		return
	}
	f.AfterSeq, f.Limit = after, limit+1
	rows, err := a.store.ListRecords(r.Context(), f)
	if err != nil {
		a.queryFailed(w, r, err)
		return
	}
	out := map[string]any{}
	if len(rows) > limit {
		rows = rows[:limit]
		out["nextCursor"] = nextCursor(rows[limit-1].Seq)
	}
	p, _ := tenant.FromContext(r.Context())
	out["items"] = maskRecords(rows, p)
	a.queries.Add(1)
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listEntities(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	seen, err := a.store.ListEntities(r.Context())
	if err != nil {
		a.queryFailed(w, r, err)
		return
	}
	items := make([]entityItem, 0, len(seen))
	have := map[string]bool{}
	for _, e := range seen {
		last := e.LastChangeAt
		items = append(items, entityItem{Entity: e.Entity, Changes: e.Changes, LastChangeAt: &last, Recorded: true})
		have[e.Entity] = true
	}
	for _, name := range domain.KnownEntities() {
		if !have[name] {
			items = append(items, entityItem{Entity: name})
		}
	}
	slices.SortFunc(items, func(x, y entityItem) int { return cmp.Compare(x.Entity, y.Entity) })
	a.queries.Add(1)
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) query(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	entity := chi.URLParam(r, "entity")
	holdsRead := p.HasDuty(dutyRead)
	if !holdsRead && !holdsReadDuty(p, entity) {
		forbidden(w, r, "need audit.read or the read duty of the record")
		return
	}
	order := r.URL.Query().Get("order")
	if order != "" && order != "asc" && order != "desc" {
		badQuery(w, r, "order", "must be asc or desc")
		return
	}
	rows, err := a.store.Query(r.Context(), entity, chi.URLParam(r, "recordId"))
	if err != nil {
		a.queryFailed(w, r, err)
		return
	}
	if !holdsRead {
		for _, rec := range rows {
			// A record with no legal entity is tenant-wide master data; holding its read duty is enough.
			if rec.LegalEntityID != "" && !tenant.InLegalEntityScope(p, rec.LegalEntityID) {
				forbidden(w, r, "the record belongs to a legal entity outside your scope")
				return
			}
		}
	}
	if order == "desc" {
		slices.Reverse(rows)
	}
	a.queries.Add(1)
	httpx.JSON(w, http.StatusOK, map[string]any{"items": maskRecords(rows, p)})
}

func holdsReadDuty(p tenant.Principal, entity string) bool {
	duty, ok := domain.ReadDuty(entity)
	return ok && p.HasDuty(duty)
}

func forbidden(w http.ResponseWriter, r *http.Request, detail string) {
	httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", detail))
}

func (a *API) queryFailed(w http.ResponseWriter, r *http.Request, err error) {
	a.queriesFailed.Add(1)
	slog.Error("audit query failed", "err", err, "path", r.URL.Path, "correlationId", middleware.GetReqID(r.Context()))
	httpx.Fail(w, r, err)
}

func maskRecords(rows []store.Record, p tenant.Principal) []store.Record {
	if p.HasDuty(dutyExport) {
		return rows
	}
	out := make([]store.Record, len(rows))
	for i, rec := range rows {
		if domain.IsSensitive(rec.Entity, rec.Field) {
			rec.OldValue, rec.NewValue = domain.Mask(rec.OldValue), domain.Mask(rec.NewValue)
		}
		out[i] = rec
	}
	return out
}
