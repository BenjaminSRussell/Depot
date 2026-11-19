package domain

const (
	DutyCUIPolicyRead  = "cui.policy.read"
	DutyNeedToKnowRead = "compliance.need-to-know.read"
	DutyCUIPolicyAdmin = "cui.policy.admin"

	SourceDeploymentDefault = "DEPLOYMENT_DEFAULT"
)

type NeedToKnowState struct {
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"`
}

func ResolveNeedToKnow(boundary Boundary, tenant, override *bool) NeedToKnowState {
	switch {
	case override != nil:
		return NeedToKnowState{Enabled: *override, Source: SourceLegalEntity}
	case tenant != nil:
		return NeedToKnowState{Enabled: *tenant, Source: SourceTenant}
	}
	return NeedToKnowState{Enabled: boundary == BoundaryCUI, Source: SourceDeploymentDefault}
}

func MayChangeNeedToKnow(tenantWide, enabled bool) bool { return tenantWide || enabled }
