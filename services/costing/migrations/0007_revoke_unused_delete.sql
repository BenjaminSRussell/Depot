-- The store never deletes from any costing table, so depot_app loses DELETE on
-- costing_version and item_cost. No table keeps DELETE. Skipped when the role does
-- not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'REVOKE DELETE ON costing_version, item_cost FROM depot_app';
  END IF;
END
$$;
