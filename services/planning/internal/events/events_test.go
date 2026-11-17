package events

import (
	"os"
	"regexp"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/events"
)

func TestEventTypesFollowTheNamingRule(t *testing.T) {
	for _, typ := range All() {
		if !events.ValidType(typ) {
			t.Errorf("%s is not dev.depot.<context>.<entity>.<verb>.v<n>", typ)
		}
	}
	if PlanCompleted != "dev.depot.planning.plan.completed.v1" {
		t.Errorf("PlanCompleted = %s", PlanCompleted)
	}
	if PlannedOrderFirmed != "dev.depot.planning.plannedorder.firmed.v1" {
		t.Errorf("PlannedOrderFirmed = %s", PlannedOrderFirmed)
	}
	if PlanStarted != "dev.depot.planning.plan.started.v1" {
		t.Errorf("PlanStarted = %s", PlanStarted)
	}
	if MessageRaised != "dev.depot.planning.message.raised.v1" {
		t.Errorf("MessageRaised = %s", MessageRaised)
	}
	if MessageApplied != "dev.depot.planning.message.applied.v1" || MessageDismissed != "dev.depot.planning.message.dismissed.v1" {
		t.Errorf("MessageApplied = %s, MessageDismissed = %s", MessageApplied, MessageDismissed)
	}
	if ForecastPublished != "dev.depot.planning.forecast.published.v1" {
		t.Errorf("ForecastPublished = %s", ForecastPublished)
	}
}

// TestAsyncAPIDeclaresEveryEvent keeps events/asyncapi.yaml and All() in step.
func TestAsyncAPIDeclaresEveryEvent(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`name: (dev\.depot\.\S+)`).FindAllStringSubmatch(string(spec), -1) {
		declared[m[1]] = true
	}
	for _, typ := range All() {
		if !declared[typ] {
			t.Errorf("%s is published but not declared in asyncapi.yaml", typ)
		}
		delete(declared, typ)
	}
	for typ := range declared {
		t.Errorf("asyncapi.yaml declares %s which All() does not list", typ)
	}
}
