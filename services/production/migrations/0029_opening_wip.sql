-- #49: open production orders and their work in progress loaded at cutover. A batch is idempotent per (legal entity, batch key); a line is idempotent
-- per (legal entity, reference), the reference being the legacy production order id the migration factory holds in its cross-reference. An imported
-- order keeps its legacy order number and is Started with its open quantity; its WIP is booked by one posted journal of type opening-wip per order,
-- as WIP subledger rows (wip-material, wip-labor and wip-overhead against the posting profile's inventory, labor absorption and overhead absorption
-- accounts) that production's ordinary GL posting books in the general ledger. The order carries no BOM or route snapshot: it is a WIP balance, not
-- a plan.
ALTER TABLE production_journal DROP CONSTRAINT IF EXISTS production_journal_journal_type_check;
ALTER TABLE production_journal ADD CONSTRAINT production_journal_journal_type_check
  CHECK (journal_type IN ('picking-list', 'route-card', 'report-as-finished', 'split-transfer', 'job-card', 'opening-wip'));

CREATE TABLE IF NOT EXISTS production_opening_batch (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  batch_key text NOT NULL CHECK (batch_key <> ''),
  content_hash text NOT NULL,
  line_count integer NOT NULL CHECK (line_count > 0),
  control_totals jsonb NOT NULL,
  imported_by text NOT NULL,
  imported_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, batch_key)
);

CREATE TABLE IF NOT EXISTS production_opening_line (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  reference text NOT NULL CHECK (reference <> ''),
  batch_id uuid NOT NULL,
  measures jsonb NOT NULL,
  PRIMARY KEY (tenant_id, legal_entity_id, reference),
  FOREIGN KEY (tenant_id, batch_id) REFERENCES production_opening_batch (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS production_opening_line_batch ON production_opening_line (tenant_id, batch_id);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['production_opening_batch', 'production_opening_line'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''depot.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''depot.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''depot.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''depot.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON production_opening_batch, production_opening_line TO depot_app';
  END IF;
END
$$;
