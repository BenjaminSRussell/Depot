CREATE TABLE IF NOT EXISTS ai_data_access (
  tenant_id uuid NOT NULL,
  seq bigserial NOT NULL,
  id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('READ', 'EXPORT')),
  legal_entity_id uuid,
  actor text NOT NULL CHECK (length(actor) BETWEEN 1 AND 320),
  user_id text NOT NULL CHECK (length(user_id) BETWEEN 1 AND 256),
  agent_id text NOT NULL DEFAULT '' CHECK (length(agent_id) <= 64),
  run_id text NOT NULL CHECK (length(run_id) BETWEEN 1 AND 128),
  tool text NOT NULL CHECK (length(tool) BETWEEN 1 AND 128),
  entity_type text NOT NULL CHECK (length(entity_type) BETWEEN 1 AND 128),
  record_ids text[] NOT NULL CHECK (
    cardinality(record_ids) <= 200
    AND length(array_to_string(record_ids, ',')) <= 25800
    AND array_to_string(record_ids, ',') ~ '^[A-Za-z0-9_.:,-]*$'),
  record_classes text[] NOT NULL CHECK (cardinality(record_classes) = cardinality(record_ids)),
  highest_class text NOT NULL CHECK (highest_class IN ('P0', 'P1', 'P2', 'P3')),
  route_id text NOT NULL CHECK (length(route_id) BETWEEN 1 AND 128),
  region text NOT NULL DEFAULT '' CHECK (length(region) <= 64),
  redaction_counts jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(redaction_counts) = 'object'),
  excluded_counts jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(excluded_counts) = 'object'),
  policy_version integer NOT NULL CHECK (policy_version >= 1),
  decision text NOT NULL CHECK (decision IN ('ALLOWED', 'REDACTED', 'WITHHELD', 'BLOCKED')),
  reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 128),
  trace_id text NOT NULL CHECK (length(trace_id) BETWEEN 1 AND 128),
  occurred_at timestamptz NOT NULL,
  prev_hash char(64) NOT NULL,
  hash char(64) NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, seq),
  UNIQUE (tenant_id, hash)
);
ALTER TABLE ai_data_access ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_data_access FORCE ROW LEVEL SECURITY;
CREATE POLICY ai_data_access_tenant_access ON ai_data_access AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);
CREATE POLICY ai_data_access_legal_entity_scope ON ai_data_access AS RESTRICTIVE
  USING (legal_entity_id IS NULL
         OR current_setting('depot.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = any(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));
CREATE INDEX IF NOT EXISTS ai_data_access_time ON ai_data_access (tenant_id, occurred_at);
CREATE INDEX IF NOT EXISTS ai_data_access_user ON ai_data_access (tenant_id, user_id, seq);

CREATE FUNCTION ai_data_access_immutable() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'ai_data_access is append-only';
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER ai_data_access_no_mutation
  BEFORE UPDATE OR DELETE ON ai_data_access
  FOR EACH ROW EXECUTE PROCEDURE ai_data_access_immutable();
CREATE TRIGGER ai_data_access_no_truncate
  BEFORE TRUNCATE ON ai_data_access
  FOR EACH STATEMENT EXECUTE PROCEDURE ai_data_access_immutable();

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON ai_data_access TO depot_app';
    EXECUTE 'GRANT USAGE ON SEQUENCE ai_data_access_seq_seq TO depot_app';
  END IF;
END
$$;
