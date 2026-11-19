// Package rlsexempt is the list of tables that deliberately differ from the standard row-level-security template, with the reason for each.
// tools/rlscheck (CI) and services/ops provisioning (the databases step) both read it, so the two cannot disagree about what is allowed.
package rlsexempt

// The problem kinds of the allowlist keys ("<service>.<table>:<problem>").
const (
	NotForced     = "rls-not-enabled-and-forced"
	NoTenantID    = "no-tenant-id-column"
	NoTenantPol   = "no-tenant-policy"
	NoEntityPol   = "no-legal-entity-policy"
	ExtraPolicy   = "extra-permissive-policy"
	NullEntityPol = "null-admitting-legal-entity-policy"
	// NoEntityScope is a table of an EntityScopedServices service that neither carries legal_entity_id (and so the
	// restrictive legal-entity policy NoEntityPol checks) nor has a restrictive policy on depot.legal_entity_scopes
	// (a child table that follows its parent's legal entity).
	NoEntityScope = "no-legal-entity-scope"
	// DefinerPath: a SECURITY DEFINER function whose search_path is unset or does not end with pg_temp (tools/rlscheck);
	// the "table" of its key is the function name.
	DefinerPath = "definer-search-path-without-trailing-pg_temp"
)

// EntityScopedServices are the services architecture gap D17 named as having no legal-entity column or policy
// (B-03b). Every table of theirs must be legal-entity scoped or carry a NoEntityScope reason here, so the gap cannot
// silently reopen. audit is added by B-07, which owns its migrations.
var EntityScopedServices = map[string]bool{
	"shopfloor":          true,
	"scheduling":         true,
	"mobile":             true,
	"fx":                 true,
	"integration-hub-ts": true,
	"agents-ts":          true,
}

// KernelNoEntityScopeReason covers the platform kernel's own tables, which every service creates from the dbx
// templates: they hold delivery bookkeeping keyed by tenant (and request, message or token id), not business data
// of a legal entity, and are written and read only by the kernel's middleware, relay and consumer.
const KernelNoEntityScopeReason = "permanent: platform kernel bookkeeping keyed by tenant (outbox relay, consumer inbox, idempotency replay, service-token replay), not business data of a legal entity; written and read only by the kernel"

var kernelTables = map[string]bool{"outbox": true, "consumer_inbox": true, "idempotency_keys": true, "service_token_replay": true}

// NilTenant is the platform tenant whose rows (system reference data) every tenant may read.
const NilTenant = "00000000-0000-0000-0000-000000000000"

const (
	problemNotForced   = NotForced
	problemNoTenantID  = NoTenantID
	problemNoTenantPol = NoTenantPol
	problemNoEntityPol = NoEntityPol
	problemExtraPolicy = ExtraPolicy
	problemNullEntity  = NullEntityPol
)

// replayNotForcedReason is the documented gap on dbx.ServiceTokenReplayDDL:
// RLS is enabled but not forced so SECURITY DEFINER service_token_replay_purge()
// can delete every tenant's expired ids; depot_app is still bound by the
// tenant policy and may only insert. Any service that copies the template is
// covered — the table is allowlisted by name so a new copy cannot fail CI.
const ReplayNotForcedReason = "permanent: RLS enabled but not forced so the SECURITY DEFINER service_token_replay_purge() can delete every tenant's expired ids; depot_app is still bound by the tenant policy and may only insert"

// Reason says why a table may have the problem, and whether it may. service_token_replay is allowed by name in every service.
func Reason(service, table, problem string) (string, bool) {
	if table == "service_token_replay" && problem == problemNotForced {
		return ReplayNotForcedReason, true
	}
	if kernelTables[table] && problem == NoEntityScope {
		return KernelNoEntityScopeReason, true
	}
	reason, ok := Allowlist[service+"."+table+":"+problem]
	return reason, ok
}

var Allowlist = map[string]string{
	"ops.ops_region:" + problemNotForced:                    "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0001_init, #50)",
	"ops.ops_region:" + problemNoTenantID:                   "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0001_init, #50)",
	"ops.ops_failover:" + problemNotForced:                  "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0001_init, #50)",
	"ops.ops_failover:" + problemNoTenantID:                 "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0001_init, #50)",
	"ops.ops_failover_request:" + problemNotForced:          "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0003_failover_request, #50)",
	"ops.ops_failover_request:" + problemNoTenantID:         "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0003_failover_request, #50)",
	"ops.ops_failover_step:" + problemNotForced:             "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0004_steps_break_glass, #50)",
	"ops.ops_failover_step:" + problemNoTenantID:            "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0004_steps_break_glass, #50)",
	"ops.ops_break_glass:" + problemNotForced:               "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0004_steps_break_glass, #50)",
	"ops.ops_break_glass:" + problemNoTenantID:              "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0004_steps_break_glass, #50)",
	"ops.ops_break_glass_use:" + problemNotForced:           "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0004_steps_break_glass, #50)",
	"ops.ops_break_glass_use:" + problemNoTenantID:          "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0004_steps_break_glass, #50)",
	"ops.ops_tenant_license:" + problemNotForced:            "permanent: platform control-plane state, not tenant data; keyed by licensee_id, reached only by the ops.* operator duties (0005_license, #50)",
	"ops.ops_tenant_license:" + problemNoTenantID:           "permanent: platform control-plane state, not tenant data; keyed by licensee_id, reached only by the ops.* operator duties (0005_license, #50)",
	"ops.ops_tenant_provision:" + problemNotForced:          "permanent: platform control-plane state, not tenant data; keyed by customer_tenant, reached only by the ops.* operator duties (0006_ops_part3, #50)",
	"ops.ops_tenant_provision:" + problemNoTenantID:         "permanent: platform control-plane state, not tenant data; keyed by customer_tenant, reached only by the ops.* operator duties (0006_ops_part3, #50)",
	"ops.ops_tenant_provision_step:" + problemNotForced:     "permanent: platform control-plane state, not tenant data; keyed by customer_tenant, reached only by the ops.* operator duties (0006_ops_part3, #50)",
	"ops.ops_tenant_provision_step:" + problemNoTenantID:    "permanent: platform control-plane state, not tenant data; keyed by customer_tenant, reached only by the ops.* operator duties (0006_ops_part3, #50)",
	"ops.ops_tenant_cost:" + problemNotForced:               "permanent: platform control-plane state, not tenant data; keyed by customer_tenant, reached only by the ops.* operator duties (0006_ops_part3, #50)",
	"ops.ops_tenant_cost:" + problemNoTenantID:              "permanent: platform control-plane state, not tenant data; keyed by customer_tenant, reached only by the ops.* operator duties (0006_ops_part3, #50)",
	"ops.ops_license_check:" + problemNotForced:             "permanent: platform control-plane state, not tenant data; keyed by licensee_id, reached only by the ops.* operator duties (0006_ops_part3, #50)",
	"ops.ops_license_check:" + problemNoTenantID:            "permanent: platform control-plane state, not tenant data; keyed by licensee_id, reached only by the ops.* operator duties (0006_ops_part3, #50)",
	"ops.ops_slo_state:" + problemNotForced:                 "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0006_ops_part3, #50)",
	"ops.ops_slo_state:" + problemNoTenantID:                "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0006_ops_part3, #50)",
	"ops.ops_tenant_decommission:" + problemNotForced:       "permanent: platform control-plane state, not tenant data; keyed by customer_tenant, reached only by the ops.* operator duties (0009_ops_part4, #50)",
	"ops.ops_tenant_decommission:" + problemNoTenantID:      "permanent: platform control-plane state, not tenant data; keyed by customer_tenant, reached only by the ops.* operator duties (0009_ops_part4, #50)",
	"ops.ops_tenant_decommission_step:" + problemNotForced:  "permanent: platform control-plane state, not tenant data; keyed by customer_tenant, reached only by the ops.* operator duties (0009_ops_part4, #50)",
	"ops.ops_tenant_decommission_step:" + problemNoTenantID: "permanent: platform control-plane state, not tenant data; keyed by customer_tenant, reached only by the ops.* operator duties (0009_ops_part4, #50)",
	"ops.ops_audit:" + problemNotForced:                     "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0009_ops_part4, #50)",
	"ops.ops_audit:" + problemNoTenantID:                    "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0009_ops_part4, #50)",
	"ops.ops_clone_schedule:" + problemNotForced:            "permanent: platform control-plane state, not tenant data; keyed by source_tenant, reached only by the ops.* operator duties (0009_ops_part4, #50)",
	"ops.ops_clone_schedule:" + problemNoTenantID:           "permanent: platform control-plane state, not tenant data; keyed by source_tenant, reached only by the ops.* operator duties (0009_ops_part4, #50)",
	"ops.ops_tenant_purge:" + problemNotForced:              "permanent: platform control-plane state, not tenant data; keyed by customer_tenant, reached only by the ops.* operator duties (0011_purge_realm, #50)",
	"ops.ops_tenant_purge:" + problemNoTenantID:             "permanent: platform control-plane state, not tenant data; keyed by customer_tenant, reached only by the ops.* operator duties (0011_purge_realm, #50)",
	"ops.ops_clone_run:" + problemNotForced:                 "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0009_ops_part4, #50)",
	"ops.ops_clone_run:" + problemNoTenantID:                "permanent: platform control-plane state, not tenant data; no tenant column, reached only by the ops.* operator duties (0009_ops_part4, #50)",
	// A consolidation group spans legal entities by design: the group's units, ownership chain and consolidated trial balance are read
	// whole by the consolidation duties, and hiding a unit outside the caller's scope would give a wrong consolidated result, not a smaller one.
	"consolidation.consolidation_unit:" + problemNoEntityPol: "permanent: group-level master data; a consolidation group spans legal entities and is read whole by the consolidation duties (0003_groups, #40)",
	// #486 review decision: in EAM, legal_entity_id on asset and work_order is the GL booking entity, not a visibility scope. Sites, plans, permits,
	// rounds and every other EAM table are tenant-wide and join these rows, and legacy rows have a NULL entity that a scoped caller must see to set it
	// (PUT /v1/eam/assets/{id}/legal-entity) and that the GL reconciliation lists; a restrictive policy would hide them or would have to admit NULL.
	// The GL tables carrying the entity (eam_posting_profile, eam_gl_posting, eam_pending_booking) do have the policy (0010, 0011).
	"eam.asset:" + problemNoEntityPol:      "permanent: tenant-wide EAM master data; legal_entity_id is the GL booking entity, nullable for legacy assets a scoped caller must see to set it (#486 review decision)",
	"eam.work_order:" + problemNoEntityPol: "permanent: tenant-wide EAM work; legal_entity_id is copied from the asset for GL booking and is null for legacy orders (#486 review decision)",
	// Per the #398 review decision: identity 0008_policy_bundle_read adds operator-gated permissive SELECT policies so the policy compiler can read users and role assignments; the gate is depot.policy_compile plus a visible identity_operator row.
	"identity.identity_users:" + problemExtraPolicy:               "permanent: SELECT-only policy gated on depot.policy_compile and identity_operator (0008_policy_bundle_read, #398 review decision)",
	"identity.identity_role_assignments:" + problemExtraPolicy:    "permanent: SELECT-only policy gated on depot.policy_compile and identity_operator (0008_policy_bundle_read, #398 review decision)",
	"identity.identity_legal_entities:" + problemExtraPolicy:      "permanent: SELECT-only policy gated on depot.policy_compile and identity_operator, so the policy compiler drops scoped assignments of inactive legal entities (0019_policy_bundle_legal_entity_read, B-03b)",
	"agents-ts.agent_version:" + problemNotForced:                 "permanent: the platform's agent catalog, the same definitions for every tenant and no tenant data; depot_app may only insert and read, rows never change (0006_versions_evals_partitions, #47)",
	"agents-ts.agent_version:" + problemNoTenantID:                "permanent: the platform's agent catalog, the same definitions for every tenant and no tenant data; depot_app may only insert and read, rows never change (0006_versions_evals_partitions, #47)",
	"agents-ts.eval_run:" + problemNotForced:                      "permanent: the eval pipeline's gate results for the platform's agent definitions, run on platform datasets, no tenant data; depot_app may only read, the pipeline writes with the owner role (0006_versions_evals_partitions, #47)",
	"agents-ts.eval_run:" + problemNoTenantID:                     "permanent: the eval pipeline's gate results for the platform's agent definitions, run on platform datasets, no tenant data; depot_app may only read, the pipeline writes with the owner role (0006_versions_evals_partitions, #47)",
	"agents-ts.kill_switch_change:" + problemExtraPolicy:          "permanent: every agent pod polls every kill switch toggle (AC-47.6), whatever tenant it serves; SELECT-only policy, the rows carry no business data, and inserts stay bound by the tenant policy (0005_kill_switch, #47)",
	"close.reopen_expiry:" + problemNotForced:                     "permanent: no depot_app grant; reached only via SECURITY DEFINER functions that scan across tenants by design",
	"close.reopen_expiry:" + problemNoTenantPol:                   "permanent: no depot_app grant; reached only via SECURITY DEFINER functions that scan across tenants by design",
	"close.accrual_due:" + problemNotForced:                       "permanent: no depot_app grant; maintained by the SECURITY DEFINER accrual_run_track_due() trigger and read only by close_accrual_due_tenants(), which lists due tenants across tenants for the accrual sweeper (0007_slice_b, #460)",
	"close.accrual_due:" + problemNoTenantPol:                     "permanent: no depot_app grant; maintained by the SECURITY DEFINER accrual_run_track_due() trigger and read only by close_accrual_due_tenants(), which lists due tenants across tenants for the accrual sweeper (0007_slice_b, #460)",
	"fulfillment.sales_delivery_report_due:" + problemNotForced:   "permanent: no depot_app grant; maintained by the SECURITY DEFINER sales_delivery_report_track_due() trigger and read only by fulfillment_sales_report_due_tenants(), which lists the tenants with a sales delivery report due across tenants for the reporter (0035_sales_delivery_report, #496)",
	"fulfillment.sales_delivery_report_due:" + problemNoTenantPol: "permanent: no depot_app grant; maintained by the SECURITY DEFINER sales_delivery_report_track_due() trigger and read only by fulfillment_sales_report_due_tenants(), which lists the tenants with a sales delivery report due across tenants for the reporter (0035_sales_delivery_report, #496)",
	"fulfillment.packing_slip_gl_due:" + problemNotForced:         "permanent: no depot_app grant; maintained by the SECURITY DEFINER packing_slip_gl_track_due() trigger and read only by fulfillment_slip_gl_due_tenants(), which lists the tenants with a packing slip journal due across tenants for the poster (0036_packing_slip_gl, B-14)",
	"fulfillment.packing_slip_gl_due:" + problemNoTenantPol:       "permanent: no depot_app grant; maintained by the SECURITY DEFINER packing_slip_gl_track_due() trigger and read only by fulfillment_slip_gl_due_tenants(), which lists the tenants with a packing slip journal due across tenants for the poster (0036_packing_slip_gl, B-14)",
	"billing.sales_invoice_report_due:" + problemNotForced:        "permanent: no depot_app grant; maintained by the SECURITY DEFINER sales_invoice_report_track_due() trigger and read only by billing_sales_report_due_tenants(), which lists the tenants with a sales invoice report due across tenants for the reporter (0014_sales_invoice_report, #29)",
	"billing.sales_invoice_report_due:" + problemNoTenantPol:      "permanent: no depot_app grant; maintained by the SECURITY DEFINER sales_invoice_report_track_due() trigger and read only by billing_sales_report_due_tenants(), which lists the tenants with a sales invoice report due across tenants for the reporter (0014_sales_invoice_report, #29)",
	"billing.tax_transaction_report_due:" + problemNotForced:      "permanent: no depot_app grant; maintained by the SECURITY DEFINER tax_transaction_report_track_due() trigger and read only by billing_tax_report_due_tenants(), which lists the tenants with a tax transaction report due across tenants for the reporter (0015_tax_transaction_report, #29)",
	"billing.tax_transaction_report_due:" + problemNoTenantPol:    "permanent: no depot_app grant; maintained by the SECURITY DEFINER tax_transaction_report_track_due() trigger and read only by billing_tax_report_due_tenants(), which lists the tenants with a tax transaction report due across tenants for the reporter (0015_tax_transaction_report, #29)",
	"revrec.modification_billing_due:" + problemNotForced:         "permanent: no depot_app grant; maintained by the SECURITY DEFINER modification_billing_track_due() trigger and read only by revrec_modification_billing_due_tenants(), which lists the tenants with a contract modification billing request due across tenants for the reporter (0012_modification_reductions, #29)",
	"revrec.modification_billing_due:" + problemNoTenantPol:       "permanent: no depot_app grant; maintained by the SECURITY DEFINER modification_billing_track_due() trigger and read only by revrec_modification_billing_due_tenants(), which lists the tenants with a contract modification billing request due across tenants for the reporter (0012_modification_reductions, #29)",
	"payables.ap_withholding_report_due:" + problemNotForced:      "permanent: no depot_app grant; maintained by the SECURITY DEFINER ap_withholding_report_track_due() trigger and read only by payables_withholding_report_due_tenants(), which lists the tenants with a withholding report due across tenants for the reporter; its tenant column is report_tenant (0018_withholding_report, #13)",
	"payables.ap_withholding_report_due:" + problemNoTenantID:     "permanent: no depot_app grant; maintained by the SECURITY DEFINER ap_withholding_report_track_due() trigger and read only by payables_withholding_report_due_tenants(), which lists the tenants with a withholding report due across tenants for the reporter; its tenant column is report_tenant (0018_withholding_report, #13)",
	"edi.partner_entity_index:" + problemNotForced:                "permanent: no depot_app grant; maintained by the SECURITY DEFINER partner_trade_profile trigger and read only by edi_partner_in_entity_scope(), which returns whether the current caller's scopes cover the legal entity of a partner's trade profile, so the partner update can refuse a caller the profile's restrictive policy hides it from (0009_partner_update, #483)",
	"edi.partner_entity_index:" + problemNoTenantPol:              "permanent: no depot_app grant; maintained by the SECURITY DEFINER partner_trade_profile trigger and read only by edi_partner_in_entity_scope(), which filters by depot.tenant_id itself and returns a boolean (0009_partner_update, #483)",
	"edi.partner_entity_index:" + problemNoEntityPol:              "permanent: the index exists to answer the legal-entity question the trade profile's restrictive policy hides; no depot_app grant, read only by edi_partner_in_entity_scope() (0009_partner_update, #483)",
	"edi.public_endpoint:" + problemNotForced:                     "permanent: no depot_app grant; maintained by the SECURITY DEFINER partner_cxml and punchout_session triggers and read only by edi_public_endpoint() (cXML endpoint or punchout return id to tenant and partner for the unauthenticated cXML and punchout return calls) (0012_b2b_exchange, #35)",
	"edi.public_endpoint:" + problemNoTenantPol:                   "permanent: no depot_app grant; maintained by the SECURITY DEFINER partner_cxml and punchout_session triggers and read only by edi_public_endpoint() (cXML endpoint or punchout return id to tenant and partner for the unauthenticated cXML and punchout return calls) (0012_b2b_exchange, #35)",
	"edi.transport_work:" + problemNotForced:                      "permanent: no depot_app grant; maintained by the SECURITY DEFINER partner_transport, transport_delivery and transport_mdn triggers and read only by edi_as2_endpoint() (endpoint id to tenant and partner for the unauthenticated AS2 listener) and edi_transport_due_tenants() (due tenants for the dispatcher, the MDN timeout sweep and the SFTP poller) (0006_partner_transport, 0007_as2_mdn_queue, #483)",
	"edi.transport_work:" + problemNoTenantPol:                    "permanent: no depot_app grant; maintained by the SECURITY DEFINER partner_transport, transport_delivery and transport_mdn triggers and read only by edi_as2_endpoint() (endpoint id to tenant and partner for the unauthenticated AS2 listener) and edi_transport_due_tenants() (due tenants for the dispatcher, the MDN timeout sweep and the SFTP poller) (0006_partner_transport, 0007_as2_mdn_queue, #483)",
	"receivables.gateway_route:" + problemNotForced:               "permanent: no depot_app grant; maintained by the SECURITY DEFINER gateway_payment trigger and read only by receivables_gateway_route() (payment provider PaymentIntent id to tenant, legal entity and attempt for the unauthenticated, signature-verified payment webhook listener) (0030_gateway_payments, #30)",
	"receivables.gateway_route:" + problemNoTenantPol:             "permanent: no depot_app grant; maintained by the SECURITY DEFINER gateway_payment trigger and read only by receivables_gateway_route() (payment provider PaymentIntent id to tenant, legal entity and attempt for the unauthenticated, signature-verified payment webhook listener) (0030_gateway_payments, #30)",
	"receivables.gateway_route:" + problemNoEntityPol:             "permanent: the route exists to find the legal entity of a webhook before any scope is known; no depot_app grant, read only by receivables_gateway_route() (0030_gateway_payments, #30)",
	"identity.identity_tenants:" + problemExtraPolicy:             "permanent: SELECT-only operator policy gated on identity_operator, whose own policy is tenant-scoped; lets the provisioning operator list tenants",
	"ledger.description_template:" + problemNullEntity:            "permanent: NULL legal_entity_id is a tenant-wide description template, not a missing scope",
	"audit.audit_entry:" + problemNullEntity:                      "permanent: NULL legal_entity_id is a tenant-level audit entry (a tenant-level record such as a role assignment, or a row written before B-07) that every reader of the tenant may see; the hash chain itself is tenant-wide (0009_cdc_change_feed, B-07)",
	"audit.alert_inbox:" + problemNullEntity:                      "permanent: NULL legal_entity_id is an alert on a tenant-level audit entry, scoped as the entry is (0009_cdc_change_feed, B-07)",
	"print.print_output_rule:" + problemNullEntity:                "permanent: NULL legal_entity_id is a tenant-wide output rule; only callers with tenant-wide authority may create or delete one (#622 review)",
	"compliance.data_designation:" + problemNullEntity:            "permanent: NULL legal_entity_id is a tenant-wide CUI designation (0011_data_designation, DEPOT-301); an entity-scoped caller must still see it, or the effective class of its own records would resolve as unclassified",
	"org.address_purpose:" + problemNullEntity:                    "permanent: NULL legal_entity_id is tenant-wide reference data",
	"org.currency_rounding:" + problemNullEntity:                  "permanent: NULL legal_entity_id is tenant-wide reference data",
	"org.org_unit_translation:" + problemNullEntity:               "permanent: NULL legal_entity_id is tenant-wide reference data",
	"org.postal_address:" + problemNullEntity:                     "permanent: NULL legal_entity_id is tenant-wide reference data",
	"shopfloor.pay_export_line:" + problemNullEntity:              "permanent: an exported line follows its batch's legal entity; for a batch exported before batches carried one (NULL) it falls back to its pay line's worker's legal entity, compared with the scope like any other, so NULL alone never admits a row (shopfloor 0041, B-03b)",
}

// Reasons why tables of the services architecture gap D17 named stay tenant-wide (B-03b). Every other table of an
// EntityScopedServices service carries legal_entity_id with its restrictive policy, or follows its parent's legal
// entity (the shop floor's worker time and pay, shopfloor 0039_worker_legal_entity). "interim:" marks a table that
// should become legal-entity scoped, with the follow-up named; "permanent:" one that is tenant-wide by design.
const (
	d17ShopfloorSetup    = "permanent: tenant-wide shop floor setup (code lists, time profiles and pay agreements shared by every legal entity, unique per tenant); rules only, no person, time or pay of a worker, which follow the worker's legal entity (B-03b)"
	d17ShopfloorResource = "permanent: equipment and execution state keyed by resource or site, which are tenant-wide engineering data; a resource runs work of any legal entity, so the dispatch list, andon and OEE must see all of it. Quantities and states, no pay or person data; the production order behind a job stays legal-entity scoped in production (B-03b)"
	d17ShopfloorEdge     = "permanent: the edge device fleet (nodes, certificates, enrollment tokens, connector configuration and readings, write audit) is the tenant's plant infrastructure, not business data of a legal entity (B-03b)"
	d17Scheduling        = "permanent: finite-capacity scheduling is tenant-wide: resources are tenant-wide engineering data and a schedule run must see every order loading a resource, whatever legal entity owns it, as hiding one company's operations would double-book the resource (a wrong schedule, not a smaller one). Rows carry order references, times and capacity, no amounts; the production order stays legal-entity scoped in production (B-03b)"
	d17FX                = "permanent: exchange rates, rate types and provider import runs are tenant-wide reference data every legal entity converts with; no legal entity owns a rate (B-03b)"
	d17MobileConfig      = "permanent: handheld configuration (devices, flows, menus) shared by the tenant's warehouses; no business data (B-03b)"
	d17MobileRuntime     = "interim: a handheld session and its queued offline commands belong to the signed-in user and take effect only when replayed to warehouse under that user's service token and legal-entity scope, but the queued payloads are visible to the tenant's conflict resolvers (mobile.offline.resolve) across legal entities until the queue records the user's legal entity (tracked in #725, B-03b)"
	d17HubConfig         = "permanent: integration administration (API clients, subscriptions, event catalog, rate-limit buckets and usage, coexistence adapters) is tenant-level configuration under the integration duties, not business data of a legal entity (B-03b)"
	d17HubDelivery       = "interim: webhook deliveries and attempts hold the payloads of events of any legal entity, read only by the tenant's integration administrators; a subscription can filter on the event's legal entity (event_filter), but the delivery log is not scoped until it records the event's legal entity (tracked in #725, B-03b)"
	d17AgentsPlatform    = "permanent: the platform's agent catalog and eval results, the same for every tenant and no tenant data (0006_versions_evals_partitions, B-03b)"
	d17AgentsTenant      = "permanent: tenant-level agent controls (monthly token budget, holds against it, kill-switch changes) and the tenant's knowledge base indexed for the copilot span the tenant's legal entities by design; no business data of one legal entity (B-03b)"
	d17AgentsRuns        = "interim: an agent run, its actions, approvals, state and credentials belong to the invoking user, and act on business services only through tools that call them with the invoker's token and legal-entity scope; the run log itself is readable across legal entities by agents.run.read holders until runs record the invoker's legal entity (tracked in #725, B-03b)"
)

var d17TenantWide = map[string]map[string][]string{
	"shopfloor": {
		d17ShopfloorSetup:    {"absence_code", "time_profile", "profile_day", "pay_agreement"},
		d17ShopfloorResource: {"machine", "machine_event", "downtime_record", "downtime_reason", "andon_alert", "andon_setting", "andon_site_setting", "oee_bucket", "shop_job", "job_feedback", "job_check_result", "job_reopening", "operation_production_status", "pi_category", "pi_step_template", "pi_sheet", "pi_execution", "pi_execution_step", "pi_readout_claim", "control_recipe", "control_recipe_event", "process_category", "process_message", "process_message_delivery"},
		d17ShopfloorEdge:     {"edge_node", "edge_certificate", "edge_enrollment_token", "edge_config_version", "edge_write_audit", "node_reading"},
	},
	"scheduling": {
		d17Scheduling: {"schedule_run", "schedule_run_job", "scheduled_job", "scheduled_job_secondary", "scheduling_bottleneck_episode", "scheduling_campaign", "scheduling_campaign_cleanout", "scheduling_campaign_order", "scheduling_capacity_block", "scheduling_capacity_load", "scheduling_capacity_override", "scheduling_cleanout_counter", "scheduling_cleanout_reset", "scheduling_cleanout_rule", "scheduling_network_edge", "scheduling_operation", "scheduling_order_import", "scheduling_order_peg", "scheduling_parameters", "scheduling_resource", "scheduling_resource_network", "scheduling_setup_matrix", "scheduling_setup_matrix_entry"},
	},
	"fx": {
		d17FX: {"exchange_rate", "exchange_rate_type", "provider_import_run"},
	},
	"mobile": {
		d17MobileConfig:  {"mobile_device", "mobile_flow", "mobile_menu"},
		d17MobileRuntime: {"device_session", "offline_transaction"},
	},
	"integration-hub-ts": {
		d17HubConfig:   {"hub_api_client", "hub_subscription", "hub_catalog_entry", "hub_client_bucket", "hub_client_usage", "hub_coexistence_adapter"},
		d17HubDelivery: {"hub_delivery", "hub_attempt"},
	},
	"agents-ts": {
		d17AgentsPlatform: {"agent_version", "eval_run"},
		d17AgentsTenant:   {"token_budget", "budget_hold", "agent_budget_change", "kill_switch_change", "knowledge_chunk"},
		d17AgentsRuns:     {"agent_run", "agent_action", "approval_request", "agent_run_state", "agent_run_credential"},
	},
}

func init() {
	for service, reasons := range d17TenantWide {
		for reason, tables := range reasons {
			for _, table := range tables {
				Allowlist[service+"."+table+":"+NoEntityScope] = reason
			}
		}
	}
}
