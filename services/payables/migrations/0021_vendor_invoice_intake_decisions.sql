-- #35 (35-5): the vendor invoices received electronically get a review. An AP clerk matches a received invoice to the purchase order
-- and its receipt lines, posts the vendor invoice in receiving, and then records here which one (POSTED), or rejects the received
-- document with a reason (REJECTED). A decided intake no longer waits on the worklist. A vendor invoice closes one intake only.
-- The X12 810 of a vendor (edi's vendor route) is a third source.
ALTER TABLE ap_vendor_invoice_intake DROP CONSTRAINT IF EXISTS ap_vendor_invoice_intake_source_check;
ALTER TABLE ap_vendor_invoice_intake ADD CONSTRAINT ap_vendor_invoice_intake_source_check CHECK (source IN ('CXML', 'INTERCOMPANY', 'X12_810'));
ALTER TABLE ap_vendor_invoice_intake DROP CONSTRAINT IF EXISTS ap_vendor_invoice_intake_status_check;
ALTER TABLE ap_vendor_invoice_intake ADD CONSTRAINT ap_vendor_invoice_intake_status_check CHECK (status IN ('RECEIVED', 'POSTED', 'REJECTED'));
ALTER TABLE ap_vendor_invoice_intake
  ADD COLUMN IF NOT EXISTS decided_at timestamptz,
  ADD COLUMN IF NOT EXISTS decided_by text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS decision_note text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS vendor_invoice_id uuid;
ALTER TABLE ap_vendor_invoice_intake ADD CONSTRAINT ap_vendor_invoice_intake_decision_check
  CHECK ((status = 'RECEIVED') = (decided_at IS NULL) AND (status = 'POSTED') = (vendor_invoice_id IS NOT NULL));
CREATE UNIQUE INDEX IF NOT EXISTS ap_vendor_invoice_intake_invoice ON ap_vendor_invoice_intake (tenant_id, vendor_invoice_id) WHERE vendor_invoice_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS ap_vendor_invoice_intake_waiting ON ap_vendor_invoice_intake (tenant_id, legal_entity_id, received_at DESC, id DESC) WHERE status = 'RECEIVED';

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE (status, decided_at, decided_by, decision_note, vendor_invoice_id) ON ap_vendor_invoice_intake TO passage_app';
  END IF;
END
$$;
