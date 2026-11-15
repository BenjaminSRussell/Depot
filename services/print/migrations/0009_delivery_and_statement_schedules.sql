-- #46: delivery of a rendered document to the destinations of its output plan, the documents offered to the customer
-- portal, and the schedule that renders customer statements.

-- One row per (document, output rule) the service tried to deliver. source_key names the document the caller
-- delivered (or the statement run), so repeating a delivery never sends it twice: the unique key claims the destination first.
CREATE TABLE IF NOT EXISTS print_delivery (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  source_key text NOT NULL,
  rule_id uuid NOT NULL,
  copy_kind text NOT NULL CHECK (copy_kind IN ('ORIGINAL', 'COPY')),
  channel text NOT NULL CHECK (channel IN ('PRINTER', 'EMAIL', 'EINVOICE', 'ARCHIVE', 'PORTAL')),
  destination text NOT NULL DEFAULT '',
  status text NOT NULL CHECK (status IN ('PENDING', 'SENT', 'SKIPPED', 'FAILED')),
  detail text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, source_key, rule_id)
);
ALTER TABLE print_delivery ENABLE ROW LEVEL SECURITY;
ALTER TABLE print_delivery FORCE ROW LEVEL SECURITY;
CREATE POLICY print_delivery_tenant_access ON print_delivery AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- A PDF made available to the customer portal for one customer (account). The portal reads the list and the bytes
-- with its own service-only duty and only for the customer in its token.
CREATE TABLE IF NOT EXISTS print_portal_document (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  source_key text NOT NULL,
  account_id uuid NOT NULL,
  legal_entity_id uuid,
  document_type text NOT NULL,
  number text NOT NULL,
  pages integer NOT NULL CHECK (pages >= 1),
  pdf bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, source_key)
);
CREATE INDEX IF NOT EXISTS print_portal_document_account ON print_portal_document (tenant_id, account_id, created_at DESC);
ALTER TABLE print_portal_document ENABLE ROW LEVEL SECURITY;
ALTER TABLE print_portal_document FORCE ROW LEVEL SECURITY;
CREATE POLICY print_portal_document_tenant_access ON print_portal_document AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Scheduled statements: every cadence run renders the statement of every customer of one legal entity that has open
-- items and delivers it by the PERIODIC_STATEMENT output rules. claimed_until is a lease: a replica claims the due
-- schedule with FOR UPDATE SKIP LOCKED and the others skip it until the lease ends.
CREATE TABLE IF NOT EXISTS print_statement_schedule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
  legal_entity_id uuid NOT NULL,
  sender_name text NOT NULL CHECK (length(btrim(sender_name)) BETWEEN 1 AND 200),
  cadence text NOT NULL CHECK (cadence IN ('WEEKLY', 'MONTHLY')),
  run_day integer NOT NULL CHECK (run_day BETWEEN 1 AND 28),
  language text NOT NULL DEFAULT 'en' CHECK (language ~ '^[a-z]{2}(-[A-Z]{2})?$'),
  template_code text NOT NULL DEFAULT '',
  enabled boolean NOT NULL DEFAULT true,
  next_run_on date NOT NULL,
  last_run_on date,
  -- An unfinished run (a customer failed) keeps its date and is retried, with a cap, until it finishes or the cap is reached.
  run_as_of date,
  attempts integer NOT NULL DEFAULT 0,
  retry_after timestamptz,
  claimed_by text NOT NULL DEFAULT '',
  claimed_until timestamptz,
  idempotency_key text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE print_statement_schedule ENABLE ROW LEVEL SECURITY;
ALTER TABLE print_statement_schedule FORCE ROW LEVEL SECURITY;
CREATE POLICY print_statement_schedule_tenant_access ON print_statement_schedule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- One row per customer statement of one run date: a run that stops half way, or two replicas that both reach the same
-- customer, produce each statement once.
CREATE TABLE IF NOT EXISTS print_statement_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  schedule_id uuid NOT NULL,
  as_of date NOT NULL,
  customer_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('PENDING', 'DONE', 'FAILED')),
  detail text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, schedule_id, as_of, customer_id),
  FOREIGN KEY (tenant_id, schedule_id) REFERENCES print_statement_schedule (tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE print_statement_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE print_statement_run FORCE ROW LEVEL SECURITY;
CREATE POLICY print_statement_run_tenant_access ON print_statement_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON print_delivery, print_statement_run TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON print_portal_document TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON print_statement_schedule TO passage_app';
  END IF;
END
$$;
