-- A register of the source documents budget checks have seen (#500): one row per document with a readable number, and one
-- row per document line with its dimension, span and amount. The control screens pick a document and a line from it
-- instead of typing ids; every check records its document and lines here (the check writes both in its own transaction).
-- The source documents themselves stay owned by purchasing, payables and ledger.
CREATE TABLE IF NOT EXISTS budget_document (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  number text,
  document_type text NOT NULL CHECK (document_type IN ('Requisition', 'PurchaseOrder', 'Invoice', 'Journal', 'Reservation')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
CREATE TABLE IF NOT EXISTS budget_document_line (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  document_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  dimension_key text NOT NULL,
  span_key text NOT NULL,
  amount numeric(19,4) NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, document_id) REFERENCES budget_document (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS budget_document_line_document ON budget_document_line (tenant_id, document_id, line_no);

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['budget_document', 'budget_document_line']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''depot.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''depot.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''depot.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''depot.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  END LOOP;
END $$;

-- Numbers count per tenant (BD-000042) from a counter row the insert trigger takes under a row lock, as quality orders do.
CREATE TABLE IF NOT EXISTS budget_document_counter (
  tenant_id uuid PRIMARY KEY,
  last_number bigint NOT NULL CHECK (last_number > 0)
);
ALTER TABLE budget_document_counter ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget_document_counter FORCE ROW LEVEL SECURITY;
CREATE POLICY budget_document_counter_tenant_access ON budget_document_counter AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);

CREATE OR REPLACE FUNCTION budget_document_assign_number() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  n bigint;
BEGIN
  IF NEW.number IS NULL THEN
    INSERT INTO budget_document_counter AS c (tenant_id, last_number) VALUES (NEW.tenant_id, 1)
      ON CONFLICT (tenant_id) DO UPDATE SET last_number = c.last_number + 1
      RETURNING last_number INTO n;
    NEW.number := 'BD-' || lpad(n::text, greatest(6, length(n::text)), '0');
  END IF;
  RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS budget_document_number ON budget_document;
CREATE TRIGGER budget_document_number BEFORE INSERT ON budget_document FOR EACH ROW EXECUTE FUNCTION budget_document_assign_number();
CREATE UNIQUE INDEX IF NOT EXISTS budget_document_number_key ON budget_document (tenant_id, number);

-- Backfill from the checks already recorded: the oldest check of a document sets its type and first-seen time and orders
-- the numbers per tenant, the newest check its lines. It reads every tenant, so it runs only under a role that bypasses
-- row-level security; otherwise it is skipped with a notice and the register fills as documents are checked again.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND (rolsuper OR rolbypassrls)) THEN
    RAISE NOTICE 'budget document backfill skipped: role % does not bypass row-level security', current_user;
    RETURN;
  END IF;
  INSERT INTO budget_document (tenant_id, id, legal_entity_id, document_type, created_at, updated_at)
  SELECT tenant_id, document_id, legal_entity_id, document_type, checked_at, checked_at
  FROM (SELECT DISTINCT ON (tenant_id, document_id) tenant_id, document_id, legal_entity_id, document_type, checked_at
        FROM budget_check_result ORDER BY tenant_id, document_id, checked_at) first_checks
  ORDER BY checked_at, document_id;
  INSERT INTO budget_document_line (tenant_id, id, document_id, legal_entity_id, line_no, dimension_key, span_key, amount)
  SELECT DISTINCT ON (c.tenant_id, (l.v ->> 'lineId')::uuid) c.tenant_id, (l.v ->> 'lineId')::uuid, c.document_id, c.legal_entity_id, l.n::integer,
         l.v ->> 'dimensionKey', l.v ->> 'spanKey', (l.v ->> 'amount')::numeric
  FROM budget_check_result c, LATERAL jsonb_array_elements(c.lines) WITH ORDINALITY AS l(v, n)
  ORDER BY c.tenant_id, (l.v ->> 'lineId')::uuid, c.checked_at DESC;
END
$$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON budget_document, budget_document_line, budget_document_counter TO depot_app';
  END IF;
END $$;
