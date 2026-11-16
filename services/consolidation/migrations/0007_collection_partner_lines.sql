-- #40: the intercompany part of a collection per trading partner. A line says how much of one unit's account balance is with one other
-- unit of the group (the ledger's lines name the counterparty legal entity), translated like the collected line. It is a memo of the
-- collected balance, never added to the consolidated trial balance again. A run reads its intercompany sales, cost, receivable and
-- payable, and estimates the unrealised profit in inventory, per holder-subsidiary pair from them. Immutable like the collection; a
-- group spans legal entities, so there is no legal_entity_id column and tenant isolation is by tenant_id alone.
CREATE TABLE IF NOT EXISTS consolidation_collection_partner_line (
  tenant_id uuid NOT NULL,
  collection_id uuid NOT NULL,
  seq integer NOT NULL,
  unit_id uuid NOT NULL,
  counterparty_unit_id uuid NOT NULL,
  main_account text NOT NULL,
  consolidation_account text NOT NULL,
  account_type text NOT NULL CHECK (account_type IN ('balance_sheet', 'income')),
  role text NOT NULL,
  unit_currency text NOT NULL CHECK (unit_currency ~ '^[A-Z]{3}$'),
  unit_amount numeric(28,6) NOT NULL,
  rate numeric(20,10) NOT NULL CHECK (rate > 0),
  group_amount numeric(28,6) NOT NULL,
  PRIMARY KEY (tenant_id, collection_id, seq),
  FOREIGN KEY (tenant_id, collection_id) REFERENCES consolidation_collection (tenant_id, id),
  CHECK (unit_id <> counterparty_unit_id)
);

CREATE TRIGGER consolidation_collection_partner_line_immutable BEFORE UPDATE OR DELETE ON consolidation_collection_partner_line
  FOR EACH ROW EXECUTE FUNCTION consolidation_reject_change();
CREATE TRIGGER consolidation_collection_partner_line_no_truncate BEFORE TRUNCATE ON consolidation_collection_partner_line
  FOR EACH STATEMENT EXECUTE FUNCTION consolidation_reject_change();
ALTER TABLE consolidation_collection_partner_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE consolidation_collection_partner_line FORCE ROW LEVEL SECURITY;
CREATE POLICY consolidation_collection_partner_line_tenant_access ON consolidation_collection_partner_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON consolidation_collection_partner_line TO passage_app';
  END IF;
END
$$;

-- The buyer's side of intercompany goods (#40): the accounts it books IC purchases and the cost of those sold on to, and the account that
-- holds them.
ALTER TABLE consolidation_account_map DROP CONSTRAINT IF EXISTS consolidation_account_map_role_check;
ALTER TABLE consolidation_account_map ADD CONSTRAINT consolidation_account_map_role_check
  CHECK (role IS NULL OR role IN ('asset', 'liability', 'equity', 'ic_sales', 'ic_cost', 'ic_receivable', 'ic_payable', 'dividend_paid',
    'ic_purchases', 'ic_cogs', 'ic_inventory'));
