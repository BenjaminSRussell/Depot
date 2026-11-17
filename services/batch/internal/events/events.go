package events

const (
	InstanceSubmitted = "dev.depot.workflow.instance.submitted.v1"
	WorkItemAssigned  = "dev.depot.workflow.workitem.assigned.v1"
	WorkItemEscalated = "dev.depot.workflow.workitem.escalated.v1"
	InstanceCompleted = "dev.depot.workflow.instance.completed.v1"
	BatchRunStarted   = "dev.depot.batch.run.started.v1"
	BatchRunCompleted = "dev.depot.batch.run.completed.v1"
	BatchRunFailed    = "dev.depot.batch.run.failed.v1"
	MessageSent       = "dev.depot.notify.message.sent.v1"
	MessageFailed     = "dev.depot.notify.message.failed.v1"
	MessageBounced    = "dev.depot.notify.message.bounced.v1"
	TemplatePublished = "dev.depot.notify.template.published.v1"
	EntityPublished   = "dev.depot.metadata.entity.published.v1"
)
