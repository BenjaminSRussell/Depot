-- Masked-clone schedules (part 4 review): a run in flight holds a lease so another replica cannot claim the same schedule while a
-- long clone runs, and a claimed run whose pod died is retried once its lease expires. Deleting a schedule is a soft delete, so the
-- run history (the record that a tenant's data was cloned) is never erased; passage_app loses DELETE on both tables.
ALTER TABLE ops_clone_schedule ADD COLUMN running_until timestamptz;
ALTER TABLE ops_clone_schedule ADD COLUMN deleted_at timestamptz;
ALTER TABLE ops_clone_schedule DROP CONSTRAINT ops_clone_schedule_name_key;
CREATE UNIQUE INDEX ops_clone_schedule_name_live ON ops_clone_schedule (name) WHERE deleted_at IS NULL;
ALTER TABLE ops_clone_run DROP CONSTRAINT ops_clone_run_schedule_id_fkey;
ALTER TABLE ops_clone_run ADD CONSTRAINT ops_clone_run_schedule_id_fkey FOREIGN KEY (schedule_id) REFERENCES ops_clone_schedule (id) ON DELETE RESTRICT;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'REVOKE DELETE ON ops_clone_schedule FROM passage_app';
  END IF;
END
$$;
