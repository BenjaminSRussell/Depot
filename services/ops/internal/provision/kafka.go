package provision

import "context"

// SharedTopicKafka is the KafkaAdmin under the shared-topic model (docs/architecture.md C.2, Q5 and decision D6): entity topics are
// shared by every tenant and keyed by tenant id, so there is nothing to create or authorise per tenant at provisioning, and an ACL
// cannot be scoped to a tenant on a shared topic. External consumers read egress topics (passage.egress.<tenantSlug>.<context>)
// that the integration hub creates, with their KafkaUser, when the tenant subscribes (POST /v1/integration/event-egress/{context}:subscribe);
// that is the tenant's own action, not provisioning. Both calls report DONE with that reason and touch nothing.
type SharedTopicKafka struct{}

const sharedTopicReason = "not needed under the shared-topic model: entity topics are shared and keyed by tenant id; per-tenant egress topics and KafkaUsers are created when the tenant subscribes to event egress"

func (SharedTopicKafka) EnsureAccess(context.Context, Tenant, bool) (string, error) {
	return sharedTopicReason, nil
}

func (SharedTopicKafka) RemoveAccess(context.Context, Tenant, bool) (string, error) {
	return sharedTopicReason + " (egress topics of the tenant are removed by integration when its subscriptions end)", nil
}
