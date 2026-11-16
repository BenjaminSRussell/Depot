-- #25 (FR-25.15, D7): the PI step library. A step template is one reusable process instruction (text, setpoint, data
-- request, calculation, inspection, material list, signature, sequence, machine readout, timer or branch) kept under a
-- code and versioned. A version is a draft until it is published; a published version is immutable (a trigger refuses any
-- change to it, apart from becoming superseded), and a new draft of the code supersedes it when that is published. PI
-- sheets and control recipes refer to a template by its code and version and carry a copy of it, so a later version never
-- changes a sheet or a recipe. A template may be filed under a PI category (0032).

CREATE TABLE IF NOT EXISTS pi_step_template (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_.-]{0,39}$'),
  version integer NOT NULL CHECK (version > 0),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  category_id uuid,
  kind text NOT NULL CHECK (kind IN ('instruction-text', 'process-parameter-setpoint', 'process-data-request', 'calculation',
    'inspection-characteristic', 'material-list', 'signature-request', 'sequence-definition', 'machine-readout', 'timer', 'branch')),
  spec jsonb NOT NULL CHECK (jsonb_typeof(spec) = 'object'),
  status text NOT NULL CHECK (status IN ('draft', 'published', 'superseded')),
  rev integer NOT NULL DEFAULT 1 CHECK (rev > 0),
  created_by text NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 200),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_by text NOT NULL CHECK (char_length(updated_by) BETWEEN 1 AND 200),
  updated_at timestamptz NOT NULL DEFAULT now(),
  published_by text,
  published_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code, version),
  FOREIGN KEY (tenant_id, category_id) REFERENCES pi_category (tenant_id, id),
  CHECK ((status = 'draft') = (published_at IS NULL))
);
-- One draft and one published version per code: a new version is cut from the published one only when no draft is open.
CREATE UNIQUE INDEX IF NOT EXISTS pi_step_template_one_draft ON pi_step_template (tenant_id, code) WHERE status = 'draft';
CREATE UNIQUE INDEX IF NOT EXISTS pi_step_template_one_published ON pi_step_template (tenant_id, code) WHERE status = 'published';
CREATE INDEX IF NOT EXISTS pi_step_template_list ON pi_step_template (tenant_id, code, version DESC);
CREATE INDEX IF NOT EXISTS pi_step_template_category ON pi_step_template (tenant_id, category_id) WHERE category_id IS NOT NULL;

ALTER TABLE pi_step_template ENABLE ROW LEVEL SECURITY;
ALTER TABLE pi_step_template FORCE ROW LEVEL SECURITY;
CREATE POLICY pi_step_template_tenant_access ON pi_step_template AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- A published version is immutable: the only change it takes is to become superseded (publishing its successor), and a
-- superseded one takes none. Whatever the service does, the database refuses it.
CREATE OR REPLACE FUNCTION pi_step_template_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.status = 'draft' THEN
    RETURN NEW;
  END IF;
  IF OLD.status = 'published' AND NEW.status = 'superseded'
     AND NEW.tenant_id = OLD.tenant_id AND NEW.id = OLD.id AND NEW.code = OLD.code AND NEW.version = OLD.version
     AND NEW.name = OLD.name AND NEW.category_id IS NOT DISTINCT FROM OLD.category_id AND NEW.kind = OLD.kind
     AND NEW.spec = OLD.spec AND NEW.published_by IS NOT DISTINCT FROM OLD.published_by AND NEW.published_at IS NOT DISTINCT FROM OLD.published_at THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'PI step template % version % is % and cannot be changed', OLD.code, OLD.version, OLD.status USING ERRCODE = '55000';
END
$$;
DROP TRIGGER IF EXISTS pi_step_template_immutable ON pi_step_template;
CREATE TRIGGER pi_step_template_immutable BEFORE UPDATE ON pi_step_template FOR EACH ROW EXECUTE FUNCTION pi_step_template_immutable();

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON pi_step_template TO passage_app';
  END IF;
END
$$;
