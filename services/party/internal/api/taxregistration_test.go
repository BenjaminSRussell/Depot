package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/services/party/internal/api"
	"github.com/BenjaminSRussell/depot/services/party/internal/store"
)

type fakeTaxRegs struct {
	set   []store.TaxRegistration
	asked [][]string
}

func (f *fakeTaxRegs) SetTaxRegistration(_ context.Context, id, country, vat string) (store.TaxRegistration, error) {
	r := store.TaxRegistration{PartnerID: id, Kind: "CUSTOMER", Country: country, VATID: vat}
	f.set = append(f.set, r)
	return r, nil
}

func (f *fakeTaxRegs) TaxRegistrations(_ context.Context, ids []string) ([]store.TaxRegistration, error) {
	f.asked = append(f.asked, ids)
	return []store.TaxRegistration{{PartnerID: ids[0], Kind: "CUSTOMER", Country: "DE"}}, nil
}

func TestTaxRegistrationRoutes_DutiesAndInput(t *testing.T) {
	f := &fakeTaxRegs{}
	r := httpx.NewRouter()
	api.New(&stubChecker{}, stubPinger{}, &fakeParties{}, &fakeSequencer{}).WithTaxRegistrations(f).Register(r)
	put := "/v1/party/partners/" + knownID + "/tax-registration"
	if rec := do(r, http.MethodPut, put, `{"country":"de","vatId":"de 123"}`, "party.read", true); rec.Code != http.StatusForbidden {
		t.Errorf("a reader may not set one: %d", rec.Code)
	}
	if rec := do(r, http.MethodPut, put, `{"country":"de","vatId":"de 123"}`, "party.write", true); rec.Code != http.StatusOK || len(f.set) != 1 || f.set[0].Country != "DE" || f.set[0].VATID != "DE123" {
		t.Errorf("set: %d %s %+v", rec.Code, rec.Body, f.set)
	}
	list := "/v1/party/partners:tax-registrations?ids=" + knownID
	if rec := do(r, http.MethodGet, list, "", "party.tax-registration.read", true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"country":"DE"`) {
		t.Errorf("service read: %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, list, "", "party.vendor.lookup", true); rec.Code != http.StatusForbidden {
		t.Errorf("another duty: %d", rec.Code)
	}
	for name, path := range map[string]string{"no ids": "/v1/party/partners:tax-registrations", "a bad id": "/v1/party/partners:tax-registrations?ids=x"} {
		if rec := do(r, http.MethodGet, path, "", "party.read", true); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
}
