DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON tax_transaction, tax_recommit_queue, tax_ledger_config TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON tax_settlement_run, outbox TO depot_app';
  END IF;
END
$$;
