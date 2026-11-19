CREATE TABLE IF NOT EXISTS ai_data_policy_change (
  tenant_id uuid NOT NULL,
  version integer NOT NULL CHECK (version >= 2),
  legal_entity_id uuid,
  before_modules jsonb NOT NULL,
  after_modules jsonb NOT NULL,
  by_user text NOT NULL,
  loosening boolean NOT NULL,
  reason text CHECK (reason IS NULL OR length(reason) <= 500),
  changed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, version)
);
CREATE INDEX IF NOT EXISTS ai_data_policy_change_scope ON ai_data_policy_change (tenant_id, legal_entity_id, version DESC);
ALTER TABLE ai_data_policy_change ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_data_policy_change FORCE ROW LEVEL SECURITY;
CREATE POLICY ai_data_policy_change_tenant_access ON ai_data_policy_change AS PERMISSIVE
  USING (tenant_id = nullif(current_setting('depot.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('depot.tenant_id', true), '')::uuid);
CREATE POLICY ai_data_policy_change_legal_entity_scope ON ai_data_policy_change AS RESTRICTIVE
  USING (legal_entity_id IS NULL
         OR current_setting('depot.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON ai_data_policy_change TO depot_app';
  END IF;
END
$$;
