package events_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/services/quality/internal/events"
)

func TestNonconformanceCreated_PayloadMatchesAsyncAPI(t *testing.T) {
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	start := strings.Index(spec, "    NonconformanceCreatedData:\n")
	end := strings.Index(spec[start+1:], "\n    CAPAClosedData:")
	if start < 0 || end < 0 {
		t.Fatal("NonconformanceCreatedData schema missing from asyncapi.yaml")
	}
	section := spec[start : start+1+end]
	encoded, err := json.Marshal(events.NonconformanceCreatedData{
		NCRID: "n", Source: "s", Severity: "v", ProblemType: "p", ItemID: "i", VendorID: "v", CustomerID: "c",
		Qty: "1", UOM: "u", QualityOrderID: "q", LegalEntityID: "l",
	})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["legalEntityId"]; !ok {
		t.Fatal("legalEntityId is not part of the payload")
	}
	for name := range fields {
		if !strings.Contains(section, "        "+name+": ") {
			t.Errorf("property %s missing from the asyncapi payload", name)
		}
	}
}

func TestNonconformanceCreated_OmitsLegalEntityWhenTenantWide(t *testing.T) {
	encoded, err := json.Marshal(events.NonconformanceCreatedData{NCRID: "n"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "legalEntityId") {
		t.Fatalf("legalEntityId present for a tenant-wide emitter: %s", encoded)
	}
}
