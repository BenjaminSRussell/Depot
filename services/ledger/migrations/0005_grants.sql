-- Application role privileges (FR-11.6). depot_app (no BYPASSRLS) gets INSERT and
-- SELECT only on the journal tables, so posted entries cannot be changed even before
-- the append-only triggers fire. Dimension combinations are immutable too. Skipped
-- when the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON outbox, idempotency_keys, main_account, ledger, balance TO depot_app';
    EXECUTE 'REVOKE UPDATE, DELETE, TRUNCATE ON journal_entry, journal_line, dimension_combination FROM depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON journal_entry, journal_line, dimension_combination TO depot_app';
  END IF;
END
$$;
