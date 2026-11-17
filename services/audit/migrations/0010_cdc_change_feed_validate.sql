-- Second half of 0009, kept apart so the scan runs without ACCESS EXCLUSIVE (as 0006 for 0005): VALIDATE CONSTRAINT
-- takes SHARE UPDATE EXCLUSIVE, so appends and reads on audit_entry go on while it checks the existing rows.
SET LOCAL lock_timeout = '5s';

ALTER TABLE audit_entry VALIDATE CONSTRAINT audit_entry_actor_kind_check;
ALTER TABLE audit_entry VALIDATE CONSTRAINT audit_entry_service_actor;
ALTER TABLE audit_entry VALIDATE CONSTRAINT audit_entry_flag_check;
ALTER TABLE audit_entry VALIDATE CONSTRAINT audit_entry_source_check;
