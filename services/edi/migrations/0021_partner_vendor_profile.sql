-- #35 (35-5): a partner that is one of our vendors. Its inbound X12 810 is a vendor invoice for payables' vendor-invoice intake, in this
-- legal entity and for this vendor, and not the self-billed invoice of a customer. A partner has one or the other for its 810s.
CREATE TABLE IF NOT EXISTS partner_vendor_profile (
  tenant_id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, partner_id),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id)
);
ALTER TABLE partner_vendor_profile ENABLE ROW LEVEL SECURITY;
ALTER TABLE partner_vendor_profile FORCE ROW LEVEL SECURITY;
CREATE POLICY partner_vendor_profile_tenant_access ON partner_vendor_profile AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY partner_vendor_profile_legal_entity_scope ON partner_vendor_profile AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON partner_vendor_profile TO passage_app';
  END IF;
END
$$;
