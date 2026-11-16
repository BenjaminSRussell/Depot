DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc_party') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO debezium_cdc_party', current_schema());
    EXECUTE 'GRANT SELECT ON outbox TO debezium_cdc_party';
  ELSE
    RAISE WARNING 'role debezium_cdc_party does not exist: the outbox-party connector cannot read outbox; create the role, then delete migration 0020 from schema_migrations and restart to re-run this grant';
  END IF;
END
$$;
