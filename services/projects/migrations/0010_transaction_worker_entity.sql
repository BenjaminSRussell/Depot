-- #40: an HOUR transaction may name the legal entity that employs its worker (the lending entity) when the timesheet source knows it.
-- The transaction.posted event carries it with the project's legal entity (the borrowing entity), so intercompany can charge the
-- hours across when the two differ.
ALTER TABLE project_transaction ADD COLUMN IF NOT EXISTS worker_legal_entity_id uuid;
