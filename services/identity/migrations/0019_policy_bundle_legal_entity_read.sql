-- B-03b: the policy compiler drops a legal-entity-scoped role assignment whose legal entity is no longer active, so it
-- must read the legal entities of every tenant, as it already reads users and role assignments (0008). The same gate:
-- SELECT only, only with depot.policy_compile on, and only in a session that sees identity_operator (the operator
-- tenant's). The legal-entity scope policy is restrictive, so the compiler also sets depot.legal_entity_scopes to '*'.
create policy identity_legal_entities_policy_compile_read on identity_legal_entities as permissive for select
  using (current_setting('depot.policy_compile', true) = 'on' and exists (select 1 from identity_operator));
