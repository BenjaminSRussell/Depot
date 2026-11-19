CREATE TABLE IF NOT EXISTS need_to_know_setting (
  tenant_id uuid NOT NULL,
  setting_id uuid NOT NULL,
  legal_entity_id uuid,
  enabled boolean NOT NULL,
  reason text NOT NULL CHECK (char_length(btrim(reason)) BETWEEN 1 AND 500),
  changed_by text NOT NULL,
  changed_at timestamptz NOT NULL,
  version integer NOT NULL DEFAULT 2 CHECK (version >= 2),
  PRIMARY KEY (tenant_id, setting_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS need_to_know_setting_tenant ON need_to_know_setting (tenant_id) WHERE legal_entity_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS need_to_know_setting_legal_entity ON need_to_know_setting (tenant_id, legal_entity_id) WHERE legal_entity_id IS NOT NULL;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = 'need_to_know_setting' AND policyname = 'need_to_know_setting_tenant_access') THEN
    ALTER TABLE need_to_know_setting ENABLE ROW LEVEL SECURITY;
    ALTER TABLE need_to_know_setting FORCE ROW LEVEL SECURITY;
    CREATE POLICY need_to_know_setting_tenant_access ON need_to_know_setting AS PERMISSIVE
      USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);
    CREATE POLICY need_to_know_setting_legal_entity_scope ON need_to_know_setting AS RESTRICTIVE
      USING (legal_entity_id IS NULL OR current_setting('depot.legal_entity_scopes', true) = '*'
        OR legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));
  END IF;
END
$$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON need_to_know_setting TO depot_app';
  END IF;
END
$$;
