-- Period lock (FR-14.1, B-08c): the ledger's own copy of whether a fiscal period accepts postings, written by the
-- close service through PUT /v1/ledger/ledgers/{ledger}/period-locks/{fiscalPeriodId} before close reports a period
-- transition. Close and the ledger have separate databases (ADR-0008, ADR-0012), so close's period_status row cannot be
-- read inside the posting transaction; this table can. A posting reads its row with FOR SHARE and the lock update takes
-- the ledger row and these rows FOR UPDATE, so a posting either commits before the lock changes or sees the new status.
--
-- One row per (ledger, fiscal period, source system) with the status close resolved for postings from that source;
-- source_system '*' is every source close does not map to a module. open_until carries a temporary reopen's expiry:
-- from then on the ledger treats the period as closed even before close's sweeper has closed it.
-- Expand-only: a new table, no change to existing ones.
CREATE TABLE IF NOT EXISTS period_lock (
  tenant_id        uuid        NOT NULL,
  legal_entity_id  uuid        NOT NULL,
  ledger_id        uuid        NOT NULL,
  fiscal_period_id uuid        NOT NULL,
  source_system    text        NOT NULL CHECK (char_length(source_system) BETWEEN 1 AND 40),
  module           text        NOT NULL CHECK (char_length(module) BETWEEN 1 AND 20),
  status           text        NOT NULL CHECK (status IN ('open', 'on_hold', 'closed', 'permanently_closed', 'never_opened')),
  open_until       timestamptz,
  start_date       date        NOT NULL,
  end_date         date        NOT NULL,
  lock_version     bigint      NOT NULL CHECK (lock_version > 0),
  changed_by       text        NOT NULL,
  changed_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, ledger_id, fiscal_period_id, source_system),
  CHECK (end_date >= start_date),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS period_lock_date_idx ON period_lock (tenant_id, ledger_id, start_date, end_date);

ALTER TABLE period_lock ENABLE ROW LEVEL SECURITY;
ALTER TABLE period_lock FORCE ROW LEVEL SECURITY;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename = 'period_lock' AND policyname = 'period_lock_tenant_access') THEN
    CREATE POLICY period_lock_tenant_access ON period_lock AS PERMISSIVE
      USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename = 'period_lock' AND policyname = 'period_lock_legal_entity_scope') THEN
    CREATE POLICY period_lock_legal_entity_scope ON period_lock AS RESTRICTIVE
      USING (current_setting('depot.legal_entity_scopes', true) = '*'
             OR legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));
  END IF;
END
$$;

-- Rows are upserted by the lock update; a source close stops sending is deleted. FOR SHARE needs UPDATE.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON period_lock TO depot_app';
  END IF;
END
$$;
