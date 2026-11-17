-- The version of the ledger period lock close last sent for a period (FR-14.1, B-08c review). Every row of a
-- period carries the same value; close computes the next one as greatest(previous + 1, clock in microseconds)
-- under the period's row locks, so a database clock that steps back cannot make a lock update older than the
-- one the ledger already holds. Expand-only: a nullable-free column with a constant default is a catalogue
-- change, and the grants of 0001 cover it.
SET LOCAL lock_timeout = '5s';

ALTER TABLE period_status ADD COLUMN IF NOT EXISTS ledger_lock_version bigint NOT NULL DEFAULT 0;
