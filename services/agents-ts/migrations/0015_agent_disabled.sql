DO $do$
BEGIN
  IF current_user = 'depot_app' THEN
    RAISE EXCEPTION 'agents migrations must run as the owner role, not depot_app';
  END IF;
END
$do$;

CREATE TABLE IF NOT EXISTS agent_disabled (
  tenant_id uuid NOT NULL,
  agent_id text NOT NULL CHECK (agent_id ~ '^[a-z][a-z0-9_]{0,63}$'),
  disabled_by text NOT NULL,
  disabled_at timestamptz NOT NULL DEFAULT now(),
  reason text CHECK (char_length(reason) <= 500),
  PRIMARY KEY (tenant_id, agent_id)
);
ALTER TABLE agent_disabled ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_disabled FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS agent_disabled_tenant_access ON agent_disabled;
CREATE POLICY agent_disabled_tenant_access ON agent_disabled AS PERMISSIVE
  USING (tenant_id = nullif(current_setting('depot.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('depot.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS agent_disabled_change (
  tenant_id uuid NOT NULL,
  change_seq bigint GENERATED ALWAYS AS IDENTITY,
  agent_id text NOT NULL CHECK (agent_id ~ '^[a-z][a-z0-9_]{0,63}$'),
  disabled boolean NOT NULL,
  changed_by text NOT NULL,
  changed_at timestamptz NOT NULL DEFAULT now(),
  reason text CHECK (char_length(reason) <= 500),
  PRIMARY KEY (tenant_id, change_seq)
);
CREATE INDEX IF NOT EXISTS agent_disabled_change_agent ON agent_disabled_change (tenant_id, agent_id, change_seq DESC);
ALTER TABLE agent_disabled_change ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_disabled_change FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS agent_disabled_change_tenant_access ON agent_disabled_change;
CREATE POLICY agent_disabled_change_tenant_access ON agent_disabled_change AS PERMISSIVE
  USING (tenant_id = nullif(current_setting('depot.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('depot.tenant_id', true), '')::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON agent_disabled TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON agent_disabled_change TO depot_app';
  END IF;
END
$$;
