-- #30 (FR-30.14, the customer portal's card and ACH payments): one row per payment attempt a customer contact starts in the portal.
-- The attempt names the open items and the amount on each; the payment provider (Stripe first, Adyen later) collects the card or bank
-- details on its own hosted element, so nothing here is a card number, a CVC or a bank account number: only the brand, the last four
-- digits and the expiry month and year the provider reports. The status moves only on a signature-verified provider webhook. On
-- success the attempt becomes an ordinary PORTAL payment applied through the payment and apply path (payment_id); a refund or a
-- dispute (an ACH return) reverses it through the unapply and receipt reversal path.
CREATE TABLE IF NOT EXISTS gateway_payment (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  provider text NOT NULL CHECK (provider IN ('STRIPE', 'ADYEN')),
  -- The provider account the payment is made on (a Stripe Connect account id), null for the platform account.
  provider_account text CHECK (provider_account IS NULL OR provider_account ~ '^[A-Za-z0-9_]{1,64}$'),
  -- The provider's payment (a Stripe PaymentIntent id), set once the provider created it.
  provider_intent_id text CHECK (provider_intent_id IS NULL OR provider_intent_id ~ '^[A-Za-z0-9_]{1,128}$'),
  method text NOT NULL CHECK (method IN ('CARD', 'ACH')),
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  status text NOT NULL DEFAULT 'REQUIRES_PAYMENT'
    CHECK (status IN ('REQUIRES_PAYMENT', 'PROCESSING', 'SUCCEEDED', 'FAILED', 'REFUNDED', 'DISPUTED')),
  card_brand text CHECK (card_brand IS NULL OR length(card_brand) <= 20),
  last4 text CHECK (last4 IS NULL OR last4 ~ '^[0-9]{4}$'),
  card_exp_month smallint CHECK (card_exp_month IS NULL OR card_exp_month BETWEEN 1 AND 12),
  card_exp_year smallint CHECK (card_exp_year IS NULL OR card_exp_year BETWEEN 2000 AND 2200),
  bank_name text CHECK (bank_name IS NULL OR length(bank_name) <= 100),
  failure_code text CHECK (failure_code IS NULL OR length(failure_code) <= 80),
  failure_message text CHECK (failure_message IS NULL OR length(failure_message) <= 300),
  payment_id uuid,
  refunded_amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (refunded_amount >= 0),
  needs_review boolean NOT NULL DEFAULT false,
  review_reason text CHECK (review_reason IS NULL OR length(review_reason) <= 300),
  contact_id text CHECK (contact_id IS NULL OR length(contact_id) <= 200),
  idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 200),
  request_hash text NOT NULL,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, payment_id) REFERENCES payment (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS gateway_payment_intent ON gateway_payment (provider, provider_intent_id) WHERE provider_intent_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS gateway_payment_customer ON gateway_payment (tenant_id, customer_id, created_at DESC);
CREATE INDEX IF NOT EXISTS gateway_payment_payment ON gateway_payment (tenant_id, payment_id) WHERE payment_id IS NOT NULL;
ALTER TABLE gateway_payment ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway_payment FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS gateway_payment_tenant_access ON gateway_payment;
CREATE POLICY gateway_payment_tenant_access ON gateway_payment AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);
DROP POLICY IF EXISTS gateway_payment_legal_entity_scope ON gateway_payment;
CREATE POLICY gateway_payment_legal_entity_scope ON gateway_payment AS RESTRICTIVE
  USING (current_setting('depot.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));

-- The open items an attempt pays and the amount on each, as the customer chose them.
CREATE TABLE IF NOT EXISTS gateway_payment_allocation (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  gateway_payment_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  open_item_id uuid NOT NULL,
  document_number text NOT NULL,
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  PRIMARY KEY (tenant_id, gateway_payment_id, line_no),
  UNIQUE (tenant_id, gateway_payment_id, open_item_id),
  FOREIGN KEY (tenant_id, gateway_payment_id) REFERENCES gateway_payment (tenant_id, id),
  FOREIGN KEY (tenant_id, open_item_id) REFERENCES open_item (tenant_id, id)
);
ALTER TABLE gateway_payment_allocation ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway_payment_allocation FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS gateway_payment_allocation_tenant_access ON gateway_payment_allocation;
CREATE POLICY gateway_payment_allocation_tenant_access ON gateway_payment_allocation AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);
DROP POLICY IF EXISTS gateway_payment_allocation_legal_entity_scope ON gateway_payment_allocation;
CREATE POLICY gateway_payment_allocation_legal_entity_scope ON gateway_payment_allocation AS RESTRICTIVE
  USING (current_setting('depot.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));

-- Every provider webhook event handled, by the provider's event id: a redelivered or replayed event is answered without being
-- handled twice, and the list is the attempt's history on the payment page.
CREATE TABLE IF NOT EXISTS gateway_event (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  provider text NOT NULL CHECK (provider IN ('STRIPE', 'ADYEN')),
  event_id text NOT NULL CHECK (length(event_id) BETWEEN 1 AND 128),
  event_type text NOT NULL CHECK (length(event_type) BETWEEN 1 AND 80),
  gateway_payment_id uuid NOT NULL,
  outcome text NOT NULL CHECK (length(outcome) <= 300),
  occurred_at timestamptz NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, provider, event_id),
  FOREIGN KEY (tenant_id, gateway_payment_id) REFERENCES gateway_payment (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS gateway_event_payment ON gateway_event (tenant_id, gateway_payment_id, processed_at);
ALTER TABLE gateway_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway_event FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS gateway_event_tenant_access ON gateway_event;
CREATE POLICY gateway_event_tenant_access ON gateway_event AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);
DROP POLICY IF EXISTS gateway_event_legal_entity_scope ON gateway_event;
CREATE POLICY gateway_event_legal_entity_scope ON gateway_event AS RESTRICTIVE
  USING (current_setting('depot.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));

-- Cross-tenant route from a provider payment id to its attempt, for the unauthenticated webhook listener (like edi.transport_work):
-- no depot_app grant, maintained by the SECURITY DEFINER trigger on gateway_payment and read only by receivables_gateway_route(),
-- which returns the tenant, legal entity and attempt ids and nothing else. The webhook is authenticated by its signature first.
CREATE TABLE IF NOT EXISTS gateway_route (
  provider text NOT NULL,
  intent_id text NOT NULL,
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  gateway_payment_id uuid NOT NULL,
  PRIMARY KEY (provider, intent_id)
);

DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION receivables_gateway_route_track() RETURNS trigger
      LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
    BEGIN
      IF NEW.provider_intent_id IS NOT NULL THEN
        INSERT INTO gateway_route (provider, intent_id, tenant_id, legal_entity_id, gateway_payment_id)
        VALUES (NEW.provider, NEW.provider_intent_id, NEW.tenant_id, NEW.legal_entity_id, NEW.id)
        ON CONFLICT (provider, intent_id) DO NOTHING;
      END IF;
      RETURN NEW;
    END
    $body$;
    CREATE OR REPLACE FUNCTION receivables_gateway_route(p_provider text, p_intent text)
      RETURNS TABLE (tenant_id uuid, legal_entity_id uuid, gateway_payment_id uuid)
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ SELECT r.tenant_id, r.legal_entity_id, r.gateway_payment_id FROM gateway_route r
        WHERE r.provider = p_provider AND r.intent_id = p_intent $body$;
  $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION receivables_gateway_route_track() FROM PUBLIC;
REVOKE ALL ON FUNCTION receivables_gateway_route(text, text) FROM PUBLIC;
DROP TRIGGER IF EXISTS gateway_payment_route ON gateway_payment;
CREATE TRIGGER gateway_payment_route AFTER INSERT OR UPDATE OF provider_intent_id ON gateway_payment
  FOR EACH ROW EXECUTE FUNCTION receivables_gateway_route_track();

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON gateway_payment, gateway_payment_allocation TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON gateway_event TO depot_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION receivables_gateway_route(text, text) TO depot_app';
  END IF;
END
$$;
