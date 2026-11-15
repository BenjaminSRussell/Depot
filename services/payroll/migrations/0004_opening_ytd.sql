-- #49: payroll year-to-date balances loaded at cutover. A batch is idempotent per (legal entity, batch key); a line is idempotent per (legal entity,
-- reference), the reference being the legacy id of the worker's year the migration factory holds in its cross-reference (suffixed with the pay element).
-- A balance is one amount per worker, tax year and pay element (GROSS, TAX, DEDUCTIONS, NET) of a legal entity; a second batch cannot restate it.
-- Nothing is posted: the liabilities these balances stand for arrive in the general ledger with the migrated GL balances, and no journal is written here.
CREATE TABLE IF NOT EXISTS payroll_opening_batch (
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

CREATE TABLE IF NOT EXISTS payroll_opening_line (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  reference text NOT NULL CHECK (reference <> ''),
  batch_id uuid NOT NULL,
  measures jsonb NOT NULL,
  PRIMARY KEY (tenant_id, legal_entity_id, reference),
  FOREIGN KEY (tenant_id, batch_id) REFERENCES payroll_opening_batch (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS payroll_opening_line_batch ON payroll_opening_line (tenant_id, batch_id);

CREATE TABLE IF NOT EXISTS payroll_opening_ytd (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  worker_id uuid NOT NULL,
  tax_year char(4) NOT NULL CHECK (tax_year ~ '^[0-9]{4}$'),
  element text NOT NULL CHECK (element IN ('GROSS', 'TAX', 'DEDUCTIONS', 'NET')),
  currency char(3) NOT NULL,
  amount numeric(18, 2) NOT NULL,
  reference text NOT NULL,
  batch_id uuid NOT NULL,
  PRIMARY KEY (tenant_id, legal_entity_id, worker_id, tax_year, element),
  FOREIGN KEY (tenant_id, batch_id) REFERENCES payroll_opening_batch (tenant_id, id)
);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['payroll_opening_batch', 'payroll_opening_line', 'payroll_opening_ytd'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON payroll_opening_batch, payroll_opening_line, payroll_opening_ytd TO passage_app';
  END IF;
END
$$;
