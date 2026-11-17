-- #40: a pairing that opts in to auto-post (intercompany autoPostInvoice) has its borrower-side vendor invoice posted by the
-- intercompany charge consumer instead of waiting for an AP clerk: the consumer books the ledger entry, opens the AP item and marks
-- the intake row POSTED with the entry id (ledger_entry_id, no vendor invoice in receiving). A refused auto-post stays RECEIVED with
-- the reason in auto_post_note, for a clerk to post by hand; a row a clerk posted is never auto-posted.
ALTER TABLE ap_vendor_invoice_intake ADD COLUMN IF NOT EXISTS ledger_entry_id text NOT NULL DEFAULT '';
ALTER TABLE ap_vendor_invoice_intake ADD COLUMN IF NOT EXISTS auto_post_note text NOT NULL DEFAULT '';
ALTER TABLE ap_vendor_invoice_intake DROP CONSTRAINT IF EXISTS ap_vendor_invoice_intake_decision_check;
ALTER TABLE ap_vendor_invoice_intake ADD CONSTRAINT ap_vendor_invoice_intake_decision_check
  CHECK ((status = 'RECEIVED') = (decided_at IS NULL) AND (status <> 'POSTED' OR vendor_invoice_id IS NOT NULL OR ledger_entry_id <> '')
         AND (vendor_invoice_id IS NULL OR ledger_entry_id = ''));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT UPDATE (ledger_entry_id, auto_post_note) ON ap_vendor_invoice_intake TO depot_app';
  END IF;
END
$$;
