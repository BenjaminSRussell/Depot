-- B-07a review fixes, on top of 0009 (which is already merged, so it is not edited).
--
-- 1. depot.ctx messages are signed (platform/go/auditctx): a captured change whose message does not verify is recorded
--    as actor kind 'system' with the flag 'unattributed-bad-signature'. The flag check admits it; 0012 validates it.
-- 2. cdc_tx_context keeps whether a message verified (verified, rejection) and its source commit time (committed_at):
--    the consumer joins a message to a change only within an hour of commit time, so a message of an older
--    transaction that reused the 32-bit transaction id never attaches. Rows from before this migration are unverified
--    and out of every window ('-infinity'), so they attribute nothing.
-- 3. depot_app cannot plant anchors: it loses INSERT on audit_anchor, and anchors are written only through
--    audit_anchor_record(), which reads the head hash from the chain itself and refuses any date but today or yesterday
--    (UTC). A planted anchor would block the real one (one per tenant per day) and fail verification for good.
SET LOCAL lock_timeout = '5s';

ALTER TABLE audit_entry DROP CONSTRAINT IF EXISTS audit_entry_flag_check;
-- An unattributed flag ('unattributed': no depot.ctx message; 'unattributed-bad-signature': a message that is not
-- authenticated) is exactly the system actor.
ALTER TABLE audit_entry ADD CONSTRAINT audit_entry_flag_check
  CHECK (flag IN ('', 'unattributed', 'unattributed-bad-signature') AND (flag <> '') = (actor_kind = 'system')) NOT VALID;

ALTER TABLE cdc_tx_context
  ADD COLUMN IF NOT EXISTS verified boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS rejection text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS committed_at timestamptz NOT NULL DEFAULT '-infinity';

-- The one way to write an anchor. SECURITY DEFINER runs as the table owner, which forced row-level security still
-- binds to the caller's tenant (depot.tenant_id) and legal-entity scope: the nightly job's transaction sees the whole
-- chain. The head hash is read from audit_entry at p_head_seq (the head the job verified), never taken from the
-- caller; seq 0 is the empty chain (genesis), which :verify does not check. The verdict columns are the job's report.
CREATE OR REPLACE FUNCTION audit_anchor_record(p_date date, p_head_seq bigint, p_entries bigint, p_verified boolean,
    p_breaks bigint, p_first_break_seq bigint, p_legacy_breaks bigint) RETURNS boolean
  LANGUAGE plpgsql SECURITY DEFINER SET search_path FROM CURRENT AS $$
DECLARE
  t uuid := nullif(current_setting('depot.tenant_id', true), '')::uuid;
  today date := (now() AT TIME ZONE 'UTC')::date;
  h text;
  n int;
BEGIN
  IF t IS NULL THEN
    RAISE EXCEPTION 'audit_anchor_record: no tenant' USING ERRCODE = '42501';
  END IF;
  IF p_date IS NULL OR p_date > today OR p_date < today - 1 THEN
    RAISE EXCEPTION 'audit_anchor_record: anchor date % is not today or yesterday (UTC)', p_date USING ERRCODE = '22023';
  END IF;
  IF p_head_seq = 0 THEN
    h := repeat('0', 64);
  ELSE
    SELECT e.hash INTO h FROM audit_entry e WHERE e.tenant_id = t AND e.seq = p_head_seq;
    IF h IS NULL THEN
      RAISE EXCEPTION 'audit_anchor_record: the chain holds no row %', p_head_seq USING ERRCODE = '22023';
    END IF;
  END IF;
  INSERT INTO audit_anchor (tenant_id, anchor_date, head_seq, head_hash, entries, verified, breaks, first_break_seq, legacy_breaks)
    VALUES (t, p_date, p_head_seq, h, p_entries, p_verified, p_breaks, p_first_break_seq, p_legacy_breaks)
    ON CONFLICT (tenant_id, anchor_date) DO NOTHING;
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN n = 1;
END;
$$;
REVOKE ALL ON FUNCTION audit_anchor_record(date, bigint, bigint, boolean, bigint, bigint, bigint) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'REVOKE INSERT ON audit_anchor FROM depot_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION audit_anchor_record(date, bigint, bigint, boolean, bigint, bigint, bigint) TO depot_app';
  END IF;
END
$$;
