-- dbx:no-transaction
-- B-03b review: the child policies of 0039 find the tenant's workers whose legal_entity_id::text is in the caller's
-- scope list and hash them once per query. The index is on that expression (the policies compare the text form,
-- like every legal-entity policy, so a malformed scope entry matches nothing instead of failing a uuid cast), for
-- tenants with many workers. Measured with 2,000 workers and 100,000 registrations, in 2 and in 50 legal entities, the
-- planner still prefers one sequential scan of worker hashed for the whole query (about 2 ms warm, 15 ms cold); the
-- registration scan dominates. Built CONCURRENTLY so a large shop floor keeps taking registrations during the build. A concurrent
-- build that failed leaves an INVALID index that IF NOT EXISTS would keep; drop only such a leftover first, in this
-- schema (another schema's build in progress is also not yet valid).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
             WHERE c.relname = 'worker_legal_entity' AND c.relnamespace = current_schema()::regnamespace
               AND NOT i.indisvalid) THEN
    EXECUTE 'DROP INDEX worker_legal_entity';
  END IF;
END
$$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS worker_legal_entity ON worker (tenant_id, (legal_entity_id::text), id);
