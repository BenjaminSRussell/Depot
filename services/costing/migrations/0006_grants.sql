-- Application role privileges. depot_app (no BYPASSRLS) works under the row-level
-- security policies. outbox, idempotency_keys, valuation_ledger, aging_snapshot,
-- valuation_category, aging_bucket_set, cost_transaction, cost_adjustment and
-- reconciliation_run are append-only (the last three also carry no-update triggers).
-- Skipped when the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON outbox, idempotency_keys, valuation_ledger, aging_snapshot, valuation_category, aging_bucket_set, cost_transaction, cost_adjustment, reconciliation_run TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON costing_version, item_cost TO depot_app';
  END IF;
END
$$;
