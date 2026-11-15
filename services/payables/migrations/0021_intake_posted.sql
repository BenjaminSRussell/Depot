-- #40: a pairing that opts in to auto-post (intercompany autoPostInvoice) has its borrower-side vendor invoice posted by the
-- intercompany charge consumer instead of waiting for an AP clerk: the consumer books the ledger entry, opens the AP item and marks
-- the intake row POSTED with the entry id. A row still RECEIVED is one nobody posted yet.
ALTER TABLE ap_vendor_invoice_intake DROP CONSTRAINT IF EXISTS ap_vendor_invoice_intake_status_check;
ALTER TABLE ap_vendor_invoice_intake ADD CONSTRAINT ap_vendor_invoice_intake_status_check CHECK (status IN ('RECEIVED', 'POSTED'));
ALTER TABLE ap_vendor_invoice_intake ADD COLUMN IF NOT EXISTS ledger_entry_id text NOT NULL DEFAULT '';
ALTER TABLE ap_vendor_invoice_intake ADD COLUMN IF NOT EXISTS posted_at timestamptz;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE (status, ledger_entry_id, posted_at) ON ap_vendor_invoice_intake TO passage_app';
  END IF;
END
$$;
