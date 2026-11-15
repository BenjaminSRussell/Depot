-- #49: open sales orders loaded at cutover. A batch is idempotent per (legal entity, batch key); a line is
-- idempotent per (legal entity, reference), the reference being the legacy line id the migration factory
-- holds in its cross-reference. An imported order keeps its legacy order number and is CONFIRMED (or
-- PARTIALLY_SHIPPED when part of it shipped before the cutover) with the delivered quantity on its lines.
CREATE TABLE IF NOT EXISTS sales_opening_batch (
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

CREATE TABLE IF NOT EXISTS sales_opening_line (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  reference text NOT NULL CHECK (reference <> ''),
  batch_id uuid NOT NULL,
  order_id uuid NOT NULL,
  line_id uuid NOT NULL,
  currency char(3) NOT NULL,
  ordered_qty numeric(20,6) NOT NULL CHECK (ordered_qty > 0),
  delivered_qty numeric(20,6) NOT NULL CHECK (delivered_qty >= 0),
  open_qty numeric(20,6) NOT NULL CHECK (open_qty > 0),
  open_value numeric(20,6) NOT NULL CHECK (open_value >= 0),
  PRIMARY KEY (tenant_id, legal_entity_id, reference),
  FOREIGN KEY (tenant_id, batch_id) REFERENCES sales_opening_batch (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS sales_opening_line_batch ON sales_opening_line (tenant_id, batch_id);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['sales_opening_batch', 'sales_opening_line'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON sales_opening_batch, sales_opening_line TO passage_app';
  END IF;
END
$$;
