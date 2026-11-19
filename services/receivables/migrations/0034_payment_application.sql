-- DEPOT-300: one :apply call is one payment application, so it can be reversed as a unit. The application row is never deleted: a
-- reversal marks it REVERSED with who, when and why, and the settlement rows it stamped (application_id) get compensating rows.
-- payment.bank_reconciled_at is a nullable flag owned by receivables; null counts as not reconciled.
CREATE TABLE IF NOT EXISTS payment_application (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  payment_id uuid NOT NULL,
  status text NOT NULL DEFAULT 'APPLIED' CHECK (status IN ('APPLIED', 'REVERSED')),
  posting_date date NOT NULL,
  applied_by text NOT NULL DEFAULT '',
  applied_at timestamptz NOT NULL DEFAULT now(),
  reversed_by text,
  reversed_at timestamptz,
  reversal_reason text,
  reversal_journal_entry_id text,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, payment_id) REFERENCES payment (tenant_id, id),
  CHECK ((status = 'REVERSED') = (reversed_at IS NOT NULL AND reversal_journal_entry_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS payment_application_payment ON payment_application (tenant_id, payment_id);
ALTER TABLE payment_application ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_application FORCE ROW LEVEL SECURITY;
CREATE POLICY payment_application_tenant_access ON payment_application AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);
CREATE POLICY payment_application_legal_entity_scope ON payment_application AS RESTRICTIVE
  USING (current_setting('depot.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));
ALTER TABLE settlement ADD COLUMN IF NOT EXISTS application_id uuid;
ALTER TABLE settlement ADD CONSTRAINT settlement_application_fk FOREIGN KEY (tenant_id, application_id) REFERENCES payment_application (tenant_id, id);
CREATE INDEX IF NOT EXISTS settlement_application ON settlement (tenant_id, application_id) WHERE application_id IS NOT NULL;
ALTER TABLE payment ADD COLUMN IF NOT EXISTS bank_reconciled_at timestamptz;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON payment_application TO depot_app';
  END IF;
END
$$;
