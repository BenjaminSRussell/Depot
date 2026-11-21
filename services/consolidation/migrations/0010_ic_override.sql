-- B-40: a consolidation run that goes ahead over open intercompany mismatches is approved with consolidation.approve and a reason. The
-- approval is kept here, immutable, in the transaction that creates the run.
CREATE TABLE IF NOT EXISTS consolidation_ic_override (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  run_id uuid NOT NULL,
  group_id text NOT NULL,
  period text NOT NULL,
  open_mismatches integer NOT NULL CHECK (open_mismatches > 0),
  reason text NOT NULL CHECK (length(btrim(reason)) > 0 AND length(reason) <= 500),
  approved_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES consolidation_run (tenant_id, id)
);

CREATE TRIGGER consolidation_ic_override_immutable BEFORE UPDATE OR DELETE ON consolidation_ic_override
  FOR EACH ROW EXECUTE FUNCTION consolidation_reject_change();
CREATE TRIGGER consolidation_ic_override_no_truncate BEFORE TRUNCATE ON consolidation_ic_override
  FOR EACH STATEMENT EXECUTE FUNCTION consolidation_reject_change();
ALTER TABLE consolidation_ic_override ENABLE ROW LEVEL SECURITY;
ALTER TABLE consolidation_ic_override FORCE ROW LEVEL SECURITY;
CREATE POLICY consolidation_ic_override_tenant_access ON consolidation_ic_override AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON consolidation_ic_override TO depot_app';
  END IF;
END
$$;
