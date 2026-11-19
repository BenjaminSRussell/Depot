CREATE TABLE IF NOT EXISTS data_designation (
  tenant_id uuid NOT NULL,
  designation_id uuid NOT NULL,
  scope text NOT NULL CHECK (scope IN ('TENANT', 'LEGAL_ENTITY', 'RECORD')),
  legal_entity_id uuid,
  entity_type text CHECK (entity_type IS NULL OR btrim(entity_type) <> ''),
  record_id text CHECK (record_id IS NULL OR btrim(record_id) <> ''),
  class text NOT NULL CHECK (class IN ('CUI')),
  category text CHECK (category IS NULL OR char_length(btrim(category)) BETWEEN 1 AND 100),
  reason text NOT NULL CHECK (char_length(btrim(reason)) BETWEEN 1 AND 500),
  designated_by text NOT NULL,
  designated_at timestamptz NOT NULL,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  PRIMARY KEY (tenant_id, designation_id),
  CHECK (
    (scope = 'TENANT' AND legal_entity_id IS NULL AND entity_type IS NULL AND record_id IS NULL)
    OR (scope = 'LEGAL_ENTITY' AND legal_entity_id IS NOT NULL AND entity_type IS NULL AND record_id IS NULL)
    OR (scope = 'RECORD' AND legal_entity_id IS NOT NULL AND entity_type IS NOT NULL AND record_id IS NOT NULL)
  )
);
CREATE UNIQUE INDEX IF NOT EXISTS data_designation_tenant ON data_designation (tenant_id) WHERE scope = 'TENANT';
CREATE UNIQUE INDEX IF NOT EXISTS data_designation_legal_entity ON data_designation (tenant_id, legal_entity_id) WHERE scope = 'LEGAL_ENTITY';
CREATE UNIQUE INDEX IF NOT EXISTS data_designation_record ON data_designation (tenant_id, entity_type, record_id) WHERE scope = 'RECORD';

CREATE TABLE IF NOT EXISTS audit_event (
  tenant_id uuid NOT NULL,
  audit_id uuid NOT NULL,
  entity_type text NOT NULL CHECK (btrim(entity_type) <> ''),
  entity_id uuid NOT NULL,
  action text NOT NULL CHECK (btrim(action) <> ''),
  actor text NOT NULL,
  occurred_at timestamptz NOT NULL,
  reason text,
  before jsonb,
  after jsonb,
  PRIMARY KEY (tenant_id, audit_id)
);
CREATE INDEX IF NOT EXISTS audit_event_entity ON audit_event (tenant_id, entity_type, entity_id, occurred_at DESC);

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = 'data_designation' AND policyname = 'data_designation_tenant_access') THEN
    ALTER TABLE data_designation ENABLE ROW LEVEL SECURITY;
    ALTER TABLE data_designation FORCE ROW LEVEL SECURITY;
    CREATE POLICY data_designation_tenant_access ON data_designation AS PERMISSIVE
      USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);
    CREATE POLICY data_designation_legal_entity_scope ON data_designation AS RESTRICTIVE
      USING (legal_entity_id IS NULL OR current_setting('depot.legal_entity_scopes', true) = '*'
        OR legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = 'audit_event' AND policyname = 'audit_event_tenant_access') THEN
    ALTER TABLE audit_event ENABLE ROW LEVEL SECURITY;
    ALTER TABLE audit_event FORCE ROW LEVEL SECURITY;
    CREATE POLICY audit_event_tenant_access ON audit_event AS PERMISSIVE
      USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);
  END IF;
END
$$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON data_designation TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON audit_event TO depot_app';
  END IF;
END
$$;
