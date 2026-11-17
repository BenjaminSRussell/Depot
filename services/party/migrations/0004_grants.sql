DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON party, consumer_inbox, idempotency_keys, payment_term, posting_profile, customer_role, party_merge TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON outbox TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON pending_change TO depot_app';
  END IF;
END
$$;
