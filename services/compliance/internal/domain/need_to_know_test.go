package domain

import "testing"

func TestResolveNeedToKnow_OverrideBeatsTenantBeatsDeploymentDefault(t *testing.T) {
	yes, no := true, false
	for name, tc := range map[string]struct {
		boundary Boundary
		tenant   *bool
		override *bool
		want     NeedToKnowState
	}{
		"cui, never set":                         {BoundaryCUI, nil, nil, NeedToKnowState{true, SourceDeploymentDefault}},
		"standard, never set":                    {BoundaryStandard, nil, nil, NeedToKnowState{false, SourceDeploymentDefault}},
		"standard, tenant ON":                    {BoundaryStandard, &yes, nil, NeedToKnowState{true, SourceTenant}},
		"cui, tenant OFF":                        {BoundaryCUI, &no, nil, NeedToKnowState{false, SourceTenant}},
		"tenant ON, override OFF":                {BoundaryCUI, &yes, &no, NeedToKnowState{false, SourceLegalEntity}},
		"tenant OFF, override ON":                {BoundaryStandard, &no, &yes, NeedToKnowState{true, SourceLegalEntity}},
		"override ON, tenant unset, standard":    {BoundaryStandard, nil, &yes, NeedToKnowState{true, SourceLegalEntity}},
		"override OFF, tenant unset, cui":        {BoundaryCUI, nil, &no, NeedToKnowState{false, SourceLegalEntity}},
		"record without legal entity, tenant ON": {BoundaryStandard, &yes, nil, NeedToKnowState{true, SourceTenant}},
	} {
		if got := ResolveNeedToKnow(tc.boundary, tc.tenant, tc.override); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", name, got, tc.want)
		}
	}
}

func TestMayChangeNeedToKnow_OnlyTenantWideMayTurnItOff(t *testing.T) {
	for _, tc := range []struct{ tenantWide, enabled, want bool }{
		{true, true, true}, {true, false, true}, {false, true, true}, {false, false, false},
	} {
		if got := MayChangeNeedToKnow(tc.tenantWide, tc.enabled); got != tc.want {
			t.Errorf("tenantWide=%v enabled=%v: got %v", tc.tenantWide, tc.enabled, got)
		}
	}
}
