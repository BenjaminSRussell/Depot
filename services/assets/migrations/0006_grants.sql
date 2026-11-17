-- Application role privileges. depot_app (no BYPASSRLS) works under the row-level
-- security policies. The outbox is insert-only (Debezium relays it).
-- asset_transaction, asset_addition, lease_modification, asset_count_session,
-- asset_count_line, asset_insurance_policy, capex_request_variant and
-- capex_request_decision are append-only. Tables the store updates, upserts or
-- row-locks (FOR UPDATE needs UPDATE) get SELECT, INSERT, UPDATE. Lease schedule
-- lines and depreciation run lines are replaced wholesale, so they also get DELETE.
-- Skipped when the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON asset_transaction, asset_addition, lease_modification, asset_count_session, asset_count_line, asset_insurance_policy, capex_request_variant, capex_request_decision TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON asset, asset_book, lease, depreciation_run, capex_request, idempotency_keys, asset_index_series, asset_index_value TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON depreciation_run_line TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON lease_schedule_line TO depot_app';
  END IF;
END
$$;
