-- Application role privileges. depot_app (no BYPASSRLS) works under the row-level
-- security policies. The outbox is insert-only (Debezium relays it). trading_partner
-- is append-only. Tables the store updates, upserts or row-locks (FOR UPDATE needs
-- UPDATE) get SELECT, INSERT, UPDATE: interchange and message are locked and
-- updated, control_number_counter and idempotency_keys are upserted. Skipped when
-- the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON trading_partner TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON interchange, message, control_number_counter, idempotency_keys TO depot_app';
  END IF;
END
$$;
