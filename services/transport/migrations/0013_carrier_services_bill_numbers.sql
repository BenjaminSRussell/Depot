-- Carrier services as a master the rate card and the lookups read (#500), and a readable number on
-- every freight bill. A service belongs to one carrier party; rate cards keep naming it by id, and
-- cards written before this migration stay valid (service_id is not a foreign key).
CREATE TABLE IF NOT EXISTS carrier_service (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  carrier_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  mode text NOT NULL CHECK (mode IN ('TL', 'LTL', 'PARCEL', 'INTERMODAL')),
  active boolean NOT NULL DEFAULT true,
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, carrier_id, code),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS carrier_service_carrier ON carrier_service (tenant_id, carrier_id, code);
ALTER TABLE carrier_service ENABLE ROW LEVEL SECURITY;
ALTER TABLE carrier_service FORCE ROW LEVEL SECURITY;
CREATE POLICY carrier_service_tenant_access ON carrier_service AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- A freight bill reads FB-000042 instead of its id. The sequence is shared across tenants (numbers of
-- one tenant have gaps), which keeps the number unique without a per-tenant counter row.
ALTER TABLE freight_bill ADD COLUMN IF NOT EXISTS bill_no bigint GENERATED ALWAYS AS IDENTITY;
CREATE INDEX IF NOT EXISTS freight_bill_number ON freight_bill (tenant_id, bill_no);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON carrier_service TO passage_app';
  END IF;
END $$;
