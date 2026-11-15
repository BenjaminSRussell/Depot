package api

import (
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/store"
)

// limitQuery reads ?limit= (1 to 500); empty is 0, the store's default.
func limitQuery(fields map[string]string, raw string) int {
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 500 {
		fields["limit"] = "must be a whole number from 1 to 500"
		return 0
	}
	return n
}

func checkQ(fields map[string]string, q string) {
	if utf8.RuneCountInString(q) > 100 {
		fields["q"] = "must be at most 100 characters"
	}
}

// listDocuments serves the document lookups of the budget check and reservation screens.
func (a *API) listDocuments(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.DocumentFilter{Type: q.Get("type"), Q: strings.TrimSpace(q.Get("q")), Number: q.Get("number")}
	fields := map[string]string{}
	if f.Type != "" && !docTypes[f.Type] && f.Type != "Reservation" {
		fields["type"] = "must be Requisition, PurchaseOrder, Invoice, Journal or Reservation"
	}
	checkQ(fields, f.Q)
	f.Limit = limitQuery(fields, q.Get("limit"))
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rows, err := a.store.ListDocuments(r.Context(), f)
	list(w, r, rows, err)
}

func (a *API) getDocument(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeDocumentNotFound, "budget document not found"))
		return
	}
	d, err := a.store.GetDocument(r.Context(), id)
	one(w, r, d, err)
}

func (a *API) listDocumentLines(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.DocumentLineFilter{DocumentID: q.Get("documentId"), Q: strings.TrimSpace(q.Get("q"))}
	fields := map[string]string{}
	if f.DocumentID != "" && !ids.Valid(f.DocumentID) {
		fields["documentId"] = "must be a UUID"
	}
	checkQ(fields, f.Q)
	f.Limit = limitQuery(fields, q.Get("limit"))
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rows, err := a.store.ListDocumentLines(r.Context(), f)
	list(w, r, rows, err)
}

func (a *API) getDocumentLine(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeDocumentNotFound, "budget document line not found"))
		return
	}
	l, err := a.store.GetDocumentLine(r.Context(), id)
	one(w, r, l, err)
}
