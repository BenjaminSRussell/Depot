-- DEPOT-300: the ledger legs of a payment application reversal are posted outside the database transaction. Each reversal attempt is
-- recorded and committed before the first ledger post, with its deterministic ledger keys and lines, so an attempt whose outcome is
-- unknown (lost ledger answer, failed commit, crash) is resolved by the sweeper: txid_status tells whether the transaction committed,
-- and the legs are compensated by one set of journals when it did not. A new generation starts only after the previous one is COMPENSATED.
CREATE TABLE IF NOT EXISTS payment_application_reversal_attempt (
  tenant_id uuid NOT NULL,
  application_id uuid NOT NULL,
  generation integer NOT NULL CHECK (generation >= 0),
  legal_entity_id uuid NOT NULL,
  payment_id uuid NOT NULL,
  posting_date date NOT NULL,
  state text NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING', 'POSTED', 'COMMITTED', 'COMPENSATING', 'COMPENSATED', 'AMBIGUOUS')),
  main_key text NOT NULL,
  main_currency text NOT NULL,
  main_lines jsonb NOT NULL,
  main_entry_id text,
  fx_key text,
  fx_currency text,
  fx_lines jsonb,
  fx_entry_id text,
  probe_legs text[] NOT NULL DEFAULT '{}',
  txid bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, application_id, generation)
);
CREATE INDEX IF NOT EXISTS payment_application_reversal_attempt_open ON payment_application_reversal_attempt (tenant_id, updated_at)
  WHERE state IN ('PENDING', 'POSTED', 'COMPENSATING', 'AMBIGUOUS');
ALTER TABLE payment_application_reversal_attempt ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_application_reversal_attempt FORCE ROW LEVEL SECURITY;
CREATE POLICY payment_application_reversal_attempt_tenant_access ON payment_application_reversal_attempt AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);
CREATE POLICY payment_application_reversal_attempt_legal_entity_scope ON payment_application_reversal_attempt AS RESTRICTIVE
  USING (current_setting('depot.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON payment_application_reversal_attempt TO depot_app';
  END IF;
END
$$;
