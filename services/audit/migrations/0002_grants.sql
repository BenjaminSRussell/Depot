-- Application role privileges. depot_app (no BYPASSRLS) works under the row-level
-- security policies. The outbox is insert-only (Debezium relays it). audit_entry,
-- alert_rule and alert_inbox are append-only; audit_entry.seq is a bigserial, so
-- inserts need USAGE on its sequence. Skipped when the role does not exist
-- (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON audit_entry, alert_rule, alert_inbox TO depot_app';
    EXECUTE 'GRANT USAGE ON SEQUENCE audit_entry_seq_seq TO depot_app';
  END IF;
END
$$;
