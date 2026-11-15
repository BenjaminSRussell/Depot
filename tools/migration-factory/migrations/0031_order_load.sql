-- #49: the staged lines of an open_orders run are loaded into sales (SALES lines, POST /v1/sales/opening-orders) and purchasing
-- (PURCHASE lines, POST /v1/procurement/opening-orders). order_load is the run's load (its own status and lease), order_load_row
-- what the targets took per staged line (the staged table stays insert-only). Both carry tenant_id first and are held to the tenant
-- by forced row-level security and to the caller's legal-entity scopes, like opening_load.
create table order_load (
  tenant_id uuid not null,
  run_id uuid not null,
  legal_entity_id uuid not null,
  status text not null check (status in ('STAGED', 'LOADING', 'PARTIAL', 'UNRECONCILED', 'MISMATCH', 'RECONCILED', 'FAILED')),
  staged int not null check (staged >= 0),
  loaded int not null default 0 check (loaded >= 0),
  rejected int not null default 0 check (rejected >= 0),
  pending int not null default 0 check (pending >= 0),
  attempts int not null default 0 check (attempts >= 0),
  last_error text not null default '',
  lease_until timestamptz,
  started_by text not null default '',
  started_at timestamptz,
  finished_at timestamptz,
  version int not null default 1 check (version >= 1),
  created_at timestamptz not null default now(),
  primary key (tenant_id, run_id),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id)
);

create table order_load_row (
  tenant_id uuid not null,
  run_id uuid not null,
  line_id uuid not null,
  legal_entity_id uuid not null,
  order_type text not null check (order_type in ('SALES', 'PURCHASE')),
  status text not null check (status in ('LOADED', 'REJECTED')),
  batch_key text not null default '',
  code text not null default '',
  detail text not null default '',
  updated_at timestamptz not null default now(),
  primary key (tenant_id, line_id),
  foreign key (tenant_id, run_id) references order_load (tenant_id, run_id),
  foreign key (tenant_id, line_id) references open_order_opening_line (tenant_id, line_id)
);
create index order_load_row_run on order_load_row (tenant_id, run_id, status);

alter table order_load enable row level security;
alter table order_load force row level security;
create policy order_load_tenant_access on order_load as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy order_load_legal_entity_scope on order_load as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table order_load_row enable row level security;
alter table order_load_row force row level security;
create policy order_load_row_tenant_access on order_load_row as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy order_load_row_legal_entity_scope on order_load_row as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON order_load, order_load_row TO passage_app';
  END IF;
END
$$;
