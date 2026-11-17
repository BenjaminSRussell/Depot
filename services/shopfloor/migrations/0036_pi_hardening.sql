-- #25 review fixes. (1) A published step template may become superseded and nothing else about it changes: authorship
-- (created_by, created_at) and the rev stay as they were, and it needs a successor version of the code. (2) The reading a
-- kiosk machine-readout step is given is recorded as a claim per recipe step, so the sealed record can name the edge node
-- and the time of the value it holds, and a value reported as read from the machine can be matched against it.

CREATE OR REPLACE FUNCTION pi_step_template_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.status = 'draft' THEN
    RETURN NEW;
  END IF;
  IF OLD.status = 'published' AND NEW.status = 'superseded'
     AND NEW.tenant_id = OLD.tenant_id AND NEW.id = OLD.id AND NEW.code = OLD.code AND NEW.version = OLD.version
     AND NEW.name = OLD.name AND NEW.category_id IS NOT DISTINCT FROM OLD.category_id AND NEW.kind = OLD.kind
     AND NEW.spec = OLD.spec AND NEW.published_by IS NOT DISTINCT FROM OLD.published_by AND NEW.published_at IS NOT DISTINCT FROM OLD.published_at
     AND NEW.created_by = OLD.created_by AND NEW.created_at = OLD.created_at AND NEW.rev = OLD.rev
     AND EXISTS (SELECT 1 FROM pi_step_template s WHERE s.tenant_id = OLD.tenant_id AND s.code = OLD.code AND s.version > OLD.version AND s.status IN ('draft', 'published')) THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'PI step template % version % is % and cannot be changed', OLD.code, OLD.version, OLD.status USING ERRCODE = '55000';
END
$$;

CREATE TABLE IF NOT EXISTS pi_readout_claim (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  recipe_id uuid NOT NULL,
  step_key text NOT NULL,
  edge_id uuid NOT NULL,
  node text NOT NULL,
  value numeric NOT NULL,
  read_at timestamptz NOT NULL,
  claimed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, recipe_id, step_key),
  FOREIGN KEY (tenant_id, recipe_id) REFERENCES control_recipe (tenant_id, id),
  FOREIGN KEY (tenant_id, edge_id) REFERENCES edge_node (tenant_id, id)
);
ALTER TABLE pi_readout_claim ENABLE ROW LEVEL SECURITY;
ALTER TABLE pi_readout_claim FORCE ROW LEVEL SECURITY;
CREATE POLICY pi_readout_claim_tenant_access ON pi_readout_claim AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON pi_readout_claim TO depot_app';
  END IF;
END
$$;
