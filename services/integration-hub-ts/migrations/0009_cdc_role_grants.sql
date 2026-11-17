DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc_integration_hub_ts') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO debezium_cdc_integration_hub_ts', current_schema());
    EXECUTE 'GRANT SELECT ON outbox TO debezium_cdc_integration_hub_ts';
  ELSE
    RAISE WARNING 'role debezium_cdc_integration_hub_ts does not exist: the outbox-integration-hub-ts connector cannot read outbox; create the role, then delete migration 0009 from schema_migrations and restart to re-run this grant';
  END IF;
END
$$;
