DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc_finreport') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO debezium_cdc_finreport', current_schema());
    EXECUTE 'GRANT SELECT ON outbox TO debezium_cdc_finreport';
  ELSE
    RAISE WARNING 'role debezium_cdc_finreport does not exist: the outbox-finreport connector cannot read outbox; create the role, then delete migration 0024 from schema_migrations and restart to re-run this grant';
  END IF;
END
$$;
