-- Application role privileges. depot_app (no BYPASSRLS) works under the row-level
-- security policies. outbox, idempotency_keys, container_line, packing_slip_line,
-- wave, wave_shipment, wave_line, shipment_tracking_event and packaging_fee_line are
-- append-only. Skipped when the role does not exist (developer databases without the
-- role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON outbox, idempotency_keys, container_line, packing_slip_line, wave, wave_shipment, wave_line, shipment_tracking_event, packaging_fee_line TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON number_counter, shipment, shipment_line, container, packing_slip, container_type, packing_rule, product_packaging, packaging_fee_rate, returnable_packaging_balance TO depot_app';
  END IF;
END
$$;
