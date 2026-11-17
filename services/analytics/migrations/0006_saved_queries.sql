-- Saved explorer queries (#500): a named set of metrics and group-by dimensions with a look-back window, kept per user.
-- A schedule or the explorer picks one by name instead of a typed id. The range is relative (the last N days) so a saved
-- query stays useful and a scheduled one runs on fresh data. Metrics and dimensions are checked against the catalog when
-- the query is saved; a metric retired later is refused when the query is next planned.
CREATE TABLE IF NOT EXISTS saved_query (
  tenant_id uuid NOT NULL,
  query_id uuid NOT NULL,
  user_id text NOT NULL CHECK (btrim(user_id) <> ''),
  name text NOT NULL CHECK (btrim(name) <> '' AND length(name) <= 120),
  measures text[] NOT NULL CHECK (cardinality(measures) BETWEEN 1 AND 10),
  dimensions text[] NOT NULL DEFAULT '{}' CHECK (cardinality(dimensions) <= 8),
  lookback_days integer NOT NULL CHECK (lookback_days BETWEEN 1 AND 1826),
  created_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, query_id),
  UNIQUE (tenant_id, user_id, name)
);
CREATE INDEX IF NOT EXISTS saved_query_user ON saved_query (tenant_id, user_id, name);
ALTER TABLE saved_query ENABLE ROW LEVEL SECURITY;
ALTER TABLE saved_query FORCE ROW LEVEL SECURITY;
CREATE POLICY saved_query_tenant_access ON saved_query AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON saved_query TO depot_app';
  END IF;
END
$$;
