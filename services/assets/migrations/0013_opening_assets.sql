-- #49: the opening asset register loaded at cutover. A batch is idempotent per (legal entity, batch key); a line is idempotent per
-- (legal entity, reference), the reference being the legacy asset-book id the migration factory holds in its cross-reference. Each line
-- becomes an asset (one per asset number, shared by its books) with a book carrying the cost, the accumulated depreciation, the NBV and
-- the depreciation position at the cutover, and one Acquisition transaction of source type 'opening' that books cost, accumulated
-- depreciation and (against the acquisition clearing account) the NBV. Nothing is announced: no asset event is written.
CREATE TABLE IF NOT EXISTS asset_opening_batch (
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

CREATE TABLE IF NOT EXISTS asset_opening_line (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  reference text NOT NULL CHECK (reference <> ''),
  batch_id uuid NOT NULL,
  measures jsonb NOT NULL,
  PRIMARY KEY (tenant_id, legal_entity_id, reference),
  FOREIGN KEY (tenant_id, batch_id) REFERENCES asset_opening_batch (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS asset_opening_line_batch ON asset_opening_line (tenant_id, batch_id);

-- Which asset and book a reference created, so a second book of an asset imported by an earlier batch finds its asset.
CREATE TABLE IF NOT EXISTS asset_opening_link (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  reference text NOT NULL,
  asset_id uuid NOT NULL,
  asset_book_id uuid NOT NULL,
  PRIMARY KEY (tenant_id, legal_entity_id, reference),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES asset (tenant_id, id),
  FOREIGN KEY (tenant_id, asset_book_id) REFERENCES asset_book (tenant_id, id)
);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['asset_opening_batch', 'asset_opening_line', 'asset_opening_link'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON asset_opening_batch, asset_opening_line, asset_opening_link TO passage_app';
  END IF;
END
$$;
