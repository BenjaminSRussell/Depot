-- #40: the legal entity on the other side of an intercompany customer. Recorded when billing's invoice.posted names a trading partner,
-- it lets the ledger journal of a payment application name that partner on the receivable credit, so a payment lowers the tagged
-- receivable in the partner balances like the invoice raised it.
CREATE TABLE IF NOT EXISTS ar_customer_partner (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  partner_legal_entity_id uuid NOT NULL CHECK (partner_legal_entity_id <> legal_entity_id),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, customer_id)
);

ALTER TABLE ar_customer_partner ENABLE ROW LEVEL SECURITY;
ALTER TABLE ar_customer_partner FORCE ROW LEVEL SECURITY;
CREATE POLICY ar_customer_partner_tenant_access ON ar_customer_partner AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ar_customer_partner_legal_entity_scope ON ar_customer_partner AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ar_customer_partner TO passage_app';
  END IF;
END
$$;
