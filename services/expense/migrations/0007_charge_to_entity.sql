-- #40: an expense line may name the legal entity that bears its cost (the cost-bearing entity) when it differs from the entity that
-- pays the report. The approved-report event carries those lines so intercompany can charge the paying entity's cost to the bearer.
alter table expense_line add column if not exists charge_to_legal_entity_id uuid;
