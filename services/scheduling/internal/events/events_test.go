package events

import (
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/events"
)

func TestEveryTypeFollowsTheNamingRule(t *testing.T) {
	want := map[string]bool{
		"dev.depot.scheduling.run.completed.v1":            true,
		"dev.depot.scheduling.job.scheduled.v1":            true,
		"dev.depot.scheduling.orderdates.changed.v1":       true,
		"dev.depot.scheduling.campaign.sequenced.v1":       true,
		"dev.depot.scheduling.campaign.started.v1":         true,
		"dev.depot.scheduling.campaign.completed.v1":       true,
		"dev.depot.scheduling.campaign.cancelled.v1":       true,
		"dev.depot.scheduling.campaign.placed.v1":          true,
		"dev.depot.scheduling.campaign.unplaced.v1":        true,
		"dev.depot.scheduling.cleanoutcounter.reset.v1":    true,
		"dev.depot.scheduling.capacityoverride.created.v1": true,
		"dev.depot.scheduling.capacityoverride.deleted.v1": true,
		"dev.depot.scheduling.resource.updated.v1":         true,
		"dev.depot.scheduling.capacityblock.updated.v1":    true,
		"dev.depot.scheduling.network.updated.v1":          true,
		"dev.depot.scheduling.orderimport.recorded.v1":     true,
		"dev.depot.scheduling.operation.updated.v1":        true,
		"dev.depot.scheduling.operation.deleted.v1":        true,
		"dev.depot.scheduling.order.unscheduled.v1":        true,
		"dev.depot.scheduling.parameters.updated.v1":       true,
		"dev.depot.scheduling.bottleneck.detected.v1":      true,
	}
	for _, typ := range All() {
		if !events.ValidType(typ) {
			t.Fatalf("%s breaks dev.depot.<context>.<entity>.<verb>.v<n>", typ)
		}
		if !want[typ] {
			t.Fatalf("%s is not documented in events/asyncapi.yaml", typ)
		}
		delete(want, typ)
	}
	if len(want) != 0 {
		t.Fatalf("documented types not published: %v", want)
	}
}

// Every published type has a message in events/asyncapi.yaml.
func TestAsyncAPIDeclaresEveryType(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range All() {
		if !strings.Contains(string(spec), "name: "+typ+"\n") {
			t.Errorf("events/asyncapi.yaml has no message named %s", typ)
		}
	}
}
