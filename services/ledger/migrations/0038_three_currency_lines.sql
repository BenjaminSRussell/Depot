ALTER TABLE journal_line
  ADD COLUMN IF NOT EXISTS currency_rpt char(3),
  ADD COLUMN IF NOT EXISTS amount_rpt numeric(20,6) CHECK (amount_rpt >= 0),
  ADD COLUMN IF NOT EXISTS rate_txn_acct numeric(28,12) CHECK (rate_txn_acct > 0),
  ADD COLUMN IF NOT EXISTS rate_acct_rpt numeric(28,12) CHECK (rate_acct_rpt > 0),
  ADD COLUMN IF NOT EXISTS rate_override_reason text CHECK (char_length(rate_override_reason) BETWEEN 1 AND 255);

CREATE OR REPLACE FUNCTION ledger_check_entry_balanced() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  lines bigint;
  acct_net numeric;
  rpt_net numeric;
  txn_unbalanced text;
BEGIN
  SELECT count(*),
         coalesce(sum(CASE WHEN debit_credit = 'D' THEN amount_acct ELSE -amount_acct END), 0),
         coalesce(sum(CASE WHEN debit_credit = 'D' THEN amount_rpt ELSE -amount_rpt END), 0)
    INTO lines, acct_net, rpt_net
    FROM journal_line
   WHERE tenant_id = NEW.tenant_id AND entry_id = NEW.id;
  SELECT string_agg(currency_txn, ',' ORDER BY currency_txn)
    INTO txn_unbalanced
    FROM (SELECT currency_txn
            FROM journal_line
           WHERE tenant_id = NEW.tenant_id AND entry_id = NEW.id
           GROUP BY currency_txn
          HAVING sum(CASE WHEN debit_credit = 'D' THEN amount_txn ELSE -amount_txn END) <> 0) unbalanced;
  IF lines = 0 OR acct_net <> 0 OR rpt_net <> 0 OR txn_unbalanced IS NOT NULL THEN
    RAISE EXCEPTION 'LEDGER_UNBALANCED: entry % has % lines, net acct %, net rpt %, unbalanced txn currencies %',
      NEW.id, lines, acct_net, rpt_net, coalesce(txn_unbalanced, 'none')
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END;
$$;
