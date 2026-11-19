DROP POLICY IF EXISTS ai_data_access_legal_entity_scope ON ai_data_access;
CREATE POLICY ai_data_access_legal_entity_scope ON ai_data_access AS RESTRICTIVE
  USING (current_setting('depot.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = any(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));
