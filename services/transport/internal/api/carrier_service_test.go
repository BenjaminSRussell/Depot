package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/transport/internal/api"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
)

func TestCarrierServices_CreateNormalisesTheCodeAndValidates(t *testing.T) {
	st := &fakeStore{}
	h := router(st)
	body := `{"carrierId":"` + carrierID + `","code":" ground ","name":"Ground","mode":"PARCEL"}`
	rec := doWith(t, h, http.MethodPost, base+"/carrier-services", body, map[string]string{"Idempotency-Key": "k1"}, api.DutyRateApprove)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"code":"GROUND"`) {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if len(st.idems) != 1 || st.idems[0].Key != "k1" {
		t.Fatalf("idempotency = %+v", st.idems)
	}
	bad := do(t, h, http.MethodPost, base+"/carrier-services", `{"carrierId":"x","code":"","name":"","mode":"BOAT"}`, api.DutyRateApprove)
	p := problem(t, bad)
	if bad.Code != http.StatusBadRequest || p.Errors["carrierId"] == "" || p.Errors["code"] == "" || p.Errors["name"] == "" || p.Errors["mode"] == "" {
		t.Fatalf("status = %d, errors %+v", bad.Code, p.Errors)
	}
}

func TestCarrierServices_ListPassesSearchAndAcceptsAnyReadDuty(t *testing.T) {
	st := &fakeStore{services: []domain.CarrierService{{ID: serviceID, CarrierID: carrierID, Code: "GROUND", Name: "Ground", Mode: "PARCEL", Active: true}}}
	h := router(st)
	for _, duty := range []string{api.DutyRate, api.DutyRateApprove, api.DutyView} {
		if rec := do(t, h, http.MethodGet, base+"/carrier-services?q=gro&code=ground&active=true&limit=5&carrierId="+carrierID, ``, duty); rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body %s", duty, rec.Code, rec.Body.String())
		}
	}
	if f := st.serviceFilter; f.Q != "gro" || f.Code != "GROUND" || !f.ActiveOnly || f.Limit != 5 || f.CarrierID != carrierID {
		t.Fatalf("filter = %+v", f)
	}
	if rec := do(t, h, http.MethodGet, base+"/carrier-services", ``, "TMS_OTHER"); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, base+"/carrier-services?limit=0", ``, api.DutyView); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for limit=0", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, base+"/carrier-services/"+serviceID, ``, api.DutyView); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestListFreightBills_PassesNumberSearch(t *testing.T) {
	st := &fakeStore{}
	h := router(st)
	if rec := do(t, h, http.MethodGet, base+"/freight-bills?q=FB-0&number=FB-000001&loadId="+loadID+"&limit=10", ``, api.DutyView); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if f := st.billFilter; f.Q != "FB-0" || f.Number != "FB-000001" || f.LoadID != loadID || f.Limit != 10 {
		t.Fatalf("filter = %+v", f)
	}
}
