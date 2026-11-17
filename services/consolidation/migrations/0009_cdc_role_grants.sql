DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc_consolidation') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO debezium_cdc_consolidation', current_schema());
    EXECUTE 'GRANT SELECT ON outbox TO debezium_cdc_consolidation';
  ELSE
    RAISE WARNING 'role debezium_cdc_consolidation does not exist: the outbox-consolidation connector cannot read outbox; create the role, then delete migration 0009 from schema_migrations and restart to re-run this grant';
  END IF;
END
$$;
