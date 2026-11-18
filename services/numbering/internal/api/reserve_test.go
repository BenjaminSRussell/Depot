package api_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/numbering/internal/store"
)

func TestReservations(t *testing.T) {
	id := testkit.TenantID(5)
	reservePath := "/v1/numbering/sequences/INV/reserve"
	confirmPath := "/v1/numbering/reservations/" + id + "/confirm"
	voidPath := "/v1/numbering/reservations/" + id + "/void"
	voidedPath := "/v1/numbering/sequences/INV/voided"
	held := &fakeStore{res: store.Reservation{ID: id, Sequence: "INV", Value: 1, Formatted: "INV-000001", Status: "reserved"}}
	conflict := &fakeStore{err: httpx.NewProblem(http.StatusConflict, "NUM-0006", "already confirmed")}
	tests := []struct {
		name, method, path, body, duties string
		store                            *fakeStore
		withTenant                       bool
		wantStatus                       int
		wantBody                         string
	}{
		{"reserve ok", http.MethodPost, reservePath, "", allDuties, held, true, http.StatusCreated, `"formatted":"INV-000001"`},
		{"reserve with lease", http.MethodPost, reservePath, `{"leaseSeconds":60}`, allDuties, held, true, http.StatusCreated, `"status":"reserved"`},
		{"reserve null lease takes the default", http.MethodPost, reservePath, `{"leaseSeconds":null}`, allDuties, held, true, http.StatusCreated, `"id"`},
		{"reserve with the narrow service duty", http.MethodPost, reservePath, "", "numbering.sequence.allocate", held, true, http.StatusCreated, `"id"`},
		{"reserve without tenant", http.MethodPost, reservePath, "", "", held, false, http.StatusUnauthorized, "AUTH-0001"},
		{"reserve without duty", http.MethodPost, reservePath, "", "numbering.read", held, true, http.StatusForbidden, "AUTH-0003"},
		{"reserve lease zero", http.MethodPost, reservePath, `{"leaseSeconds":0}`, allDuties, held, true, http.StatusBadRequest, `"leaseSeconds"`},
		{"reserve lease negative", http.MethodPost, reservePath, `{"leaseSeconds":-1}`, allDuties, held, true, http.StatusBadRequest, `"leaseSeconds"`},
		{"reserve lease too long", http.MethodPost, reservePath, `{"leaseSeconds":3601}`, allDuties, held, true, http.StatusBadRequest, `"leaseSeconds"`},
		{"reserve lease string", http.MethodPost, reservePath, `{"leaseSeconds":"60"}`, allDuties, held, true, http.StatusBadRequest, "COM-0001"},
		{"reserve lease object", http.MethodPost, reservePath, `{"leaseSeconds":{}}`, allDuties, held, true, http.StatusBadRequest, "COM-0001"},
		{"reserve lease fraction", http.MethodPost, reservePath, `{"leaseSeconds":1.5}`, allDuties, held, true, http.StatusBadRequest, "COM-0001"},
		{"reserve lease overflow", http.MethodPost, reservePath, `{"leaseSeconds":1e999}`, allDuties, held, true, http.StatusBadRequest, "COM-0001"},
		{"reserve store error", http.MethodPost, reservePath, "", allDuties, &fakeStore{err: errors.New("pg secret")}, true, http.StatusInternalServerError, "COM-0500"},
		{"confirm ok", http.MethodPost, confirmPath, `{"documentRef":"INV-2026-1"}`, allDuties, held, true, http.StatusOK, `"id"`},
		{"confirm id not a uuid", http.MethodPost, "/v1/numbering/reservations/nope/confirm", `{"documentRef":"x"}`, allDuties, held, true, http.StatusBadRequest, `"id"`},
		{"confirm without document ref", http.MethodPost, confirmPath, `{}`, allDuties, held, true, http.StatusBadRequest, `"documentRef"`},
		{"confirm document ref number", http.MethodPost, confirmPath, `{"documentRef":5}`, allDuties, held, true, http.StatusBadRequest, "COM-0001"},
		{"confirm document ref too long", http.MethodPost, confirmPath, `{"documentRef":"` + strings.Repeat("x", 201) + `"}`, allDuties, held, true, http.StatusBadRequest, `"documentRef"`},
		{"confirm state conflict", http.MethodPost, confirmPath, `{"documentRef":"x"}`, allDuties, conflict, true, http.StatusConflict, "NUM-0006"},
		{"confirm without duty", http.MethodPost, confirmPath, `{"documentRef":"x"}`, "numbering.read", held, true, http.StatusForbidden, "AUTH-0003"},
		{"void ok", http.MethodPost, voidPath, `{"reason":"posting rolled back"}`, allDuties, held, true, http.StatusOK, `"id"`},
		{"void empty reason", http.MethodPost, voidPath, `{"reason":""}`, allDuties, held, true, http.StatusBadRequest, `"reason"`},
		{"void blank reason", http.MethodPost, voidPath, `{"reason":"   "}`, allDuties, held, true, http.StatusBadRequest, `"reason"`},
		{"void reason too long", http.MethodPost, voidPath, `{"reason":"` + strings.Repeat("x", 201) + `"}`, allDuties, held, true, http.StatusBadRequest, `"reason"`},
		{"void reason number", http.MethodPost, voidPath, `{"reason":7}`, allDuties, held, true, http.StatusBadRequest, "COM-0001"},
		{"void state conflict", http.MethodPost, voidPath, `{"reason":"x"}`, allDuties, conflict, true, http.StatusConflict, "NUM-0006"},
		{"voided log", http.MethodGet, voidedPath, "", allDuties, &fakeStore{voided: []store.VoidedNumber{{ID: id, Value: 2, Formatted: "INV-000002", Reason: "lease_expired"}}}, true, http.StatusOK, `"reason":"lease_expired"`},
		{"voided log without read duty", http.MethodGet, voidedPath, "", "numbering.write", held, true, http.StatusForbidden, "AUTH-0003"},
		{"voided log bad cursor", http.MethodGet, voidedPath + "?after=x", "", allDuties, held, true, http.StatusBadRequest, `"after"`},
		{"voided log store error", http.MethodGet, voidedPath, "", allDuties, &fakeStore{err: errors.New("pg secret")}, true, http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, tc.path, tc.body, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if strings.Contains(rec.Body.String(), "pg secret") {
				t.Fatalf("store error leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestReservationMetrics(t *testing.T) {
	id := testkit.TenantID(5)
	r, a := newRouter(&fakeStore{res: store.Reservation{ID: id}})
	do(r, http.MethodPost, "/v1/numbering/sequences/INV/reserve", "", allDuties, true)
	do(r, http.MethodPost, "/v1/numbering/reservations/"+id+"/confirm", `{"documentRef":"x"}`, allDuties, true)
	do(r, http.MethodPost, "/v1/numbering/reservations/"+id+"/void", `{"reason":"x"}`, allDuties, true)
	failing, af := newRouter(&fakeStore{err: errors.New("down")})
	do(failing, http.MethodPost, "/v1/numbering/sequences/INV/reserve", "", allDuties, true)
	for api, want := range map[string][]string{
		"ok":   {"numbering_reserved_total 1", "numbering_confirmed_total 1", "numbering_voided_total 1"},
		"fail": {"numbering_failed_total 1", "numbering_reserved_total 0"},
	} {
		target := a
		if api == "fail" {
			target = af
		}
		w := httptest.NewRecorder()
		target.Metrics().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		for _, line := range want {
			if !strings.Contains(w.Body.String(), line) {
				t.Errorf("%s metrics missing %q: %s", api, line, w.Body.String())
			}
		}
	}
}
