package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/tenant"

	"github.com/BenjaminSRussell/depot/services/finreport/internal/datascope"
	"github.com/BenjaminSRussell/depot/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/depot/services/finreport/internal/store"
)

const libraryUUID = "33333333-3333-3333-3333-333333333333"

type fakeDecider struct {
	scope datascope.Scope
	err   error
}

func (f fakeDecider) Decide(context.Context, tenant.Principal) (datascope.Scope, error) {
	return f.scope, f.err
}

type scopedStore struct {
	fakeStore
	runScope string
	filtered decimal.Decimal
	query    *store.LibraryQuery
}

func (s *scopedStore) GetRun(_ context.Context, id string) (store.Run, error) {
	return store.Run{ID: id, DimensionScope: s.runScope}, nil
}

func (s *scopedStore) GetLibraryEntry(_ context.Context, id string) (store.LibraryEntry, error) {
	return store.LibraryEntry{ID: id, RunID: runUUID}, nil
}

func (s *scopedStore) ListCells(context.Context, string) ([]domain.Cell, error) {
	return []domain.Cell{{Row: "REV", Column: "P", Value: decimal.RequireFromString("100"), FilteredAmount: s.filtered}}, nil
}

func (s *scopedStore) GetCell(context.Context, string, string, string) (domain.Cell, error) {
	return domain.Cell{Row: "REV", Column: "P", Value: decimal.Zero, FilteredAmount: s.filtered}, nil
}

func (s *scopedStore) ListLibrary(_ context.Context, q store.LibraryQuery, _ *domain.AuditCursor, _ int) (store.LibraryPage, error) {
	s.query = &q
	return store.LibraryPage{Items: []store.LibraryEntry{}}, nil
}

func scopedRouter(s *scopedStore, d datascope.Decider) chi.Router {
	r := chi.NewRouter()
	New(s).WithDataScope(d).Register(r)
	return r
}

var costCentre100 = datascope.Scope{"COST_CENTER": {"100"}}

func TestCreateRun_TakesTheScopeFromTheDecision(t *testing.T) {
	s := &scopedStore{}
	w := call(scopedRouter(s, fakeDecider{scope: costCentre100}), http.MethodPost, "/v1/reporting/runs", runBody(""), map[string]string{"Idempotency-Key": "k1"}, DutyRun)
	if w.Code != http.StatusCreated || s.runSpec == nil || s.runSpec.DimensionScope.Key() != "COST_CENTER=100" {
		t.Fatalf("status %d spec %+v: %s", w.Code, s.runSpec, w.Body.String())
	}
}

func TestCreateRun_RefusesWithoutADecision(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
		code string
	}{
		{"policy engine unreachable", datascope.ErrNoDecision, http.StatusServiceUnavailable, CodeScopeUnavailable},
		{"unexpected failure", errors.New("boom"), http.StatusServiceUnavailable, CodeScopeUnavailable},
		{"no permitted values", datascope.ErrNoPermittedValues, http.StatusForbidden, CodeNoPermittedValues},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &scopedStore{}
			w := call(scopedRouter(s, fakeDecider{err: tc.err}), http.MethodPost, "/v1/reporting/runs", runBody(""), map[string]string{"Idempotency-Key": "k1"}, DutyRun)
			if w.Code != tc.want || problemCode(t, w) != tc.code || s.runSpec != nil {
				t.Fatalf("status %d code %s spec %+v: %s", w.Code, problemCode(t, w), s.runSpec, w.Body.String())
			}
		})
	}
}

func TestRunRoutes_HideRunsOfAnotherScope(t *testing.T) {
	base := "/v1/reporting/runs/" + runUUID
	paths := []string{base, base + "/definition", base + "/tree", base + "/cells", base + "/cells/REV/P/balances", base + "/cells/REV/P/lines", base + "/outputs/csv"}
	for _, runScope := range []string{"", "COST_CENTER=200", "COST_CENTER=100,200"} {
		for _, path := range paths {
			s := &scopedStore{runScope: runScope}
			w := call(scopedRouter(s, fakeDecider{scope: costCentre100}), http.MethodGet, path, "", nil, DutyView, DutyLines)
			if w.Code != http.StatusNotFound || problemCode(t, w) != store.CodeRunNotFound {
				t.Fatalf("run scope %q %s: status %d %s", runScope, path, w.Code, w.Body.String())
			}
		}
	}
}

func TestRunRoutes_ServeTheViewersOwnRunAndEveryRunToUnrestrictedViewers(t *testing.T) {
	path := "/v1/reporting/runs/" + runUUID + "/cells"
	for name, tc := range map[string]struct {
		viewer datascope.Scope
		run    string
	}{"restricted viewer, own run": {costCentre100, "COST_CENTER=100"}, "unrestricted viewer, restricted run": {nil, "COST_CENTER=100"}} {
		s := &scopedStore{runScope: tc.run}
		if w := call(scopedRouter(s, fakeDecider{scope: tc.viewer}), http.MethodGet, path, "", nil, DutyView); w.Code != http.StatusOK {
			t.Fatalf("%s: status %d %s", name, w.Code, w.Body.String())
		}
	}
}

func TestRunRoutes_RefuseWhenTheDecisionFails(t *testing.T) {
	s := &scopedStore{}
	w := call(scopedRouter(s, fakeDecider{err: datascope.ErrNoDecision}), http.MethodGet, "/v1/reporting/runs/"+runUUID+"/cells", "", nil, DutyView)
	if w.Code != http.StatusServiceUnavailable || problemCode(t, w) != CodeScopeUnavailable {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestDrill_ReportsTheFilteredAmount(t *testing.T) {
	s := &scopedStore{runScope: "COST_CENTER=100", filtered: decimal.RequireFromString("40.5")}
	router := scopedRouter(s, fakeDecider{scope: costCentre100})
	var cells struct {
		Items []struct {
			FilteredAmount string `json:"filteredAmount"`
		} `json:"items"`
	}
	w := call(router, http.MethodGet, "/v1/reporting/runs/"+runUUID+"/cells", "", nil, DutyView)
	if err := json.Unmarshal(w.Body.Bytes(), &cells); err != nil || cells.Items[0].FilteredAmount != "40.500000" {
		t.Fatalf("cells %s", w.Body.String())
	}
	var drill struct {
		FilteredAmount string            `json:"filteredAmount"`
		Warning        map[string]string `json:"warning"`
	}
	w = call(router, http.MethodGet, "/v1/reporting/runs/"+runUUID+"/cells/REV/P/balances", "", nil, DutyView)
	if err := json.Unmarshal(w.Body.Bytes(), &drill); err != nil || drill.FilteredAmount != "40.500000" || drill.Warning["code"] != CodeFilteredAmount {
		t.Fatalf("balances %s", w.Body.String())
	}
	s.filtered = decimal.Zero
	w = call(router, http.MethodGet, "/v1/reporting/runs/"+runUUID+"/cells/REV/P/balances", "", nil, DutyView)
	drill.Warning = nil
	if err := json.Unmarshal(w.Body.Bytes(), &drill); err != nil || drill.Warning != nil {
		t.Fatalf("no filtered amount must carry no warning: %s", w.Body.String())
	}
}

func TestListRuns_RestrictedViewerListsOnlyItsScope(t *testing.T) {
	f := &fakeStore{}
	r := chi.NewRouter()
	New(f).WithDataScope(fakeDecider{scope: costCentre100}).Register(r)
	if w := call(r, http.MethodGet, "/v1/reporting/runs", "", nil, DutyView); w.Code != http.StatusOK || f.listQuery.DimensionScope == nil || *f.listQuery.DimensionScope != "COST_CENTER=100" {
		t.Fatalf("status %d query %+v", w.Code, f.listQuery)
	}
	f = &fakeStore{}
	r = chi.NewRouter()
	New(f).WithDataScope(fakeDecider{}).Register(r)
	if w := call(r, http.MethodGet, "/v1/reporting/runs", "", nil, DutyView); w.Code != http.StatusOK || f.listQuery.DimensionScope != nil {
		t.Fatalf("unrestricted viewer must not filter: status %d query %+v", w.Code, f.listQuery)
	}
}

func TestLibraryRoutes_HideFilesOfRunsOutsideTheScope(t *testing.T) {
	base := "/v1/reporting/library/" + libraryUUID
	for _, path := range []string{base, base + "/content", base + "/share-links"} {
		s := &scopedStore{}
		w := call(scopedRouter(s, fakeDecider{scope: costCentre100}), http.MethodGet, path, "", nil, DutyView, DutyShare)
		if w.Code != http.StatusNotFound || problemCode(t, w) != store.CodeLibraryNotFound {
			t.Fatalf("%s: status %d %s", path, w.Code, w.Body.String())
		}
	}
	s := &scopedStore{}
	w := call(scopedRouter(s, fakeDecider{scope: costCentre100}), http.MethodPost, base+"/share-links", "", map[string]string{"Idempotency-Key": "k"}, DutyShare)
	if w.Code != http.StatusNotFound {
		t.Fatalf("creating a share link to a foreign file: status %d", w.Code)
	}
	s = &scopedStore{}
	if w := call(scopedRouter(s, fakeDecider{scope: costCentre100}), http.MethodGet, "/v1/reporting/library", "", nil, DutyView); w.Code != http.StatusOK || s.query.DimensionScope == nil || *s.query.DimensionScope != "COST_CENTER=100" {
		t.Fatalf("library list: status %d query %+v", w.Code, s.query)
	}
}

func TestDimensionRoutes_RefuseWithoutAPrincipal(t *testing.T) {
	r := scopedRouter(&scopedStore{}, fakeDecider{})
	w := call(r, http.MethodPost, "/v1/reporting/runs", runBody(""), map[string]string{"Idempotency-Key": "k"})
	if w.Code != http.StatusForbidden || problemCode(t, w) != "AUTH-0003" {
		t.Fatalf("no duty: status %d", w.Code)
	}
}
