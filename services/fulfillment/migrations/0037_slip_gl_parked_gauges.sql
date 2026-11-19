-- R-2: the pending and parked slip journals are counted process-wide for /metrics.
-- packing_slip_gl_due (0036) now tracks every open (PENDING) row with its parked
-- flag, so a SECURITY DEFINER function can count both without a tenant; the
-- poster's tenant list excludes parked rows as before.
ALTER TABLE packing_slip_gl_due ADD COLUMN IF NOT EXISTS parked boolean NOT NULL DEFAULT false;

DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION packing_slip_gl_track_due() RETURNS trigger
      LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
    BEGIN
      IF NEW.status = 'PENDING' THEN
        INSERT INTO packing_slip_gl_due (tenant_id, slip_id, next_attempt_at, parked) VALUES (NEW.tenant_id, NEW.slip_id, NEW.next_attempt_at, NEW.parked)
        ON CONFLICT (tenant_id, slip_id) DO UPDATE SET next_attempt_at = EXCLUDED.next_attempt_at, parked = EXCLUDED.parked;
      ELSE
        DELETE FROM packing_slip_gl_due WHERE tenant_id = NEW.tenant_id AND slip_id = NEW.slip_id;
      END IF;
      RETURN NEW;
    END
    $body$;
    CREATE OR REPLACE FUNCTION fulfillment_slip_gl_due_tenants() RETURNS SETOF uuid
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ SELECT DISTINCT tenant_id FROM packing_slip_gl_due WHERE next_attempt_at <= pg_catalog.now() AND NOT parked $body$;
    CREATE OR REPLACE FUNCTION fulfillment_slip_gl_counts(OUT pending_rows bigint, OUT parked_rows bigint)
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ SELECT count(*) FILTER (WHERE NOT parked), count(*) FILTER (WHERE parked) FROM packing_slip_gl_due $body$;
  $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION fulfillment_slip_gl_counts() FROM PUBLIC;

-- Rows already open before this migration are not in packing_slip_gl_due when
-- parked. The table forces row-level security and the migration sets no tenant, so
-- the backfill runs with the policy lifted for the owner and forced again in the
-- same transaction (as 0018 does).
ALTER TABLE packing_slip_gl NO FORCE ROW LEVEL SECURITY;
INSERT INTO packing_slip_gl_due (tenant_id, slip_id, next_attempt_at, parked)
  SELECT tenant_id, slip_id, next_attempt_at, parked FROM packing_slip_gl WHERE status = 'PENDING'
  ON CONFLICT (tenant_id, slip_id) DO UPDATE SET parked = EXCLUDED.parked;
ALTER TABLE packing_slip_gl FORCE ROW LEVEL SECURITY;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION fulfillment_slip_gl_counts() TO depot_app';
  END IF;
END
$$;
