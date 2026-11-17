-- B-07a second re-review R2-1: audit_anchor_record (0013) read the chain head under the caller's
-- depot.legal_entity_scopes. Deployed, the function owner is not a superuser, so forced row-level security applies
-- to it, and a caller that set the scope to '' (or one legal entity) saw an older row as the head and anchored it.
-- The chain is one per tenant, so the head read must see every legal entity: the function now sets the scope to '*'
-- for its own reads and writes and puts the caller's value back before it returns. 0013 is merged and not edited.
--
-- The tenant comes from depot.tenant_id as before. It cannot be widened the same way: the tenant policy compares
-- tenant_id with that one UUID (a list or '*' fails the uuid cast), so the function sees one tenant's chain, the one
-- the calling transaction is bound to, as every query of depot_app is.
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
      caller_scopes text := pg_catalog.current_setting('depot.legal_entity_scopes', true);
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
      -- The whole tenant chain, whatever legal-entity scope the caller runs under.
      PERFORM pg_catalog.set_config('depot.legal_entity_scopes', '*', true);
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
      PERFORM pg_catalog.set_config('depot.legal_entity_scopes', COALESCE(caller_scopes, ''), true);
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
