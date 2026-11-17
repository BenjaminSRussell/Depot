DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc_costacct') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO debezium_cdc_costacct', current_schema());
    EXECUTE 'GRANT SELECT ON outbox TO debezium_cdc_costacct';
  ELSE
    RAISE WARNING 'role debezium_cdc_costacct does not exist: the outbox-costacct connector cannot read outbox; create the role, then delete migration 0008 from schema_migrations and restart to re-run this grant';
  END IF;
END
$$;
