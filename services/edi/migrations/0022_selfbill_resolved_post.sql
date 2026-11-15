-- #35 (35-5, round 5): a document held by a dispute posts once every dispute of it is decided (its status is then RESOLVED), so the sweep and its
-- tenant finder look at RESOLVED documents as well as MATCHED ones. A posted invoice's lines name the sales order and order line they bill, so a
-- shipment now remembers the order and the order line it shipped (nullable: shipments registered before this file have none and are posted
-- without the references).
ALTER TABLE self_billing_shipment ADD COLUMN IF NOT EXISTS order_id text, ADD COLUMN IF NOT EXISTS order_line_id text;
DROP INDEX IF EXISTS self_billing_document_unposted;
CREATE INDEX IF NOT EXISTS self_billing_document_unposted ON self_billing_document (tenant_id, created_at) WHERE status IN ('MATCHED', 'RESOLVED') AND billed_at IS NULL AND post_parked_at IS NULL;

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
        SELECT d.tenant_id FROM self_billing_document d WHERE d.status IN ('MATCHED', 'RESOLVED') AND d.billed_at IS NULL AND d.post_parked_at IS NULL AND (d.next_post_at IS NULL OR d.next_post_at <= now())
        UNION
        SELECT sh.tenant_id FROM self_billing_shipment sh
          JOIN self_billing_config c ON c.tenant_id = sh.tenant_id AND c.partner_id = sh.partner_id
          WHERE sh.billed_by IS NULL AND sh.shipped_on + c.wait_days <= current_date
            AND NOT EXISTS (SELECT 1 FROM self_billing_dispute d WHERE d.tenant_id = sh.tenant_id AND d.shipment_id = sh.id AND d.kind = 'UNBILLED')
      $body$;
  $f$, current_schema());
END
$do$;
