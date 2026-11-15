package main

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func TestChargeFeedIsSeededAndNeedsItsAccounts(t *testing.T) {
	if !chargeFeedSeeded(tenant.ServicePrincipalTable()) {
		t.Fatal("service:intercompany-charges is not seeded with its flag duty")
	}
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if _, err := chargeFeedConfig(get); err == nil {
		t.Fatal("no accounts accepted")
	}
	env["IC_CHARGE_COST_ACCOUNT"], env["IC_CHARGE_RECOVERY_ACCOUNT"], env["IC_CHARGE_MARGIN_ACCOUNT"] = "6200", "4900", "4910"
	cfg, err := chargeFeedConfig(get)
	if err != nil || cfg.CostAccount != "6200" || cfg.MarginAccount != "4910" {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
}
