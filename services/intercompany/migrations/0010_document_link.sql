-- B-40 (FR-40.2): the IcDocumentLink rows of the mirror saga S4. One link per source document and mirror type; a compensated saga marks
-- its link compensated instead of deleting it, so the reconciliation and the pending-saga view keep the history.
CREATE TABLE IF NOT EXISTS ic_document_link (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  pair_id text NOT NULL CHECK (length(pair_id) BETWEEN 1 AND 64),
  source_entity text NOT NULL CHECK (length(source_entity) BETWEEN 1 AND 64),
  source_type text NOT NULL CHECK (source_type IN ('sales_order', 'packing_slip', 'invoice')),
  source_id text NOT NULL CHECK (length(source_id) BETWEEN 1 AND 64),
  mirror_entity text NOT NULL CHECK (length(mirror_entity) BETWEEN 1 AND 64 AND mirror_entity <> source_entity),
  mirror_type text NOT NULL CHECK (mirror_type IN ('purchase_order', 'receipt', 'vendor_invoice')),
  mirror_id text NOT NULL CHECK (length(mirror_id) BETWEEN 1 AND 64),
  pricing_basis text NOT NULL DEFAULT '' CHECK (length(pricing_basis) <= 64),
  status text NOT NULL DEFAULT 'linked' CHECK (status IN ('linked', 'compensated')),
  created_by text NOT NULL CHECK (length(created_by) >= 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, source_type, source_id)
);

ALTER TABLE ic_document_link ENABLE ROW LEVEL SECURITY;
ALTER TABLE ic_document_link FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ic_document_link_tenant_access ON ic_document_link;
CREATE POLICY ic_document_link_tenant_access ON ic_document_link AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ic_document_link TO depot_app';
  END IF;
END
$$;
