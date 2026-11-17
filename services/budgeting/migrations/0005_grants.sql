-- Application role privileges. depot_app (no BYPASSRLS) works under the row-level
-- security policies. Tables the service updates carry UPDATE; the rest are insert-only.
-- Skipped when the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON outbox, idempotency_keys, budget_control_rule, budget_register_entry, budget_check_result, budget_reservation_consumption, investment_carry_forward TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON budget_control_balance TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON encumbrance TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON budget_reservation TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON budget_reservation_line TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON budget_reservation_number TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON program_position TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON investment_program TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON program_assignment TO depot_app';
  END IF;
END
$$;
