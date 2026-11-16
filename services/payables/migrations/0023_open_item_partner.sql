-- #40: the legal entity on the other side of an intercompany vendor invoice, kept on the AP open item that an auto-posted intercompany
-- charge opens, when its ledger entry named the lender as trading partner on the cost and control lines. The payment run names the
-- same partner on the control debit of exactly those items, so a payment lowers the tagged payable the invoice raised. An invoice
-- posted without a partner (review mode, posted by a clerk in receiving) and every ordinary invoice leave it null and their payment
-- lines stay untagged. A nullable column without a default is a catalogue-only change; ap_open_item keeps its RLS and grants.
ALTER TABLE ap_open_item ADD COLUMN IF NOT EXISTS trading_partner_id uuid;
ALTER TABLE ap_open_item ADD CONSTRAINT ap_open_item_trading_partner_check CHECK (trading_partner_id IS NULL OR trading_partner_id <> legal_entity_id) NOT VALID;
