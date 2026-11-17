-- The Intrastat feed consumer (#13, `tax consume`, group tax-intrastat) stages the movements of fulfillment's packingslip.posted and
-- receiving's receipt.posted. The platform eventing inbox records each event id here in the transaction that stages its movement, so a
-- redelivered event stages nothing twice.
CREATE TABLE IF NOT EXISTS consumer_inbox (
  tenant_id uuid NOT NULL,
  consumer text NOT NULL,
  event_id text NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, consumer, event_id)
);
ALTER TABLE consumer_inbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE consumer_inbox FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS consumer_inbox_tenant_access ON consumer_inbox;
CREATE POLICY consumer_inbox_tenant_access ON consumer_inbox AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON consumer_inbox TO depot_app';
  END IF;
END
$$;
