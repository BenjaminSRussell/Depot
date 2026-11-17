-- Ops control plane part 4 (DPT-50): tenant decommission with its audit trail, and the schedules of masked clones. Platform state,
-- not tenant data: the tenant they describe is a key column (customer_tenant, source_tenant), never tenant_id, and there is no RLS
-- (allowlisted in tools/rlscheck).

-- A decommission is the reverse of a provision: lakehouse, kafka-acls, databases, realm, in that order. One row per tenant, so a
-- repeated request returns the first record.
CREATE TABLE ops_tenant_decommission (
  customer_tenant uuid PRIMARY KEY REFERENCES ops_tenant_provision (customer_tenant),
  reason          text NOT NULL CHECK (length(reason) BETWEEN 1 AND 500),
  status          text NOT NULL DEFAULT 'DECOMMISSIONING' CHECK (status IN ('DECOMMISSIONING', 'DECOMMISSIONED', 'FAILED')),
  requested_by    text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  completed_at    timestamptz
);

CREATE TABLE ops_tenant_decommission_step (
  customer_tenant uuid NOT NULL REFERENCES ops_tenant_decommission (customer_tenant),
  action          text NOT NULL CHECK (action IN ('lakehouse', 'kafka-acls', 'databases', 'realm')),
  ord             integer NOT NULL,
  status          text NOT NULL CHECK (status IN ('DONE', 'FAILED')),
  detail          text NOT NULL DEFAULT '',
  reported_by     text NOT NULL,
  recorded_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (customer_tenant, action)
);

-- Who did what to which tenant, written in the transaction of the change (a decommission request, each step, its completion).
CREATE TABLE ops_audit (
  id      bigserial PRIMARY KEY,
  at      timestamptz NOT NULL DEFAULT now(),
  actor   text NOT NULL,
  action  text NOT NULL,
  subject text NOT NULL,
  detail  jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX ops_audit_subject ON ops_audit (subject, id);

-- Recurring masked clones of one tenant's schema into a non-production database, run by the job in this service through
-- tools/tenant-restore (`tenant-restore clone`). interval_hours is the period; next_run_at is claimed under a row lock so two
-- replicas never run the same schedule.
CREATE TABLE ops_clone_schedule (
  id             uuid PRIMARY KEY,
  name           text NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 100),
  source_tenant  uuid NOT NULL,
  schema_name    text NOT NULL CHECK (schema_name ~ '^[a-z_][a-z0-9_]{0,62}$'),
  masks_file     text NOT NULL CHECK (length(masks_file) BETWEEN 1 AND 500),
  interval_hours integer NOT NULL CHECK (interval_hours BETWEEN 1 AND 8760),
  enabled        boolean NOT NULL DEFAULT true,
  next_run_at    timestamptz NOT NULL,
  last_run_at    timestamptz,
  last_status    text CHECK (last_status IN ('OK', 'FAILED')),
  created_by     text NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ops_clone_run (
  id          bigserial PRIMARY KEY,
  schedule_id uuid NOT NULL REFERENCES ops_clone_schedule (id) ON DELETE CASCADE,
  started_at  timestamptz NOT NULL,
  finished_at timestamptz NOT NULL,
  status      text NOT NULL CHECK (status IN ('OK', 'FAILED')),
  detail      text NOT NULL DEFAULT ''
);
CREATE INDEX ops_clone_run_schedule ON ops_clone_run (schedule_id, id DESC);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ops_tenant_decommission, ops_tenant_decommission_step, ops_clone_schedule TO depot_app';
    EXECUTE 'GRANT DELETE ON ops_clone_schedule TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON ops_audit, ops_clone_run TO depot_app';
    EXECUTE 'GRANT USAGE ON SEQUENCE ops_audit_id_seq, ops_clone_run_id_seq TO depot_app';
  END IF;
END
$$;
