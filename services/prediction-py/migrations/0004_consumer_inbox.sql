-- #47: the receivables event consumer (src/prediction/consumer.py) records each event id it has handled, so a redelivered event is
-- not handled twice. Same table as the Go services' eventing.PgInbox, tenant row-level security as everywhere. The row is written in
-- the transaction that surrounds the recompute, so a recompute that fails leaves no row and the event is delivered again.
CREATE TABLE IF NOT EXISTS consumer_inbox (
  tenant_id uuid NOT NULL,
  consumer text NOT NULL,
  event_id text NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, consumer, event_id)
);
ALTER TABLE consumer_inbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE consumer_inbox FORCE ROW LEVEL SECURITY;
CREATE POLICY consumer_inbox_tenant_access ON consumer_inbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON consumer_inbox TO passage_app';
  END IF;
END
$$;
