-- Trading partner on journal lines (#40, FR-40.6): an intercompany posting names the legal entity on the other side of the line,
-- so the consolidation can attribute intercompany sales, receivables and payables to a counterparty pair instead of reading one
-- account balance of a parent that sells to several subsidiaries. The column is optional and not part of the dimension
-- combination, so the balance projection and every existing report are unchanged; partner balances are read from the lines.
-- Catalogue-only change like 0025: a nullable column without a default and a NOT VALID check (0034 validates it). journal_line
-- stays append-only and no new table is created, so the grants of 0005 already cover it.
SET LOCAL lock_timeout = '5s';

ALTER TABLE journal_line ADD COLUMN IF NOT EXISTS trading_partner_id uuid;
ALTER TABLE journal_line ADD CONSTRAINT journal_line_trading_partner_check
  CHECK (trading_partner_id IS NULL OR trading_partner_id <> legal_entity_id) NOT VALID;
