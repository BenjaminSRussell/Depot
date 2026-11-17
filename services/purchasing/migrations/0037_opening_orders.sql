-- #49: open purchase orders loaded at cutover. A batch is idempotent per (legal entity, batch key); a line is
-- idempotent per (legal entity, reference), the reference being the legacy line id the migration factory holds in
-- its cross-reference. An imported PO keeps its legacy number and is CONFIRMED at version 1 with the received
-- quantity on its lines.
CREATE TABLE IF NOT EXISTS po_opening_batch (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  batch_key text NOT NULL CHECK (batch_key <> ''),
  content_hash text NOT NULL,
  order_count integer NOT NULL CHECK (order_count > 0),
  line_count integer NOT NULL CHECK (line_count > 0),
  control_totals jsonb NOT NULL,
  imported_by text NOT NULL,
  imported_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, batch_key)
);

CREATE TABLE IF NOT EXISTS po_opening_line (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  reference text NOT NULL CHECK (reference <> ''),
  batch_id uuid NOT NULL,
  purchase_order_id uuid NOT NULL,
  line_no integer NOT NULL,
  currency char(3) NOT NULL,
  ordered_qty numeric(20,6) NOT NULL CHECK (ordered_qty > 0),
  received_qty numeric(20,6) NOT NULL CHECK (received_qty >= 0),
  open_qty numeric(20,6) NOT NULL CHECK (open_qty > 0),
  open_value numeric(20,6) NOT NULL CHECK (open_value >= 0),
  PRIMARY KEY (tenant_id, legal_entity_id, reference),
  FOREIGN KEY (tenant_id, batch_id) REFERENCES po_opening_batch (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS po_opening_line_batch ON po_opening_line (tenant_id, batch_id);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['po_opening_batch', 'po_opening_line'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''depot.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''depot.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''depot.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''depot.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON po_opening_batch, po_opening_line TO depot_app';
  END IF;
END
$$;
