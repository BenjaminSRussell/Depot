-- #40: the legal entity on the other side of an intercompany vendor. Recorded when an intercompany charge is taken into the intake (the
-- lender's legal entity for the pairing's vendor account), it lets the payment run name the lender as the trading partner on the control
-- debit of that vendor, so a payment lowers the tagged payable in the partner balances like the invoice raised it.
CREATE TABLE IF NOT EXISTS ap_vendor_partner (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  partner_legal_entity_id uuid NOT NULL CHECK (partner_legal_entity_id <> legal_entity_id),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, vendor_id)
);

ALTER TABLE ap_vendor_partner ENABLE ROW LEVEL SECURITY;
ALTER TABLE ap_vendor_partner FORCE ROW LEVEL SECURITY;
CREATE POLICY ap_vendor_partner_tenant_access ON ap_vendor_partner AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ap_vendor_partner_legal_entity_scope ON ap_vendor_partner AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ap_vendor_partner TO passage_app';
  END IF;
END
$$;
