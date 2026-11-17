-- B-07a (architecture gap D3, D17 for audit): the CDC-fed change chain, its daily anchor and legal-entity scoping.
--
-- audit_entry gains the legal entity of the changed record (NULL for a tenant-level record and for every row written
-- before this migration), the service its change was captured from (source, '' for HTTP ingest) and a flag
-- ('unattributed' for a captured change whose transaction carried no depot.ctx message; it is recorded as actor kind
-- 'system', user 'system:<source>'). Expand only and catalog only, as 0005: nullable or constant-default columns are
-- fast (no rewrite, so the append-only trigger never fires) and the checks are added NOT VALID; 0010 validates them.
-- The actor-kind and actor-shape checks of 0005 are replaced to admit 'system'.
--
-- Row-level security: audit_entry and alert_inbox get the restrictive legal-entity policy. It admits NULL on purpose
-- (allowlisted in platform/go/rlsexempt): a NULL legal entity is a tenant-level entry (role assignments, signatures
-- of a signer without one, rows from before B-07) that every reader of the tenant may see. The chain itself is
-- tenant-wide: the store appends, verifies and anchors with depot.legal_entity_scopes '*' (its own invariant, one
-- chain per tenant), and returns only the verdict of a verification, never another legal entity's rows.
SET LOCAL lock_timeout = '5s';

ALTER TABLE audit_entry
  ADD COLUMN IF NOT EXISTS legal_entity_id uuid,
  ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS flag text NOT NULL DEFAULT '';

ALTER TABLE audit_entry DROP CONSTRAINT IF EXISTS audit_entry_actor_kind_check;
ALTER TABLE audit_entry ADD CONSTRAINT audit_entry_actor_kind_check
  CHECK (actor_kind IN ('', 'person', 'service', 'agent', 'system')) NOT VALID;
-- A service or an agent is recorded as "service:<name>", an unattributed change as "system:<source>", and a person
-- as neither. Rows from before AUD-API-1 (actor_kind '') are left alone.
ALTER TABLE audit_entry DROP CONSTRAINT IF EXISTS audit_entry_service_actor;
ALTER TABLE audit_entry ADD CONSTRAINT audit_entry_service_actor
  CHECK (actor_kind = '' OR ((actor_kind IN ('service', 'agent')) = (user_id LIKE 'service:%')
                             AND (actor_kind = 'system') = (user_id LIKE 'system:%'))) NOT VALID;
-- 'unattributed' is exactly the system actor.
ALTER TABLE audit_entry ADD CONSTRAINT audit_entry_flag_check
  CHECK (flag IN ('', 'unattributed') AND (flag = 'unattributed') = (actor_kind = 'system')) NOT VALID;
ALTER TABLE audit_entry ADD CONSTRAINT audit_entry_source_check
  CHECK (length(source) <= 64) NOT VALID;

DROP POLICY IF EXISTS audit_entry_legal_entity_scope ON audit_entry;
CREATE POLICY audit_entry_legal_entity_scope ON audit_entry AS RESTRICTIVE
  USING (legal_entity_id IS NULL
         OR current_setting('depot.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = any(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));

-- An alert repeats a change's values, so it carries the change's legal entity and is scoped the same way.
ALTER TABLE alert_inbox ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
DROP POLICY IF EXISTS alert_inbox_legal_entity_scope ON alert_inbox;
CREATE POLICY alert_inbox_legal_entity_scope ON alert_inbox AS RESTRICTIVE
  USING (legal_entity_id IS NULL
         OR current_setting('depot.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = any(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));

-- The platform eventing inbox of the CDC consumer (`audit cdc-consume`): one row per consumer and CDC position, written
-- in the transaction that appends the position's changes, so a redelivered record is skipped.
CREATE TABLE IF NOT EXISTS consumer_inbox (
  tenant_id uuid NOT NULL,
  consumer text NOT NULL,
  event_id text NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, consumer, event_id)
);
ALTER TABLE consumer_inbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE consumer_inbox FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS consumer_inbox_tenant_access ON consumer_inbox;
CREATE POLICY consumer_inbox_tenant_access ON consumer_inbox AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);

-- The depot.ctx message of a source transaction, as the CDC consumer received it: who, why and under which trace. The
-- consumer attaches it to the row changes of the same source transaction (source, tx_id), taking the latest message
-- at or before the change's position (lsn). Kept for seven days (the nightly job purges older rows); the chain rows
-- copy what they need, so this table is not evidence.
CREATE TABLE IF NOT EXISTS cdc_tx_context (
  tenant_id uuid NOT NULL,
  source text NOT NULL,
  tx_id bigint NOT NULL,
  lsn bigint NOT NULL,
  actor text NOT NULL,
  actor_kind text NOT NULL CHECK (actor_kind IN ('person', 'service', 'agent')),
  on_behalf_of text NOT NULL DEFAULT '',
  reason text NOT NULL DEFAULT '',
  trace_id text NOT NULL DEFAULT '',
  change_set_id text NOT NULL DEFAULT '',
  event_type text NOT NULL DEFAULT '',
  event_id text NOT NULL DEFAULT '',
  workflow_instance_id text NOT NULL DEFAULT '',
  recorded_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, source, tx_id, lsn)
);
CREATE INDEX IF NOT EXISTS cdc_tx_context_recorded ON cdc_tx_context (tenant_id, recorded_at);
ALTER TABLE cdc_tx_context ENABLE ROW LEVEL SECURITY;
ALTER TABLE cdc_tx_context FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS cdc_tx_context_tenant_access ON cdc_tx_context;
CREATE POLICY cdc_tx_context_tenant_access ON cdc_tx_context AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);

-- The daily anchor: one row per tenant and UTC day with the chain head (seq and hash) the nightly job saw and its
-- verification verdict. GET /v1/audit/records:verify checks that the chain still holds every anchored head, so a
-- chain rewritten after its anchor (every hash recomputed) or cut short fails. Append-only, like the chain.
CREATE TABLE IF NOT EXISTS audit_anchor (
  tenant_id uuid NOT NULL,
  anchor_date date NOT NULL,
  head_seq bigint NOT NULL,
  head_hash char(64) NOT NULL,
  entries bigint NOT NULL,
  verified boolean NOT NULL,
  breaks bigint NOT NULL,
  first_break_seq bigint,
  legacy_breaks bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, anchor_date)
);
ALTER TABLE audit_anchor ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_anchor FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS audit_anchor_tenant_access ON audit_anchor;
CREATE POLICY audit_anchor_tenant_access ON audit_anchor AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);

CREATE OR REPLACE FUNCTION audit_anchor_immutable() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'audit_anchor is append-only';
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS audit_anchor_no_mutation ON audit_anchor;
CREATE TRIGGER audit_anchor_no_mutation
  BEFORE UPDATE OR DELETE ON audit_anchor
  FOR EACH ROW EXECUTE PROCEDURE audit_anchor_immutable();
DROP TRIGGER IF EXISTS audit_anchor_no_truncate ON audit_anchor;
CREATE TRIGGER audit_anchor_no_truncate
  BEFORE TRUNCATE ON audit_anchor
  FOR EACH STATEMENT EXECUTE PROCEDURE audit_anchor_immutable();

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON consumer_inbox, audit_anchor TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON cdc_tx_context TO depot_app';
  END IF;
END
$$;
