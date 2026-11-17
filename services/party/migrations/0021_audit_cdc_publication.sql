-- B-07a (architecture gap D3): party's vendor bank accounts are the pilot table set of the audit table CDC. The
-- audit-cdc-party connector (deploy/kafka/connect.yaml) captures their row changes from publication depot_audit_party,
-- separate from the outbox publication, and the audit service records each changed field in the change chain.
--
-- REPLICA IDENTITY FULL: an update or delete then carries the row's old values, so the audit trail can say "from".
-- It costs one full old row in the WAL per update or delete of this low-volume master-data table. The old row, like
-- the new one, includes account_number_sealed and routing_number_sealed: their ciphertext (AES-GCM, fieldcrypt) is in
-- the WAL and is streamed over the replication connection to Kafka Connect, where column.exclude.list drops it before
-- anything is written to Kafka. A publication column list cannot leave them out while the table's replica identity is
-- FULL (every column is then part of the identity).
-- The publication publishes insert, update and delete, not truncate. Reads pg_publication_rel, not
-- pg_publication_tables (see 0019). Re-running this on a database that already ran it changes nothing.
--
-- Parts b and c add the other audited party tables (credit limit, payment terms) the same way: REPLICA IDENTITY FULL,
-- ALTER PUBLICATION depot_audit_party ADD TABLE, a column-level SELECT for debezium_cdc_party.
SET LOCAL lock_timeout = '5s';

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

-- The CDC role may read every column but the sealed ones. With snapshot.mode no_data the connector never selects rows
-- (values come from the replication stream and column metadata from pg_catalog), so the grant matters only if
-- someone starts a snapshot of the table: that then cannot read the ciphertext columns.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc_party') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO debezium_cdc_party', current_schema());
    EXECUTE 'REVOKE SELECT ON vendor_bank_account FROM debezium_cdc_party';
    EXECUTE 'GRANT SELECT (tenant_id, id, vendor_id, legal_entity_id, account_holder, bank_country, currency_code, account_last4, created_by, created_at) ON vendor_bank_account TO debezium_cdc_party';
  ELSE
    RAISE WARNING 'role debezium_cdc_party does not exist: the audit-cdc-party connector cannot read vendor_bank_account; create the role, then delete migration 0021 from schema_migrations and restart to re-run this grant';
  END IF;
END
$$;
