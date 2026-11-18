CREATE TABLE IF NOT EXISTS number_reservation (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  sequence_code text NOT NULL,
  value bigint NOT NULL,
  formatted text NOT NULL,
  status text NOT NULL CHECK (status IN ('reserved', 'confirmed', 'voided')),
  lease_expires_at timestamptz NOT NULL,
  document_ref text,
  void_reason text,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  confirmed_at timestamptz,
  voided_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, sequence_code, value),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS ix_number_reservation_open ON number_reservation (tenant_id, sequence_code, lease_expires_at) WHERE status = 'reserved';
ALTER TABLE number_reservation ENABLE ROW LEVEL SECURITY;
ALTER TABLE number_reservation FORCE ROW LEVEL SECURITY;
CREATE POLICY number_reservation_tenant_access ON number_reservation AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);
CREATE POLICY number_reservation_legal_entity_scope ON number_reservation AS RESTRICTIVE
  USING (current_setting('depot.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON number_reservation TO depot_app';
  END IF;
END
$$;
