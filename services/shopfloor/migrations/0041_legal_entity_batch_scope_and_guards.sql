-- B-03b review follow-ups to 0039.
--
-- 1. An exported line follows its export batch's legal entity, a plain column check on the batch found by its primary
--    key, instead of joining pay_line and worker for every row. A batch exported before batches carried a legal entity
--    (NULL) still follows its pay line's worker, so its lines stay visible to the worker's legal entity and ExportPay's
--    "already exported" check keeps seeing them: a line is never exported twice. The assign-legal-entity routes give
--    such a batch its legal entity once all its lines belong to that legal entity's workers.
DROP POLICY IF EXISTS pay_export_line_legal_entity_scope ON pay_export_line;
CREATE POLICY pay_export_line_legal_entity_scope ON pay_export_line AS RESTRICTIVE USING (
  current_setting('depot.legal_entity_scopes', true) = '*'
  OR EXISTS (
    SELECT 1 FROM pay_export_batch AS scope_batch
    WHERE scope_batch.tenant_id = pay_export_line.tenant_id
      AND scope_batch.id = pay_export_line.batch_id
      AND (scope_batch.legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ','))
           OR (scope_batch.legal_entity_id IS NULL AND EXISTS (
             SELECT 1 FROM pay_line AS scope_line
             JOIN worker AS scope_worker ON scope_worker.tenant_id = scope_line.tenant_id AND scope_worker.id = scope_line.worker_id
             WHERE scope_line.tenant_id = pay_export_line.tenant_id
               AND scope_line.id = pay_export_line.pay_line_id
               AND scope_worker.legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')))))
  )
) WITH CHECK (
  current_setting('depot.legal_entity_scopes', true) = '*'
  OR EXISTS (
    SELECT 1 FROM pay_export_batch AS scope_batch
    WHERE scope_batch.tenant_id = pay_export_line.tenant_id
      AND scope_batch.id = pay_export_line.batch_id
      AND scope_batch.legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ','))
  )
);

-- 2. A worker's legal entity may be corrected while no time, day, correction or cost-rate row hangs off the worker
--    (pay lines need a day). After that it never changes: the history follows the worker. The checks run as the caller
--    under row-level security; the only path that changes a set legal entity is the tenant administrator's
--    assign-legal-entity route, whose scope is '*', and the children of a worker always share its legal entity, so a
--    caller who may update the worker sees all of them.
--    A worker deleted and inserted again could take another legal entity without this trigger; no application path
--    deletes workers, and once any child row exists its foreign key refuses the delete.
CREATE OR REPLACE FUNCTION shopfloor_worker_legal_entity_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.legal_entity_id IS NOT NULL AND NEW.legal_entity_id IS DISTINCT FROM OLD.legal_entity_id THEN
    IF NEW.legal_entity_id IS NULL THEN
      RAISE EXCEPTION 'a worker''s legal entity cannot be removed' USING ERRCODE = 'check_violation';
    END IF;
    IF EXISTS (SELECT 1 FROM time_registration WHERE tenant_id = OLD.tenant_id AND worker_id = OLD.id)
       OR EXISTS (SELECT 1 FROM worker_day WHERE tenant_id = OLD.tenant_id AND worker_id = OLD.id)
       OR EXISTS (SELECT 1 FROM registration_correction WHERE tenant_id = OLD.tenant_id AND worker_id = OLD.id)
       OR EXISTS (SELECT 1 FROM worker_cost_rate WHERE tenant_id = OLD.tenant_id AND worker_id = OLD.id) THEN
      RAISE EXCEPTION 'a worker''s legal entity never changes once it has time or pay' USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  RETURN NEW;
END $$;

-- 3. A payroll export batch's legal entity is set once: at export, or by the backfill for a batch exported before.
CREATE OR REPLACE FUNCTION shopfloor_pay_export_batch_legal_entity_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.legal_entity_id IS NOT NULL AND NEW.legal_entity_id IS DISTINCT FROM OLD.legal_entity_id THEN
    RAISE EXCEPTION 'a payroll export batch''s legal entity is set once and never changes' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER pay_export_batch_legal_entity_guard BEFORE UPDATE OF legal_entity_id ON pay_export_batch
  FOR EACH ROW EXECUTE FUNCTION shopfloor_pay_export_batch_legal_entity_guard();
