-- #40: the legal entity on the other side of an intercompany invoice (the borrower of a charge billed in Invoice settlement mode).
-- The GL posting names it on the receivable and revenue lines, so the consolidation attributes intercompany sales and receivables
-- to a counterparty pair. Null on every ordinary invoice. A nullable column without a default is a catalogue-only change.
ALTER TABLE invoice ADD COLUMN IF NOT EXISTS trading_partner_id uuid;
ALTER TABLE invoice ADD CONSTRAINT invoice_trading_partner_check CHECK (trading_partner_id IS NULL OR trading_partner_id <> legal_entity_id) NOT VALID;
