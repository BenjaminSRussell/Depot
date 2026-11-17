-- B-07a re-review N1 (Blocker) and N4: audit_anchor_record, the SECURITY DEFINER function of 0011, captured its
-- search_path FROM CURRENT, which leaves pg_temp out, and PostgreSQL then searches pg_temp FIRST for relations. A
-- caller (depot_app) could shadow audit_entry with a temp table, or a temp view calling a pg_temp function, and have
-- the function read it, or run that function, as the table owner. This replaces the function (0011 is merged and not
-- edited) in the repository's definer pattern (dbx.ServiceTokenReplayDDL, close 0004): search_path pinned to
-- pg_catalog, the migration's schema, then pg_temp last, and every relation in the body schema-qualified, the schema
-- name built with format('%I', current_schema()) when the migration runs.
--
-- N4: the function no longer takes the head the caller names on trust. Under the per-tenant append lock (the one
-- every append takes) it reads the chain's current head itself and refuses (SQLSTATE 40001) unless the caller's seq
-- is that head, so an anchor names the newest row at the moment it is written. The nightly job verifies, then
-- anchors, and verifies again when rows were appended in between (store.AnchorDay). The verdict columns stay the
-- caller's report: depot_app could still claim today's anchor early, with the then-current real head and a verdict of
-- its choosing; the real head is then anchored the next day (runbook audit-chain-verify-failure, "Anchors").
SET LOCAL lock_timeout = '5s';

DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION %1$I.audit_anchor_record(p_date date, p_head_seq bigint, p_entries bigint, p_verified boolean,
        p_breaks bigint, p_first_break_seq bigint, p_legacy_breaks bigint) RETURNS boolean
      LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
    DECLARE
      t uuid := NULLIF(pg_catalog.current_setting('depot.tenant_id', true), '')::uuid;
      today date := (pg_catalog.now() AT TIME ZONE 'UTC')::date;
      head_seq bigint;
      h text;
      n int;
    BEGIN
      IF t IS NULL THEN
        RAISE EXCEPTION 'audit_anchor_record: no tenant' USING ERRCODE = '42501';
      END IF;
      IF p_date IS NULL OR p_date > today OR p_date < today - 1 THEN
        RAISE EXCEPTION 'audit_anchor_record: anchor date %% is not today or yesterday (UTC)', p_date USING ERRCODE = '22023';
      END IF;
      PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('audit_entry:' || t::text, 0));
      SELECT e.seq, e.hash INTO head_seq, h FROM %1$I.audit_entry e WHERE e.tenant_id = t ORDER BY e.seq DESC LIMIT 1;
      IF head_seq IS NULL THEN
        head_seq := 0;
        h := pg_catalog.repeat('0', 64);
      END IF;
      IF p_head_seq IS DISTINCT FROM head_seq THEN
        RAISE EXCEPTION 'audit_anchor_record: seq %% is not the chain head %%', p_head_seq, head_seq USING ERRCODE = '40001';
      END IF;
      INSERT INTO %1$I.audit_anchor (tenant_id, anchor_date, head_seq, head_hash, entries, verified, breaks, first_break_seq, legacy_breaks)
        VALUES (t, p_date, head_seq, h, p_entries, p_verified, p_breaks, p_first_break_seq, p_legacy_breaks)
        ON CONFLICT (tenant_id, anchor_date) DO NOTHING;
      GET DIAGNOSTICS n = ROW_COUNT;
      RETURN n = 1;
    END
    $body$;
    REVOKE ALL ON FUNCTION %1$I.audit_anchor_record(date, bigint, bigint, boolean, bigint, bigint, bigint) FROM PUBLIC;
  $f$, current_schema());
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    -- depot_app is the only caller: the nightly job runs with the service's database credentials.
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.audit_anchor_record(date, bigint, bigint, boolean, bigint, bigint, bigint) TO depot_app', current_schema());
  END IF;
END
$do$;
