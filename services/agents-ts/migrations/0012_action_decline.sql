-- An action id on agent_action and the decline of an ungated (AUTO) action (DEPOT-298).
-- Migrations must run as the owner role, as in 0001.

DO $do$
BEGIN
  IF current_user = 'depot_app' THEN
    RAISE EXCEPTION 'agents migrations must run as the owner role, not depot_app';
  END IF;
END
$do$;

ALTER TABLE agent_action
  ADD COLUMN action_id uuid NOT NULL DEFAULT gen_random_uuid(),
  ADD COLUMN declined_at timestamptz,
  ADD COLUMN declined_by text,
  ADD COLUMN decline_reason text,
  ADD CONSTRAINT agent_action_decline_check CHECK (
    (declined_at IS NULL) = (declined_by IS NULL) AND (declined_at IS NULL OR decision = 'AUTO')
  );

-- A gated action keeps the id its approval request already carries. The owner is bound by FORCE row-level security
-- like everyone else, so it is lifted for the copy and restored before the transaction ends.
ALTER TABLE agent_action NO FORCE ROW LEVEL SECURITY;
ALTER TABLE approval_request NO FORCE ROW LEVEL SECURITY;
UPDATE agent_action a SET action_id = r.action_id
  FROM approval_request r
 WHERE a.tenant_id = r.tenant_id AND a.run_id = r.run_id AND a.seq = r.seq;
ALTER TABLE approval_request FORCE ROW LEVEL SECURITY;
ALTER TABLE agent_action FORCE ROW LEVEL SECURITY;

CREATE INDEX IF NOT EXISTS agent_action_by_action_id ON agent_action (tenant_id, run_id, action_id);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT UPDATE (declined_at, declined_by, decline_reason) ON agent_action TO depot_app';
  END IF;
END
$$;
