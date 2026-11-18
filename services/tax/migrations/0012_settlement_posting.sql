alter table tax_ledger_config add column settlement_account text;
alter table tax_settlement_run
  add column posting_account text,
  add column posted_entry_id text,
  add column posting_pending boolean not null default false;
create index tax_settlement_run_unposted_idx on tax_settlement_run (tenant_id, legal_entity_id, created_at) where posting_pending;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT UPDATE (posted_entry_id, posting_pending) ON tax_settlement_run TO depot_app';
  END IF;
END
$$;
