package events

import (
	"os"
	"strings"
	"testing"
)

// Every event the service publishes is named in events/asyncapi.yaml, and the spec names no other.
func TestAsyncAPINamesEveryEventType(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	published := []string{PaymentReceived, PaymentApplied, PaymentUnapplied, PaymentApplicationReversed, DisputeOpened, DisputeResolved, DunningLetterIssued,
		CaseOpened, CaseClosed, ConfirmationReplied, DownPaymentReceived, DownPaymentCleared, DownPaymentReleased,
		CreditSettled, CreditSettlementReversed, DeductionOpened, DeductionResolved, CustomerBalanceChanged, OpenItemsImported, CreditNotePosted}
	known := map[string]bool{}
	for _, eventType := range published {
		known[eventType] = true
		if !strings.Contains(string(spec), "name: "+eventType+"\n") {
			t.Errorf("asyncapi.yaml does not name %s", eventType)
		}
	}
	for _, line := range strings.Split(string(spec), "\n") {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "name: dev.depot."); ok && !known["dev.depot."+name] {
			t.Errorf("asyncapi.yaml names dev.depot.%s, which the service does not publish", name)
		}
	}
}
