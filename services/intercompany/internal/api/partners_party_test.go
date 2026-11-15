package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/api"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/partyclient"
)

// fakeParty is a party master holding the customers and vendors it is given; down makes every lookup fail as an unreachable service.
type fakeParty struct {
	customers, vendors map[string]bool
	down               bool
	asked              []string
}

func (f *fakeParty) CustomerExists(_ context.Context, le, id string) error {
	f.asked = append(f.asked, "customer:"+id)
	return f.lookup(f.customers, id)
}

func (f *fakeParty) VendorExists(_ context.Context, le, id string) error {
	f.asked = append(f.asked, "vendor:"+id)
	return f.lookup(f.vendors, id)
}

func (f *fakeParty) lookup(m map[string]bool, id string) error {
	switch {
	case f.down:
		return partyclient.ErrUnavailable
	case !m[id]:
		return partyclient.ErrNotFound
	}
	return nil
}

func accountsBody(customer, vendor string) string {
	return `{"sellerEntity":"P","buyerEntity":"S","customerAccount":"` + customer + `","vendorAccount":"` + vendor + `","actions":{"autoPostInvoice":true}}`
}

// #40: a pairing whose accounts are not party ids is refused when it is saved, with a code that names the account, and nothing is
// stored, so no event can reach the dead letter queue for it later.
func TestCreatePartner_RefusesAccountsThePartyMasterDoesNotHold(t *testing.T) {
	customer, vendor := ids.New(), ids.New()
	party := &fakeParty{customers: map[string]bool{customer: true}, vendors: map[string]bool{vendor: true}}
	store := newFake()
	h := router(api.New(store).WithParty(party))
	post := func(body string) (int, partnerProblem) {
		rec := call{method: http.MethodPost, path: partnersPath, duties: allDuties, body: body, headers: map[string]string{"Idempotency-Key": ids.New()}}.on(h)
		if rec.Code == http.StatusCreated {
			return rec.Code, partnerProblem{}
		}
		return rec.Code, decode[partnerProblem](t, rec)
	}

	if code, p := post(accountsBody("IC-S", vendor)); code != http.StatusUnprocessableEntity || p.Code != domain.CodeNoCustomerAccount {
		t.Fatalf("a customer code that is not an id = %d %+v", code, p)
	}
	if code, p := post(accountsBody(ids.New(), vendor)); code != http.StatusUnprocessableEntity || p.Code != domain.CodeNoCustomerAccount {
		t.Fatalf("an unknown customer = %d %+v", code, p)
	}
	if code, p := post(accountsBody(customer, ids.New())); code != http.StatusUnprocessableEntity || p.Code != domain.CodeNoVendorAccount {
		t.Fatalf("an unknown vendor = %d %+v", code, p)
	}
	// The vendor account must be a vendor: a customer id there is not one.
	if code, p := post(accountsBody(customer, customer)); code != http.StatusUnprocessableEntity || p.Code != domain.CodeNoVendorAccount {
		t.Fatalf("a customer id as the vendor account = %d %+v", code, p)
	}
	if len(store.recs) != 0 {
		t.Fatalf("a refused pairing was stored: %d rows", len(store.recs))
	}
	if code, p := post(accountsBody(customer, vendor)); code != http.StatusCreated {
		t.Fatalf("good accounts = %d %+v", code, p)
	}

	party.down = true
	if code, p := post(accountsBody(customer, vendor)); code != http.StatusServiceUnavailable || p.Code != domain.CodePartyUnavailable {
		t.Fatalf("an unreachable party service = %d %+v", code, p)
	}
}

func TestUpdatePartner_ChecksOnlyTheAccountsItChanges(t *testing.T) {
	customer, vendor, other := ids.New(), ids.New(), ids.New()
	party := &fakeParty{customers: map[string]bool{customer: true, other: true}, vendors: map[string]bool{vendor: true}}
	h := router(api.New(newFake()).WithParty(party))
	created := create(t, h, accountsBody(customer, vendor))
	party.asked = nil

	patch := func(body string) *httptest.ResponseRecorder {
		return call{method: http.MethodPatch, path: partnersPath + "/" + created.ID, duties: allDuties, body: body, headers: map[string]string{"If-Match": `"1"`}}.on(h)
	}
	if rec := patch(`{"pricing":"CostPlus","markupPct":"10"}`); rec.Code != http.StatusOK {
		t.Fatalf("a change without accounts = %d %s", rec.Code, rec.Body)
	}
	if len(party.asked) != 0 {
		t.Fatalf("no account changed, yet the party service was asked %v", party.asked)
	}
	rec := patch(`{"vendorAccount":"` + ids.New() + `"}`)
	if rec.Code != http.StatusUnprocessableEntity || decode[partnerProblem](t, rec).Code != domain.CodeNoVendorAccount {
		t.Fatalf("an unknown vendor = %d %s", rec.Code, rec.Body)
	}
	if len(party.asked) != 1 || party.asked[0][:7] != "vendor:" {
		t.Fatalf("only the changed account is checked, asked %v", party.asked)
	}
	// "1" is stale after the first patch, so use the right version for the good change.
	rec = call{method: http.MethodPatch, path: partnersPath + "/" + created.ID, duties: allDuties, body: `{"customerAccount":"` + other + `"}`, headers: map[string]string{"If-Match": `"2"`}}.on(h)
	if rec.Code != http.StatusOK {
		t.Fatalf("a known customer = %d %s", rec.Code, rec.Body)
	}
}
