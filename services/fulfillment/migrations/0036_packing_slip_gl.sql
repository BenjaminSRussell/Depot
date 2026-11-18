-- B-14: a posted packing slip (and the reversing slip of a cancel) owes the
-- ledger its journal (Dr 1405 or 5000 / Cr 1400). The slip transaction writes one
-- PENDING row per slip; the poster (cmd/fulfillment, LEDGER_URL) claims due rows
-- with a lease, posts them under the key post:fulfillment:packingslip:<slip>:issue
-- (:cancel for a reversing slip) and records the ledger's entry id, or backs off
-- on an outage, or parks the row on a ledger refusal (a closed period).
CREATE TABLE IF NOT EXISTS packing_slip_gl (
  tenant_id uuid NOT NULL,
  slip_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'POSTED')),
  journal_entry_id text,
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  parked boolean NOT NULL DEFAULT false,
  last_error text CHECK (last_error IS NULL OR length(last_error) <= 500),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, slip_id),
  FOREIGN KEY (tenant_id, slip_id) REFERENCES packing_slip (tenant_id, id),
  CHECK ((status = 'POSTED') = (journal_entry_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS packing_slip_gl_pending_idx ON packing_slip_gl (tenant_id, next_attempt_at) WHERE status = 'PENDING' AND NOT parked;

ALTER TABLE packing_slip_gl ENABLE ROW LEVEL SECURITY;
ALTER TABLE packing_slip_gl FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS packing_slip_gl_tenant_access ON packing_slip_gl;
CREATE POLICY packing_slip_gl_tenant_access ON packing_slip_gl AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);
DROP POLICY IF EXISTS packing_slip_gl_legal_entity_scope ON packing_slip_gl;
CREATE POLICY packing_slip_gl_legal_entity_scope ON packing_slip_gl AS RESTRICTIVE
  USING (current_setting('depot.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));

-- The poster finds the tenants with a due row here, as the sales reporter does
-- (0035): the table is tenant-scoped and FORCE RLS binds its owner, so a trigger
-- keeps this cross-tenant index and only the SECURITY DEFINER function reads it.
CREATE TABLE IF NOT EXISTS packing_slip_gl_due (
  tenant_id uuid NOT NULL,
  slip_id uuid NOT NULL,
  next_attempt_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, slip_id)
);
CREATE INDEX IF NOT EXISTS packing_slip_gl_due_next_idx ON packing_slip_gl_due (next_attempt_at);
DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION packing_slip_gl_track_due() RETURNS trigger
      LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
    BEGIN
      IF NEW.status = 'PENDING' AND NOT NEW.parked THEN
        INSERT INTO packing_slip_gl_due (tenant_id, slip_id, next_attempt_at) VALUES (NEW.tenant_id, NEW.slip_id, NEW.next_attempt_at)
        ON CONFLICT (tenant_id, slip_id) DO UPDATE SET next_attempt_at = EXCLUDED.next_attempt_at;
      ELSE
        DELETE FROM packing_slip_gl_due WHERE tenant_id = NEW.tenant_id AND slip_id = NEW.slip_id;
      END IF;
      RETURN NEW;
    END
    $body$;
    CREATE OR REPLACE FUNCTION fulfillment_slip_gl_due_tenants() RETURNS SETOF uuid
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ SELECT DISTINCT tenant_id FROM packing_slip_gl_due WHERE next_attempt_at <= pg_catalog.now() $body$;
  $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION packing_slip_gl_track_due() FROM PUBLIC;
REVOKE ALL ON FUNCTION fulfillment_slip_gl_due_tenants() FROM PUBLIC;
DROP TRIGGER IF EXISTS packing_slip_gl_due ON packing_slip_gl;
CREATE TRIGGER packing_slip_gl_due AFTER INSERT OR UPDATE OF status, next_attempt_at, parked ON packing_slip_gl
  FOR EACH ROW EXECUTE FUNCTION packing_slip_gl_track_due();

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON packing_slip_gl TO depot_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION fulfillment_slip_gl_due_tenants() TO depot_app';
  END IF;
END
$$;
