-- #40: the legal entity on the other side of an intercompany invoice, kept on each of its open items (and on the credit items of its credit
-- notes). Billing's invoice.posted names it as trading_partner_id when the lender's invoice of an intercompany charge was booked with the
-- partner on its receivable line. The ledger journals of a payment application, an unapply and the realized exchange difference of an
-- item then name the same partner on the receivable line, item by item, so a payment lowers the tagged receivable the invoice raised.
-- An ordinary invoice, a dunning fee and interest have none and their lines stay untagged. A nullable column without a default is a
-- catalogue-only change; open_item keeps its RLS and grants.
ALTER TABLE open_item ADD COLUMN IF NOT EXISTS trading_partner_id uuid;
ALTER TABLE open_item ADD CONSTRAINT open_item_trading_partner_check CHECK (trading_partner_id IS NULL OR trading_partner_id <> legal_entity_id) NOT VALID;
