package store

import "testing"

func TestRealmOfUsesTheRecordedRealmAndOnlyFallsBackWithoutOne(t *testing.T) {
	if got := realmOf(Provision{RealmName: "acme-prod"}, "t1"); got != "acme-prod" {
		t.Fatalf("the event must carry the recorded realm, got %q", got)
	}
	if got := realmOf(Provision{}, "t1"); got != "depot-t1" {
		t.Fatalf("fallback for tenants provisioned before the realm was recorded: %q", got)
	}
}
