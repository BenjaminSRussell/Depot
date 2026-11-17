DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc_planning') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO debezium_cdc_planning', current_schema());
    EXECUTE 'GRANT SELECT ON outbox TO debezium_cdc_planning';
  ELSE
    RAISE WARNING 'role debezium_cdc_planning does not exist: the outbox-planning connector cannot read outbox; create the role, then delete migration 0033 from schema_migrations and restart to re-run this grant';
  END IF;
END
$$;
