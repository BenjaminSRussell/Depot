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
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);

-- A freight bill reads FB-000042 instead of its id. Numbers count per tenant from a counter row the insert trigger takes
-- under a row lock (as quality orders do), so one tenant's numbers say nothing about another's volume.
ALTER TABLE freight_bill ADD COLUMN IF NOT EXISTS number text;

CREATE TABLE IF NOT EXISTS freight_bill_counter (
  tenant_id uuid PRIMARY KEY,
  last_number bigint NOT NULL CHECK (last_number > 0)
);
ALTER TABLE freight_bill_counter ENABLE ROW LEVEL SECURITY;
ALTER TABLE freight_bill_counter FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS freight_bill_counter_tenant_access ON freight_bill_counter;
CREATE POLICY freight_bill_counter_tenant_access ON freight_bill_counter AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);

CREATE OR REPLACE FUNCTION freight_bill_assign_number() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  n bigint;
BEGIN
  IF NEW.number IS NULL THEN
    INSERT INTO freight_bill_counter AS c (tenant_id, last_number) VALUES (NEW.tenant_id, 1)
      ON CONFLICT (tenant_id) DO UPDATE SET last_number = c.last_number + 1
      RETURNING last_number INTO n;
    NEW.number := 'FB-' || lpad(n::text, greatest(6, length(n::text)), '0');
  END IF;
  RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS freight_bill_number ON freight_bill;
CREATE TRIGGER freight_bill_number BEFORE INSERT ON freight_bill FOR EACH ROW EXECUTE FUNCTION freight_bill_assign_number();

-- Backfill: bills without a number are numbered per tenant in creation order, after the tenant's counter. It reads every
-- tenant, so it runs only under a role that bypasses row-level security; otherwise it is skipped with a notice and the
-- older bills keep no number.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND (rolsuper OR rolbypassrls)) THEN
    RAISE NOTICE 'freight bill number backfill skipped: role % does not bypass row-level security', current_user;
    RETURN;
  END IF;
  WITH numbered AS (
    SELECT b.tenant_id, b.id,
           coalesce(c.last_number, 0) + row_number() OVER (PARTITION BY b.tenant_id ORDER BY b.created_at, b.id) AS n
    FROM freight_bill b LEFT JOIN freight_bill_counter c ON c.tenant_id = b.tenant_id
    WHERE b.number IS NULL
  ), assigned AS (
    UPDATE freight_bill b SET number = 'FB-' || lpad(numbered.n::text, greatest(6, length(numbered.n::text)), '0')
    FROM numbered WHERE b.tenant_id = numbered.tenant_id AND b.id = numbered.id
    RETURNING numbered.tenant_id, numbered.n
  )
  INSERT INTO freight_bill_counter (tenant_id, last_number)
    SELECT tenant_id, max(n) FROM assigned GROUP BY tenant_id
    ON CONFLICT (tenant_id) DO UPDATE SET last_number = greatest(freight_bill_counter.last_number, excluded.last_number);
END
$$;
CREATE UNIQUE INDEX IF NOT EXISTS freight_bill_number_key ON freight_bill (tenant_id, number) WHERE number IS NOT NULL;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON carrier_service TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON freight_bill_counter TO depot_app';
  END IF;
END $$;
