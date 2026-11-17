-- AUD-API-1 (B-06): the actor of a change is the verified caller. Each change also says what kind of actor
-- made it (a person, a service or an agent), the person a service or agent recorded it for, why it was made
-- and which save, business event and workflow run it belongs to.
--
-- Expand only, and catalog only: audit_entry is live and append-only. A column with a constant default is a
-- fast default (no rewrite, so the append-only trigger never fires, and NOT NULL needs no scan), and the checks
-- are added NOT VALID so nothing is scanned under the ACCESS EXCLUSIVE lock, which lasts an instant. The
-- lock_timeout makes the migration fail (and the rollout retry) instead of queueing behind a long transaction
-- while every later query on the table queues behind it. 0006 validates the checks without that lock.
--
-- Rows written before this migration keep actor_kind '' (their actor was not checked) and their original hash
-- payload, so the tenant chain still verifies. Row-level security and the depot_app table grants (0002) cover
-- the new columns unchanged.
SET LOCAL lock_timeout = '5s';

ALTER TABLE audit_entry
  ADD COLUMN IF NOT EXISTS actor_kind text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS on_behalf_of text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS reason text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS change_set_id text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS event_type text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS event_id text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS workflow_instance_id text NOT NULL DEFAULT '';

ALTER TABLE audit_entry ADD CONSTRAINT audit_entry_actor_kind_check
  CHECK (actor_kind IN ('', 'person', 'service', 'agent')) NOT VALID;
-- Lengths are characters (length()), as the service and the OpenAPI maxLength count them.
ALTER TABLE audit_entry ADD CONSTRAINT audit_entry_context_length_check
  CHECK (length(on_behalf_of) <= 256 AND length(reason) <= 1000 AND length(change_set_id) <= 256
     AND length(event_type) <= 256 AND length(event_id) <= 256 AND length(workflow_instance_id) <= 256) NOT VALID;
-- A person acting directly is never recorded on someone else's behalf.
ALTER TABLE audit_entry ADD CONSTRAINT audit_entry_on_behalf_of_actor
  CHECK (on_behalf_of = '' OR actor_kind IN ('service', 'agent')) NOT VALID;
-- A service or an agent is recorded as a service actor ("service:<name>"), and a person never is. Rows from
-- before AUD-API-1 (actor_kind '') are left alone.
ALTER TABLE audit_entry ADD CONSTRAINT audit_entry_service_actor
  CHECK (actor_kind = '' OR (actor_kind IN ('service', 'agent')) = (user_id LIKE 'service:%')) NOT VALID;
