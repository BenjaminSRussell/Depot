-- Durable state of a run executed as a Temporal workflow (FR-47.4, FR-47.8): the run input, the step state and the
-- pseudonym map live here, so a run that waits days for an approval resumes on any pod with its values restored.
-- agent_run_credential holds the bearer token a step acts with (the caller's at start, the approver's at decision)
-- so no token enters Temporal history; a row is read only while the token is unexpired and is deleted when the run ends.
-- Tenant row-level security as in 0001.

CREATE TABLE IF NOT EXISTS agent_run_state (
  tenant_id uuid NOT NULL,
  run_id text NOT NULL,
  input jsonb NOT NULL,
  state jsonb NOT NULL,
  pseudonyms jsonb NOT NULL DEFAULT '{"entries":[],"counters":{}}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, run_id)
);
ALTER TABLE agent_run_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_run_state FORCE ROW LEVEL SECURITY;
CREATE POLICY agent_run_state_tenant_access ON agent_run_state AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS agent_run_credential (
  tenant_id uuid NOT NULL,
  run_id text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('starter', 'approver')),
  user_id text NOT NULL,
  duties text NOT NULL,
  token text NOT NULL,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, run_id, kind)
);
ALTER TABLE agent_run_credential ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_run_credential FORCE ROW LEVEL SECURITY;
CREATE POLICY agent_run_credential_tenant_access ON agent_run_credential AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON agent_run_state TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON agent_run_credential TO passage_app';
  END IF;
END
$$;
