package domain_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
)

func TestEveryMappedReadDutyIsSeeded(t *testing.T) {
	raw, err := os.ReadFile("../../../../policies/authz/data.json")
	if err != nil {
		t.Fatal(err)
	}
	var seed struct {
		Duties map[string]json.RawMessage `json:"duties"`
	}
	if err := json.Unmarshal(raw, &seed); err != nil {
		t.Fatal(err)
	}
	for _, entity := range domain.KnownEntities() {
		duty, _ := domain.ReadDuty(entity)
		if _, ok := seed.Duties[duty]; !ok {
			t.Errorf("entity %s maps to duty %q, which data.json does not seed", entity, duty)
		}
	}
	for _, duty := range []string{"audit.export", "audit.auditor"} {
		if _, ok := seed.Duties[duty]; !ok {
			t.Errorf("duty %s is not seeded", duty)
		}
	}
}

func TestMask(t *testing.T) {
	for in, want := range map[string]string{"": "", "abc": "****", "1234567": "****", "12345678": "****5678", "DE0000000001234": "****1234"} {
		if got := domain.Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSensitiveFieldsAndUnmappedEntities(t *testing.T) {
	if !domain.IsSensitive("vendor_bank_account", "account_number") || !domain.IsSensitive("party.customer", "credit_limit") {
		t.Fatal("bank account number and credit limit must be sensitive")
	}
	if domain.IsSensitive("party.vendor", "name") {
		t.Fatal("a name is not sensitive")
	}
	if _, ok := domain.ReadDuty("mystery"); ok {
		t.Fatal("an unmapped entity has no read duty")
	}
}
