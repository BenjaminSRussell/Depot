-- Publishes migration.outbox to the outbox-migration connector (deploy/kafka/connect.yaml). Reads pg_publication_rel, not
-- pg_publication_tables, which opens every published relation. Re-running this on a database that already ran it changes nothing.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_publication WHERE pubname = 'depot_outbox_migration') THEN
    BEGIN
      CREATE PUBLICATION depot_outbox_migration;
    EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL;
    END;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_publication_rel pr JOIN pg_publication p ON p.oid = pr.prpubid
    WHERE p.pubname = 'depot_outbox_migration' AND pr.prrelid = to_regclass('migration.outbox')
  ) THEN
    EXECUTE 'ALTER PUBLICATION depot_outbox_migration ADD TABLE migration.outbox';
  END IF;
END
$$;
