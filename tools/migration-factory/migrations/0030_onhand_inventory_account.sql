-- #49: the inventory GL account an onhand run named for its BR-49.3 reconciliation is kept on the run, so the on-hand read-back
-- after the opening load can compare the value the inventory ledger reports per inventory account with the value the factory
-- loaded for that account.
alter table pack_run add column if not exists inventory_account text;
alter table pack_run drop constraint if exists pack_run_inventory_account_onhand_only;
alter table pack_run add constraint pack_run_inventory_account_onhand_only
  check (inventory_account is null or (pack = 'onhand' and inventory_account <> ''));
