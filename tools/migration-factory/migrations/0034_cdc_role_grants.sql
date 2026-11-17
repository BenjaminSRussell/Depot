DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc_migration_factory') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO debezium_cdc_migration_factory', current_schema());
    EXECUTE 'GRANT SELECT ON outbox TO debezium_cdc_migration_factory';
  ELSE
    RAISE WARNING 'role debezium_cdc_migration_factory does not exist: the outbox-migration-factory connector cannot read outbox; create the role, then delete migration 0034 from schema_migrations and restart to re-run this grant';
  END IF;
END
$$;
