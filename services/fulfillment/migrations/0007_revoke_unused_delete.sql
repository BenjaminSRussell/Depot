-- The store never deletes from any fulfillment table, so depot_app loses DELETE on
-- number_counter, shipment, shipment_line, container, packing_slip, container_type,
-- packing_rule, product_packaging, packaging_fee_rate and
-- returnable_packaging_balance. No table keeps DELETE. Skipped when the role does not
-- exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'REVOKE DELETE ON number_counter, shipment, shipment_line, container, packing_slip, container_type, packing_rule, product_packaging, packaging_fee_rate, returnable_packaging_balance FROM depot_app';
  END IF;
END
$$;
