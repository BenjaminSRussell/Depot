-- #47: the feed from receivables. payment_prediction keeps what the days-to-pay forecast (DSO) of a legal entity is computed from
-- (its entity, the invoice date and the amount); prediction_sync keeps where the delta reads left off.
ALTER TABLE payment_prediction ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
ALTER TABLE payment_prediction ADD COLUMN IF NOT EXISTS invoice_date date;
ALTER TABLE payment_prediction ADD COLUMN IF NOT EXISTS reversed_at date;
ALTER TABLE payment_prediction ADD COLUMN IF NOT EXISTS amount numeric(20,6) CHECK (amount IS NULL OR amount >= 0);
CREATE INDEX IF NOT EXISTS payment_prediction_open_by_entity ON payment_prediction (tenant_id, legal_entity_id) WHERE actual_paid_date IS NULL AND reversed_at IS NULL;

-- One row per (tenant, legal entity): the updated_at of the last open and paid invoice read from receivables (the next delta starts
-- there), and when the entity was last retrained and scored.
CREATE TABLE IF NOT EXISTS prediction_sync (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  open_watermark timestamptz,
  paid_watermark timestamptz,
  reversed_watermark timestamptz,
  last_retrain_at timestamptz,
  last_scored_at timestamptz,
  PRIMARY KEY (tenant_id, legal_entity_id)
);
ALTER TABLE prediction_sync ENABLE ROW LEVEL SECURITY;
ALTER TABLE prediction_sync FORCE ROW LEVEL SECURITY;
CREATE POLICY prediction_sync_tenant_access ON prediction_sync AS PERMISSIVE
  USING (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON prediction_sync TO passage_app';
  END IF;
END
$$;
