-- Partner tax registration (#13). The party master held no country for a customer or vendor, so fulfillment and receiving could not say
-- where a goods movement went or came from. A role (customer_role or vendor_role id) now has one tax registration: the ISO 3166-1 alpha-2
-- country its VAT registration is in (what decides an EU cross-border movement for Intrastat) and, optionally, its VAT id. Fulfillment
-- and receiving read it with the service-only duty party.tax-registration.read when they report a shipment or a receipt.
CREATE TABLE IF NOT EXISTS partner_tax_registration (
  tenant_id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  partner_kind text NOT NULL CHECK (partner_kind IN ('CUSTOMER', 'VENDOR')),
  country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
  vat_id text NOT NULL DEFAULT '' CHECK (char_length(vat_id) <= 20),
  updated_by text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, partner_id)
);
ALTER TABLE partner_tax_registration ENABLE ROW LEVEL SECURITY;
ALTER TABLE partner_tax_registration FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS partner_tax_registration_tenant_access ON partner_tax_registration;
CREATE POLICY partner_tax_registration_tenant_access ON partner_tax_registration AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS partner_tax_registration_legal_entity_scope ON partner_tax_registration;
CREATE POLICY partner_tax_registration_legal_entity_scope ON partner_tax_registration AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON partner_tax_registration TO passage_app';
  END IF;
END
$$;
