-- B-07a (architecture gap D3): party's vendor bank accounts are the pilot table set of the audit table CDC. The
-- audit-cdc-party connector (deploy/kafka/connect.yaml) captures their row changes from publication depot_audit_party,
-- separate from the outbox publication, and the audit service records each changed field in the change chain.
--
-- REPLICA IDENTITY FULL: an update or delete then carries the row's old values, so the audit trail can say "from".
-- It costs one full old row in the WAL per update or delete of this low-volume master-data table.
-- The publication publishes insert, update and delete, not truncate. Reads pg_publication_rel, not
-- pg_publication_tables (see 0019). Re-running this on a database that already ran it changes nothing.
--
-- Parts b and c add the other audited party tables (credit limit, payment terms) the same way: REPLICA IDENTITY FULL,
-- ALTER PUBLICATION depot_audit_party ADD TABLE, GRANT SELECT to debezium_cdc_party.
ALTER TABLE vendor_bank_account REPLICA IDENTITY FULL;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_publication WHERE pubname = 'depot_audit_party') THEN
    BEGIN
      CREATE PUBLICATION depot_audit_party WITH (publish = 'insert, update, delete');
    EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL;
    END;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_publication_rel pr JOIN pg_publication p ON p.oid = pr.prpubid
    WHERE p.pubname = 'depot_audit_party' AND pr.prrelid = to_regclass(format('%I.vendor_bank_account', current_schema()))
  ) THEN
    EXECUTE format('ALTER PUBLICATION depot_audit_party ADD TABLE %I.vendor_bank_account', current_schema());
  END IF;
END
$$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc_party') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO debezium_cdc_party', current_schema());
    EXECUTE 'GRANT SELECT ON vendor_bank_account TO debezium_cdc_party';
  ELSE
    RAISE WARNING 'role debezium_cdc_party does not exist: the audit-cdc-party connector cannot read vendor_bank_account; create the role, then delete migration 0021 from schema_migrations and restart to re-run this grant';
  END IF;
END
$$;
