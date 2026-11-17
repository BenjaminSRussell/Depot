-- Application role privileges. depot_app (no BYPASSRLS) works under the row-level
-- security policies. The outbox is insert-only (Debezium relays it); bank_statement,
-- bank_statement_line, cash_forecast_entry, treasury_loan and
-- treasury_loan_schedule_line are append-only. Skipped when the role does not exist
-- (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON bank_statement, bank_statement_line, cash_forecast_entry, treasury_loan, treasury_loan_schedule_line TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON treasury_facility, idempotency_keys, bank_account, bank_check, treasury_facility_instrument TO depot_app';
  END IF;
END
$$;
