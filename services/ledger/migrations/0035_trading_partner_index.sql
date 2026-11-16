-- dbx:no-transaction
-- Built CONCURRENTLY. Only intercompany lines carry a partner, so the partial index is small; it serves the partner balance read.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
             WHERE c.relname = 'journal_line_trading_partner_ix' AND NOT i.indisvalid) THEN
    EXECUTE 'DROP INDEX journal_line_trading_partner_ix';
  END IF;
END
$$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS journal_line_trading_partner_ix ON journal_line (tenant_id, legal_entity_id, trading_partner_id)
  WHERE trading_partner_id IS NOT NULL;
