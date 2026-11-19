-- On-demand briefings (DEPOT-285). One row per generation request of one user; RLS is tenant-only, so every read
-- and write filters user_id in SQL. A partial unique index allows one RUNNING row per user (AGT-0231).

CREATE TABLE IF NOT EXISTS agent_briefing (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  user_id text NOT NULL,
  status text NOT NULL CHECK (status IN ('RUNNING', 'READY', 'FAILED')),
  narrative boolean NOT NULL DEFAULT false,
  content jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  read_at timestamptz,
  PRIMARY KEY (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS agent_briefing_one_running ON agent_briefing (tenant_id, user_id) WHERE status = 'RUNNING';
CREATE INDEX IF NOT EXISTS agent_briefing_user_recent ON agent_briefing (tenant_id, user_id, created_at DESC, id DESC);
ALTER TABLE agent_briefing ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_briefing FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS agent_briefing_tenant_access ON agent_briefing;
CREATE POLICY agent_briefing_tenant_access ON agent_briefing AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON agent_briefing TO depot_app';
  END IF;
END
$$;
