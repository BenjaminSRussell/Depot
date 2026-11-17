-- Second half of 0011, kept apart so the scan runs without ACCESS EXCLUSIVE (as 0010 for 0009).
SET LOCAL lock_timeout = '5s';

ALTER TABLE audit_entry VALIDATE CONSTRAINT audit_entry_flag_check;
