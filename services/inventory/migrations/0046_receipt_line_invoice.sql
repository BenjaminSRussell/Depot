-- Part invoices: what each vendor invoice line invoiced against a receipt line. The line moves to PURCHASED once the invoiced
-- quantities reach the booked quantity, at the cumulative amount. One row per invoice line, so a redelivery records nothing twice.

CREATE TABLE IF NOT EXISTS receipt_line_invoice (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  line_source_id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  invoice_line_no integer NOT NULL CHECK (invoice_line_no > 0),
  qty numeric NOT NULL CHECK (qty > 0),
  amount numeric NOT NULL CHECK (amount >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, line_source_id, invoice_id, invoice_line_no)
);
ALTER TABLE receipt_line_invoice ENABLE ROW LEVEL SECURITY;
ALTER TABLE receipt_line_invoice FORCE ROW LEVEL SECURITY;
CREATE POLICY receipt_line_invoice_tenant_access ON receipt_line_invoice AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);
CREATE POLICY receipt_line_invoice_legal_entity_scope ON receipt_line_invoice AS RESTRICTIVE
  USING (current_setting('depot.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON receipt_line_invoice TO depot_app';
  END IF;
END
$$;
