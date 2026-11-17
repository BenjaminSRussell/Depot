DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON ap_vendor_block TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON ap_payment_proposal, ap_payment_proposal_group, ap_payment_proposal_line TO depot_app';
    EXECUTE 'GRANT UPDATE (status, approved_by, approved_at) ON ap_payment_proposal TO depot_app';
  END IF;
END
$$;
