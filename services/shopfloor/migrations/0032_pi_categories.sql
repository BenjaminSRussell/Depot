-- #25 (FR-25.15, D7): the PI category master. A category groups reusable PI step templates (the step library, migration
-- 0033) so a designer finds "Weighing" or "Cleaning" steps together. A category has a code that never changes, a name,
-- an optional description and a status; an inactive category takes no new templates but keeps those it has. Changes carry
-- the version read (If-Match). New table, no legal entity: like every shop floor master it belongs to the tenant.

CREATE TABLE IF NOT EXISTS pi_category (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_.-]{0,39}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
  active boolean NOT NULL DEFAULT true,
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by text NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 200),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);

ALTER TABLE pi_category ENABLE ROW LEVEL SECURITY;
ALTER TABLE pi_category FORCE ROW LEVEL SECURITY;
CREATE POLICY pi_category_tenant_access ON pi_category AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON pi_category TO passage_app';
  END IF;
END
$$;
