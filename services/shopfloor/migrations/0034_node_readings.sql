-- #25: the latest value the edge connector read from a machine node, so a kiosk PI sheet's machine-readout step can fetch it.
-- The connector reports readings through the same node and type vocabulary as its setpoint readback (OPC-UA data types).
-- One row per edge node and node id, replaced by a newer reading; the kiosk reads the freshest row for a node.

CREATE TABLE IF NOT EXISTS node_reading (
  tenant_id uuid NOT NULL,
  edge_id uuid NOT NULL,
  node text NOT NULL CHECK (char_length(node) BETWEEN 1 AND 300),
  value numeric NOT NULL,
  node_type text NOT NULL DEFAULT '' CHECK (char_length(node_type) <= 20),
  read_at timestamptz NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, edge_id, node),
  FOREIGN KEY (tenant_id, edge_id) REFERENCES edge_node (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS node_reading_node ON node_reading (tenant_id, node, read_at DESC);

ALTER TABLE node_reading ENABLE ROW LEVEL SECURITY;
ALTER TABLE node_reading FORCE ROW LEVEL SECURITY;
CREATE POLICY node_reading_tenant_access ON node_reading AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON node_reading TO passage_app';
  END IF;
END
$$;
