-- Tenant purge and the recorded realm (#50 review). Platform state, not tenant data: keyed by customer_tenant, no RLS (allowlisted in
-- platform/go/rlsexempt).

-- The realm the provision created, so a decommission deletes exactly that realm and never a name it guessed.
ALTER TABLE ops_tenant_provision ADD COLUMN realm_name text CHECK (realm_name ~ '^[a-z0-9][a-z0-9-]{1,62}$');

-- A decommission keeps the tenant's data under the retention policy. The purge is a separate, later act: it needs the decommission
-- DECOMMISSIONED and a verified export on record (who, when, which export), and its outcome is audited.
CREATE TABLE ops_tenant_purge (
  customer_tenant      uuid PRIMARY KEY REFERENCES ops_tenant_decommission (customer_tenant),
  export_id            text NOT NULL CHECK (length(export_id) BETWEEN 1 AND 200),
  export_note          text NOT NULL DEFAULT '',
  export_verified_by   text NOT NULL,
  export_verified_at   timestamptz NOT NULL DEFAULT now(),
  status               text CHECK (status IN ('REQUESTED', 'DONE', 'FAILED')),
  detail               text NOT NULL DEFAULT '',
  requested_by         text,
  requested_at         timestamptz,
  completed_at         timestamptz
);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ops_tenant_purge TO passage_app';
  END IF;
END
$$;
