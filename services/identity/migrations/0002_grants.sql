DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT SELECT ON identity_tenants TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON identity_users, identity_legal_entities, identity_role_assignments TO depot_app';
  END IF;
END
$$;
