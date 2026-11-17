-- Second half of 0005, kept apart so the scan runs without ACCESS EXCLUSIVE: VALIDATE CONSTRAINT takes SHARE
-- UPDATE EXCLUSIVE, so appends and reads on audit_entry go on while it checks the existing rows (all of which
-- carry the defaults). dbx.Migrate runs every file in its own transaction.
SET LOCAL lock_timeout = '5s';

ALTER TABLE audit_entry VALIDATE CONSTRAINT audit_entry_actor_kind_check;
ALTER TABLE audit_entry VALIDATE CONSTRAINT audit_entry_context_length_check;
ALTER TABLE audit_entry VALIDATE CONSTRAINT audit_entry_on_behalf_of_actor;
ALTER TABLE audit_entry VALIDATE CONSTRAINT audit_entry_service_actor;
