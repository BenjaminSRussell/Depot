-- AUD-API-1 (B-06): the actor of a change is the verified caller. Each change also says what kind of actor
-- made it (a person, a service or an agent), the person a service or agent recorded it for, why it was made
-- and which save, business event and workflow run it belongs to. Expand only: new columns with constant
-- defaults, so no existing row is rewritten (the append-only trigger never fires) and the previous release
-- keeps writing rows. Rows written before this migration keep actor_kind '' (their actor was not checked) and
-- their original hash payload, so the tenant chain still verifies. Row-level security and the depot_app
-- table grants (0002) cover the new columns unchanged.
ALTER TABLE audit_entry
  ADD COLUMN IF NOT EXISTS actor_kind text NOT NULL DEFAULT ''
    CHECK (actor_kind IN ('', 'person', 'service', 'agent')),
  ADD COLUMN IF NOT EXISTS on_behalf_of text NOT NULL DEFAULT ''
    CHECK (length(on_behalf_of) <= 256),
  ADD COLUMN IF NOT EXISTS reason text NOT NULL DEFAULT ''
    CHECK (length(reason) <= 1000),
  ADD COLUMN IF NOT EXISTS change_set_id text NOT NULL DEFAULT ''
    CHECK (length(change_set_id) <= 256),
  ADD COLUMN IF NOT EXISTS event_type text NOT NULL DEFAULT ''
    CHECK (length(event_type) <= 256),
  ADD COLUMN IF NOT EXISTS event_id text NOT NULL DEFAULT ''
    CHECK (length(event_id) <= 256),
  ADD COLUMN IF NOT EXISTS workflow_instance_id text NOT NULL DEFAULT ''
    CHECK (length(workflow_instance_id) <= 256);

-- A person acting directly is never recorded on someone else's behalf.
ALTER TABLE audit_entry
  ADD CONSTRAINT audit_entry_on_behalf_of_actor
    CHECK (on_behalf_of = '' OR actor_kind IN ('service', 'agent'));
