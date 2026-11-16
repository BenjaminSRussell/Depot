-- Audit policies as a master (#500): a named, versioned rule that audit runs name by id instead of a typed key. The rule
-- itself (type and settings) is kept as the same spec the evaluate route reads, and every change to it is a new version,
-- because a violation is keyed by policy, version and document. Runs that still send their own spec stay valid.
CREATE TABLE IF NOT EXISTS audit_policy (
  tenant_id uuid NOT NULL,
  policy_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,31}$'),
  name text NOT NULL CHECK (btrim(name) <> '' AND length(name) <= 120),
  rule_type text NOT NULL CHECK (rule_type IN ('DUPLICATE', 'KEYWORD', 'LIST_SEARCH', 'SAMPLING', 'THRESHOLD', 'WEEKEND_HOLIDAY', 'AGGREGATE')),
  spec jsonb NOT NULL,
  active boolean NOT NULL DEFAULT true,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  -- row_version is the ETag: it grows with every change (a rename or a retirement too), while version grows only with the rule.
  row_version integer NOT NULL DEFAULT 1 CHECK (row_version >= 1),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, policy_id),
  UNIQUE (tenant_id, code)
);
ALTER TABLE audit_policy ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_policy FORCE ROW LEVEL SECURITY;
CREATE POLICY audit_policy_tenant_access ON audit_policy AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON audit_policy TO passage_app';
  END IF;
END $$;
