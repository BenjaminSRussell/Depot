-- B-03b (architecture gap D17): the shop floor's time and pay data is scoped to the legal entity that employs the
-- worker. A worker carries its employing legal entity; its registrations, calculated days, pay lines, corrections and
-- dated cost rates follow it through restrictive policies, and a payroll export batch carries the legal entity it
-- exported. A caller scoped to company A no longer sees, or writes, company B's time and pay.
--
-- Expand only: the columns are nullable so existing rows stay valid. A NULL legal entity matches no scoped caller and
-- is visible only to all-entity callers until a tenant administrator assigns it
-- (POST /v1/shopfloor/workers/{id}:assign-legal-entity). Once set, a worker's legal entity never changes: its history
-- follows it, so moving it would move that history to another company.
ALTER TABLE worker ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
ALTER TABLE pay_export_batch ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
-- The child policies look the worker up by its primary key (tenant_id, id); no new index, so no lock on a live table
-- beyond the catalog-only column adds.

DO $$
DECLARE
  owner_table text;
BEGIN
  FOREACH owner_table IN ARRAY ARRAY['worker', 'pay_export_batch']
  LOOP
    EXECUTE format(
      'CREATE POLICY %I ON %I AS RESTRICTIVE
         USING (current_setting(''depot.legal_entity_scopes'', true) = ''*''
                OR legal_entity_id::text = ANY(string_to_array(current_setting(''depot.legal_entity_scopes'', true), '','')))',
      owner_table || '_legal_entity_scope', owner_table
    );
  END LOOP;
END $$;

-- The worker's time and pay rows follow the worker's legal entity.
DO $$
DECLARE
  child_table text;
BEGIN
  FOREACH child_table IN ARRAY ARRAY['time_registration', 'worker_day', 'pay_line', 'registration_correction', 'worker_cost_rate']
  LOOP
    EXECUTE format(
      'CREATE POLICY %I ON %I AS RESTRICTIVE USING (
         current_setting(''depot.legal_entity_scopes'', true) = ''*''
         OR EXISTS (
           SELECT 1 FROM worker AS scope_worker
           WHERE scope_worker.tenant_id = %I.tenant_id
             AND scope_worker.id = %I.worker_id
             AND scope_worker.legal_entity_id::text = ANY(string_to_array(current_setting(''depot.legal_entity_scopes'', true), '',''))
         )
       ) WITH CHECK (
         current_setting(''depot.legal_entity_scopes'', true) = ''*''
         OR EXISTS (
           SELECT 1 FROM worker AS scope_worker
           WHERE scope_worker.tenant_id = %I.tenant_id
             AND scope_worker.id = %I.worker_id
             AND scope_worker.legal_entity_id::text = ANY(string_to_array(current_setting(''depot.legal_entity_scopes'', true), '',''))
         )
       )',
      child_table || '_legal_entity_scope', child_table,
      child_table, child_table, child_table, child_table
    );
  END LOOP;
END $$;

-- An exported line follows the pay line it exported, and so the worker.
CREATE POLICY pay_export_line_legal_entity_scope ON pay_export_line AS RESTRICTIVE USING (
  current_setting('depot.legal_entity_scopes', true) = '*'
  OR EXISTS (
    SELECT 1 FROM pay_line AS scope_line
    JOIN worker AS scope_worker ON scope_worker.tenant_id = scope_line.tenant_id AND scope_worker.id = scope_line.worker_id
    WHERE scope_line.tenant_id = pay_export_line.tenant_id
      AND scope_line.id = pay_export_line.pay_line_id
      AND scope_worker.legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ','))
  )
) WITH CHECK (
  current_setting('depot.legal_entity_scopes', true) = '*'
  OR EXISTS (
    SELECT 1 FROM pay_line AS scope_line
    JOIN worker AS scope_worker ON scope_worker.tenant_id = scope_line.tenant_id AND scope_worker.id = scope_line.worker_id
    WHERE scope_line.tenant_id = pay_export_line.tenant_id
      AND scope_line.id = pay_export_line.pay_line_id
      AND scope_worker.legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ','))
  )
);

CREATE OR REPLACE FUNCTION shopfloor_worker_legal_entity_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.legal_entity_id IS NOT NULL AND NEW.legal_entity_id IS DISTINCT FROM OLD.legal_entity_id THEN
    RAISE EXCEPTION 'a worker''s legal entity is set once and never changes' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER worker_legal_entity_guard BEFORE UPDATE OF legal_entity_id ON worker
  FOR EACH ROW EXECUTE FUNCTION shopfloor_worker_legal_entity_guard();
