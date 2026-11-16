-- Attribute outcomes become a master (#500): a code and a name per outcome, which test groups list as passing outcomes
-- and results reference. Results and test groups keep storing the outcome id, so every row written before this
-- migration stays valid; each id they already mention is registered here under a generated code that a person can rename.
CREATE TABLE IF NOT EXISTS quality_outcome (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,31}$'),
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  active boolean NOT NULL DEFAULT true,
  version int NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE quality_outcome ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_outcome FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_outcome_tenant_access ON quality_outcome AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Register every outcome id the results and test groups already mention. It reads every tenant, so it runs only under a
-- role that bypasses row-level security; otherwise it is skipped with a notice and outcomes are registered by hand (rows
-- that name an unregistered id stay valid). Only values shaped like a UUID are cast: older rows may hold other text.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND (rolsuper OR rolbypassrls)) THEN
    RAISE NOTICE 'quality outcome backfill skipped: role % does not bypass row-level security', current_user;
    RETURN;
  END IF;
  INSERT INTO quality_outcome (tenant_id, id, code, name)
  SELECT tenant_id, id, 'OUT-' || upper(right(replace(id::text, '-', ''), 8)), 'Outcome ' || upper(right(replace(id::text, '-', ''), 8))
  FROM (
    SELECT tenant_id, outcome_id AS id FROM quality_result WHERE outcome_id IS NOT NULL
    UNION
    SELECT g.tenant_id, o::uuid FROM quality_test_group g,
      LATERAL jsonb_array_elements(g.tests) AS t(v),
      LATERAL jsonb_array_elements_text(CASE WHEN jsonb_typeof(t.v -> 'passingOutcomeIds') = 'array' THEN t.v -> 'passingOutcomeIds' ELSE '[]'::jsonb END) AS o
    WHERE o ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  ) referenced
  ON CONFLICT DO NOTHING;
END
$$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON quality_outcome TO passage_app';
  END IF;
END
$$;
