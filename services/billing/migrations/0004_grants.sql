-- Application role privileges. depot_app (no BYPASSRLS) works under the row-level
-- security policies. The outbox is insert-only (Debezium relays it); invoice_line,
-- invoice_installment and proposal_batch are append-only; invoices and the number
-- sequence are updated but never deleted. Skipped when the role does not exist
-- (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON invoice_line, invoice_installment, proposal_batch TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON invoice, number_sequence_counter TO depot_app';
  END IF;
END
$$;
