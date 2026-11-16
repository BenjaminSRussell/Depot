-- #25 (FR-25.18): PI execution records. When a kiosk finishes a PI sheet (the control recipe is reported completed) the
-- service seals a record of the run: one header and one row per recipe step in document order, each holding what was
-- entered or read, who signed and with what meaning, when, and the deviations. The steps form a SHA-256 hash chain (each
-- hash covers the step and the hash before it) and the header's record hash covers the head of the chain, so a change to any
-- row is detectable by recomputing it (POST pi-executions/{id}:verify). The tables are append only: a trigger refuses
-- UPDATE and DELETE (and TRUNCATE) for everyone, the application role included. The PDF/A archive is not produced here;
-- the record hash is what an archive copy is checked against (decision #512).

CREATE TABLE IF NOT EXISTS pi_execution (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  recipe_id uuid NOT NULL,
  order_id uuid NOT NULL,
  resource_id uuid NOT NULL,
  pi_sheet_id uuid,
  sheet_code text,
  sheet_version integer,
  executed_by text NOT NULL CHECK (char_length(executed_by) BETWEEN 1 AND 200),
  started_at timestamptz NOT NULL,
  completed_at timestamptz NOT NULL,
  step_count integer NOT NULL CHECK (step_count >= 0),
  deviation_count integer NOT NULL CHECK (deviation_count >= 0),
  head_hash text NOT NULL CHECK (head_hash ~ '^[0-9a-f]{64}$'),
  record_hash text NOT NULL CHECK (record_hash ~ '^[0-9a-f]{64}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, recipe_id),
  FOREIGN KEY (tenant_id, recipe_id) REFERENCES control_recipe (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS pi_execution_order ON pi_execution (tenant_id, order_id, completed_at DESC);

CREATE TABLE IF NOT EXISTS pi_execution_step (
  tenant_id uuid NOT NULL,
  execution_id uuid NOT NULL,
  seq integer NOT NULL CHECK (seq > 0),
  step_key text NOT NULL,
  status text NOT NULL CHECK (status IN ('recorded', 'acknowledged', 'skipped', 'missing')),
  deviation boolean NOT NULL,
  body jsonb NOT NULL,
  prev_hash text NOT NULL CHECK (prev_hash ~ '^[0-9a-f]{64}$'),
  hash text NOT NULL CHECK (hash ~ '^[0-9a-f]{64}$'),
  PRIMARY KEY (tenant_id, execution_id, seq),
  FOREIGN KEY (tenant_id, execution_id) REFERENCES pi_execution (tenant_id, id)
);

ALTER TABLE pi_execution ENABLE ROW LEVEL SECURITY;
ALTER TABLE pi_execution FORCE ROW LEVEL SECURITY;
CREATE POLICY pi_execution_tenant_access ON pi_execution AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
ALTER TABLE pi_execution_step ENABLE ROW LEVEL SECURITY;
ALTER TABLE pi_execution_step FORCE ROW LEVEL SECURITY;
CREATE POLICY pi_execution_step_tenant_access ON pi_execution_step AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE OR REPLACE FUNCTION pi_execution_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'a PI execution record is append only: % is refused', TG_OP USING ERRCODE = '55000';
END
$$;
DROP TRIGGER IF EXISTS pi_execution_append_only ON pi_execution;
CREATE TRIGGER pi_execution_append_only BEFORE UPDATE OR DELETE ON pi_execution FOR EACH ROW EXECUTE FUNCTION pi_execution_append_only();
DROP TRIGGER IF EXISTS pi_execution_step_append_only ON pi_execution_step;
CREATE TRIGGER pi_execution_step_append_only BEFORE UPDATE OR DELETE ON pi_execution_step FOR EACH ROW EXECUTE FUNCTION pi_execution_append_only();
DROP TRIGGER IF EXISTS pi_execution_truncate ON pi_execution;
CREATE TRIGGER pi_execution_truncate BEFORE TRUNCATE ON pi_execution FOR EACH STATEMENT EXECUTE FUNCTION pi_execution_append_only();
DROP TRIGGER IF EXISTS pi_execution_step_truncate ON pi_execution_step;
CREATE TRIGGER pi_execution_step_truncate BEFORE TRUNCATE ON pi_execution_step FOR EACH STATEMENT EXECUTE FUNCTION pi_execution_append_only();

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON pi_execution, pi_execution_step TO passage_app';
  END IF;
END
$$;
