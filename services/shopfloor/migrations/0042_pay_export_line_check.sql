-- B-03b re-review R2: a new exported line must belong to a batch in the caller's scope (0041) and its pay line to a
-- worker in the caller's scope (as in 0039). Foreign keys ignore row-level security, so without the second condition
-- a caller in company A could put company B's pay line into A's batch, and B's next export would then fail on the
-- line's primary key. USING is unchanged from 0041.
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
  OR (EXISTS (
        SELECT 1 FROM pay_export_batch AS scope_batch
        WHERE scope_batch.tenant_id = pay_export_line.tenant_id
          AND scope_batch.id = pay_export_line.batch_id
          AND scope_batch.legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')))
      AND EXISTS (
        SELECT 1 FROM pay_line AS scope_line
        JOIN worker AS scope_worker ON scope_worker.tenant_id = scope_line.tenant_id AND scope_worker.id = scope_line.worker_id
        WHERE scope_line.tenant_id = pay_export_line.tenant_id
          AND scope_line.id = pay_export_line.pay_line_id
          AND scope_worker.legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ','))))
);
