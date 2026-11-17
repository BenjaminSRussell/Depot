DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc_prediction_py') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO debezium_cdc_prediction_py', current_schema());
    EXECUTE 'GRANT SELECT ON outbox TO debezium_cdc_prediction_py';
  ELSE
    RAISE WARNING 'role debezium_cdc_prediction_py does not exist: the outbox-prediction-py connector cannot read outbox; create the role, then delete migration 0006 from schema_migrations and restart to re-run this grant';
  END IF;
END
$$;
