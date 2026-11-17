-- B-07a review fix: debezium_cdc_party reads every column of vendor_bank_account but the sealed ones. 0021 now grants
-- that from the start; this migration brings a database that applied the first version of 0021 (a table-wide SELECT)
-- to the same grant. Re-running it changes nothing.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc_party') THEN
    EXECUTE 'REVOKE SELECT ON vendor_bank_account FROM debezium_cdc_party';
    EXECUTE 'GRANT SELECT (tenant_id, id, vendor_id, legal_entity_id, account_holder, bank_country, currency_code, account_last4, created_by, created_at) ON vendor_bank_account TO debezium_cdc_party';
  END IF;
END
$$;
