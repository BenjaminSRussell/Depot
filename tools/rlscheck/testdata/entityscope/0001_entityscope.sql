-- B-03b: child tables of a service D17 named, judged on no-legal-entity-scope. Only parent_row and child_follows pass.
create table parent_row (tenant_id uuid not null, id uuid not null, legal_entity_id uuid not null, primary key (tenant_id, id));
alter table parent_row enable row level security;
alter table parent_row force row level security;
create policy parent_row_tenant on parent_row as permissive
  using (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('depot.tenant_id', true)::uuid);
create policy parent_row_entity on parent_row as restrictive
  using (current_setting('depot.legal_entity_scopes', true) = '*' or legal_entity_id::text = any(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));

-- Follows the parent's legal entity: passes.
create table child_follows (tenant_id uuid not null, parent_id uuid not null);
alter table child_follows enable row level security;
alter table child_follows force row level security;
create policy child_follows_tenant on child_follows as permissive
  using (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('depot.tenant_id', true)::uuid);
create policy child_follows_entity on child_follows as restrictive
  using (current_setting('depot.legal_entity_scopes', true) = '*' or exists (
    select 1 from parent_row p where p.tenant_id = child_follows.tenant_id and p.id = child_follows.parent_id
      and p.legal_entity_id::text = any(string_to_array(current_setting('depot.legal_entity_scopes', true), ','))));

-- Mentions the scope setting but admits every row: fails.
create table child_vacuous (tenant_id uuid not null, parent_id uuid not null);
alter table child_vacuous enable row level security;
alter table child_vacuous force row level security;
create policy child_vacuous_tenant on child_vacuous as permissive
  using (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('depot.tenant_id', true)::uuid);
create policy child_vacuous_entity on child_vacuous as restrictive
  using (current_setting('depot.legal_entity_scopes', true) is not null or true);

-- Joins the parent without its legal entity: fails.
create table child_no_entity (tenant_id uuid not null, parent_id uuid not null);
alter table child_no_entity enable row level security;
alter table child_no_entity force row level security;
create policy child_no_entity_tenant on child_no_entity as permissive
  using (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('depot.tenant_id', true)::uuid);
create policy child_no_entity_entity on child_no_entity as restrictive
  using (current_setting('depot.legal_entity_scopes', true) = '*' or exists (
    select 1 from parent_row p where p.tenant_id = child_no_entity.tenant_id and p.id = child_no_entity.parent_id));

-- Tenant-wide, no policy on the scope at all: fails.
create table tenant_wide (tenant_id uuid not null);
alter table tenant_wide enable row level security;
alter table tenant_wide force row level security;
create policy tenant_wide_tenant on tenant_wide as permissive
  using (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('depot.tenant_id', true)::uuid);

-- Compares the legal entity, then admits every row anyway with a tautology: fails.
create table child_tautology (tenant_id uuid not null, parent_id uuid not null);
alter table child_tautology enable row level security;
alter table child_tautology force row level security;
create policy child_tautology_tenant on child_tautology as permissive
  using (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('depot.tenant_id', true)::uuid);
create policy child_tautology_entity on child_tautology as restrictive
  using (exists (
    select 1 from parent_row p where p.tenant_id = child_tautology.tenant_id and p.id = child_tautology.parent_id
      and p.legal_entity_id::text = any(string_to_array(current_setting('depot.legal_entity_scopes', true), ','))) or 1 = 1);
