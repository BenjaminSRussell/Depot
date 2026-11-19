DO $do$
BEGIN
  IF current_user = 'depot_app' THEN
    RAISE EXCEPTION 'agents migrations must run as the owner role, not depot_app';
  END IF;
END
$do$;

CREATE TABLE IF NOT EXISTS agent_budget_change (
  tenant_id uuid NOT NULL,
  change_seq bigint GENERATED ALWAYS AS IDENTITY,
  agent_id text NOT NULL CHECK (agent_id ~ '^[a-z][a-z0-9_]{0,63}$'),
  limit_in bigint NOT NULL CHECK (limit_in >= 0),
  limit_out bigint NOT NULL CHECK (limit_out >= 0),
  previous_limit_in bigint CHECK (previous_limit_in >= 0),
  previous_limit_out bigint CHECK (previous_limit_out >= 0),
  changed_by text NOT NULL,
  changed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, change_seq)
);
CREATE INDEX IF NOT EXISTS agent_budget_change_agent ON agent_budget_change (tenant_id, agent_id, change_seq DESC);
ALTER TABLE agent_budget_change ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_budget_change FORCE ROW LEVEL SECURITY;
CREATE POLICY agent_budget_change_tenant_access ON agent_budget_change AS PERMISSIVE
  USING (tenant_id = nullif(current_setting('depot.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('depot.tenant_id', true), '')::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON agent_budget_change TO depot_app';
  END IF;
END
$$;
