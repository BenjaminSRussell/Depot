-- #47: legal-entity scope on the tables that carry one. The repository now sets depot.legal_entity_scopes in every transaction (the
-- entity a sync job runs for, or the caller's X-Depot-Legal-Entity: '*' for a caller who holds the all-scope duty), so payment_prediction
-- and prediction_sync get the restrictive policy every other service has: a session sees a row when its scope is '*' or lists the row's
-- legal entity. A session that sets no scope (an unset setting reads as NULL) sees and writes nothing. A prediction stored before the
-- sync fed the entity has a NULL legal_entity_id and is visible only to a '*' session; the sync claims such rows for the entity the receivables feed names, in a '*' session (PgRepository.claim_unowned), before it scores them. The service has no receivables data to backfill from here.
ALTER TABLE payment_prediction ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_prediction FORCE ROW LEVEL SECURITY;
CREATE POLICY payment_prediction_legal_entity_scope ON payment_prediction AS RESTRICTIVE
  USING (current_setting('depot.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));

ALTER TABLE prediction_sync ENABLE ROW LEVEL SECURITY;
ALTER TABLE prediction_sync FORCE ROW LEVEL SECURITY;
CREATE POLICY prediction_sync_legal_entity_scope ON prediction_sync AS RESTRICTIVE
  USING (current_setting('depot.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));
