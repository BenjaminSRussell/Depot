package events

import (
	"testing"

	"github.com/BenjaminSRussell/depot/platform/events"
)

// TestTypesFollowThePlatformRule guards against an event type the kernel's
// events.New would refuse at publish time (it panics on an invalid name).
func TestTypesFollowThePlatformRule(t *testing.T) {
	for _, typ := range []string{ProjectCreated, ProjectUpdated, WBSScheduled, ChangeOrderApplied, TransactionPosted} {
		if !events.ValidType(typ) {
			t.Errorf("%s is not a valid dev.depot.<context>.<entity>.<verb>.v<n> type", typ)
		}
	}
}
