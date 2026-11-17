DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc_payables') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO debezium_cdc_payables', current_schema());
    EXECUTE 'GRANT SELECT ON outbox TO debezium_cdc_payables';
  ELSE
    RAISE WARNING 'role debezium_cdc_payables does not exist: the outbox-payables connector cannot read outbox; create the role, then delete migration 0026 from schema_migrations and restart to re-run this grant';
  END IF;
END
$$;
