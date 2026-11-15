-- #30 round 4: reversal of a credit settlement. Like a payment's settlement (migration 0026), a credit settlement is reversed by a
-- row per original (reversal_of_id) with its amounts negated, so every as-of sum over credit_settlement (statements, balance
-- confirmations) shows the items settled up to the reversal date and open again after it. Original rows keep their positive amounts.
ALTER TABLE credit_settlement DROP CONSTRAINT IF EXISTS credit_settlement_amount_check;
ALTER TABLE credit_settlement DROP CONSTRAINT IF EXISTS credit_settlement_debit_amount_check;
ALTER TABLE credit_settlement ADD COLUMN IF NOT EXISTS reversal_of_id uuid;
ALTER TABLE credit_settlement ADD COLUMN IF NOT EXISTS reversal_reason text NOT NULL DEFAULT '';
ALTER TABLE credit_settlement ADD CONSTRAINT credit_settlement_reversal_fk FOREIGN KEY (tenant_id, reversal_of_id) REFERENCES credit_settlement (tenant_id, id);
ALTER TABLE credit_settlement ADD CONSTRAINT credit_settlement_amount_sign CHECK (
  (reversal_of_id IS NULL AND amount > 0 AND (debit_amount IS NULL OR debit_amount > 0))
  OR (reversal_of_id IS NOT NULL AND amount < 0 AND (debit_amount IS NULL OR debit_amount < 0)));
CREATE UNIQUE INDEX IF NOT EXISTS credit_settlement_reversal_once ON credit_settlement (tenant_id, reversal_of_id) WHERE reversal_of_id IS NOT NULL;
