-- #35 (35-5): a matched self-billed document is posted as our legal customer invoice (billing POST /v1/billing/self-billed-invoices).
-- The tax each line carries is kept with the match, the partner's revenue account, tax code and payment term are settings, and the
-- document records the invoice billing posted (or why it could not yet), so the sweep can retry it.
ALTER TABLE self_billing_line ADD COLUMN IF NOT EXISTS tax numeric(20, 6);
ALTER TABLE self_billing_config
  ADD COLUMN IF NOT EXISTS revenue_account text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS tax_code text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS payment_term_days integer NOT NULL DEFAULT 30 CHECK (payment_term_days BETWEEN 0 AND 365);
ALTER TABLE self_billing_document
  ADD COLUMN IF NOT EXISTS billing_invoice_id uuid,
  ADD COLUMN IF NOT EXISTS billing_number text,
  ADD COLUMN IF NOT EXISTS billed_at timestamptz,
  ADD COLUMN IF NOT EXISTS post_error text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS post_attempted_at timestamptz,
  -- Retries back off (next_post_at) and stop after a number of attempts or at a definite refusal (post_parked_at); saving the partner's
  -- self-billing settings gives parked documents a fresh start.
  ADD COLUMN IF NOT EXISTS post_attempts integer NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS next_post_at timestamptz,
  ADD COLUMN IF NOT EXISTS post_parked_at timestamptz;
CREATE INDEX IF NOT EXISTS self_billing_document_unposted ON self_billing_document (tenant_id, created_at) WHERE status = 'MATCHED' AND billed_at IS NULL AND post_parked_at IS NULL;

-- The scheduled sweep finds its tenants through this function: it now also lists the tenants with an inbound X12 810 not matched yet
-- (it read INVOIC only) and with a matched document still waiting for billing to post it.
DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION edi_selfbill_due_tenants() RETURNS SETOF uuid
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
        SELECT m.tenant_id FROM message m
          JOIN interchange i ON i.tenant_id = m.tenant_id AND i.id = m.interchange_id AND i.direction = 'IN'
          WHERE m.transaction_set IN ('INVOIC', '810') AND m.status = 'MAPPED' AND m.canonical->>'selfBilled' = 'true'
        UNION
        SELECT d.tenant_id FROM self_billing_document d WHERE d.status = 'MATCHED' AND d.billed_at IS NULL AND d.post_parked_at IS NULL AND (d.next_post_at IS NULL OR d.next_post_at <= now())
        UNION
        SELECT sh.tenant_id FROM self_billing_shipment sh
          JOIN self_billing_config c ON c.tenant_id = sh.tenant_id AND c.partner_id = sh.partner_id
          WHERE sh.billed_by IS NULL AND sh.shipped_on + c.wait_days <= current_date
            AND NOT EXISTS (SELECT 1 FROM self_billing_dispute d WHERE d.tenant_id = sh.tenant_id AND d.shipment_id = sh.id AND d.kind = 'UNBILLED')
      $body$;
  $f$, current_schema());
END
$do$;
