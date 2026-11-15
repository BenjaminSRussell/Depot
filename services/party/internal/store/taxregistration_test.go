package store_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

// A customer role gets a tax registration: the country its VAT registration is in, read back by the services that report goods
// movements. A role of another legal entity, an unknown id and a bad country are refused; another legal entity reads nothing.
func TestTaxRegistration_SetReadAndScope(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	seed := seedCustomerRefs(t, app)
	partyID, err := insertParty(principalContext(1, ""), app, testkit.TenantID(1), "P-TR")
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := s.CreateCustomer(asUser(1, legalEntityA, "loader"), partyID, newCustomer(seed, "C-TR1"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := asUser(1, legalEntityA, "clerk")
	got, err := s.SetTaxRegistration(ctx, c.ID, "DE", "DE123456789")
	if err != nil || got.Kind != "CUSTOMER" || got.Country != "DE" || got.VATID != "DE123456789" {
		t.Fatalf("set = %+v, %v", got, err)
	}
	if got, err = s.SetTaxRegistration(ctx, c.ID, "FR", ""); err != nil || got.Country != "FR" {
		t.Fatalf("update = %+v, %v", got, err)
	}
	read, err := s.TaxRegistrations(ctx, []string{c.ID, "00000000-0000-4000-8000-0000000000ff"})
	if err != nil || len(read) != 1 || read[0].PartnerID != c.ID || read[0].Country != "FR" || read[0].VATID != "" {
		t.Fatalf("read = %+v, %v", read, err)
	}
	for name, bad := range map[string]string{"lower case": "de", "three letters": "DEU", "digits": "12"} {
		if _, err := s.SetTaxRegistration(ctx, c.ID, bad, ""); problemCode(t, err) != store.CodeTaxRegistrationInvalid {
			t.Errorf("%s country: %v", name, err)
		}
	}
	if _, err := s.SetTaxRegistration(ctx, c.ID, "DE", "has space"); problemCode(t, err) != store.CodeTaxRegistrationInvalid {
		t.Errorf("a VAT id with a space: %v", err)
	}
	if _, err := s.SetTaxRegistration(ctx, "00000000-0000-4000-8000-0000000000fe", "DE", ""); err == nil {
		t.Error("an unknown role must be refused")
	}
	if _, err := s.SetTaxRegistration(asUser(1, legalEntityB, "x"), c.ID, "DE", ""); err == nil {
		t.Error("a role of another legal entity must be refused")
	}
	for name, other := range map[string]int{"other legal entity": 1, "other tenant": 2} {
		le := legalEntityB
		if other == 2 {
			le = legalEntityA
		}
		if got, err := s.TaxRegistrations(asUser(other, le, "x"), []string{c.ID}); err != nil || len(got) != 0 {
			t.Errorf("%s reads %+v, %v", name, got, err)
		}
	}
	if _, err := s.TaxRegistrations(ctx, []string{"nope"}); err == nil {
		t.Error("a bad id must be refused")
	}
}
