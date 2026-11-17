-- Application role privileges. depot_app (no BYPASSRLS) works under the row-level
-- security policies. outbox, idempotency_keys, service_sla_profile, service_case and
-- service_entitlement_usage are append-only. service_agreement needs UPDATE because
-- posting a work order locks it with SELECT ... FOR UPDATE. Skipped when the role
-- does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON outbox, idempotency_keys, service_sla_profile, service_case, service_entitlement_usage TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON service_agreement TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON service_number_counter, service_work_order TO depot_app';
  END IF;
END
$$;
