-- #25: a site's own andon thresholds. A machine of a site uses its site's values when the site has a row here, and the
-- tenant's values (andon_setting) or the defaults otherwise. Ingest and the andon sweep both read them.

CREATE TABLE IF NOT EXISTS andon_site_setting (
  tenant_id uuid NOT NULL,
  site_id uuid NOT NULL,
  downtime_minutes integer NOT NULL CHECK (downtime_minutes BETWEEN 1 AND 240),
  clear_minutes integer NOT NULL CHECK (clear_minutes BETWEEN 0 AND 120),
  scrap_percent integer NOT NULL CHECK (scrap_percent BETWEEN 1 AND 100),
  scrap_min_count integer NOT NULL CHECK (scrap_min_count BETWEEN 1 AND 100000),
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, site_id)
);
ALTER TABLE andon_site_setting ENABLE ROW LEVEL SECURITY;
ALTER TABLE andon_site_setting FORCE ROW LEVEL SECURITY;
CREATE POLICY andon_site_setting_tenant_access ON andon_site_setting AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON andon_site_setting TO passage_app';
  END IF;
END
$$;
