alter table batch_job add column if not exists last_fired_at timestamptz;
alter table batch_job add column if not exists schedule_legal_entity_id uuid;
alter table batch_job add column if not exists scheduled_by uuid;
alter table batch_job add column if not exists schedule_duties text[];

create index if not exists ix_batch_job_scheduled on batch_job (status) where cron is not null;

DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION batch_due_schedules()
      RETURNS TABLE (tenant_id uuid, job_id uuid, code text, cron text, time_zone text, legal_entity_id uuid, scheduled_by uuid, duties text[], since timestamptz)
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
        SELECT j.tenant_id, j.id, j.code, j.cron, j.time_zone, j.schedule_legal_entity_id, j.scheduled_by,
               coalesce(j.schedule_duties, '{}'::text[]), coalesce(j.last_fired_at, j.created_at)
        FROM batch_job j
        WHERE j.cron IS NOT NULL AND j.status = 'active'
          AND j.schedule_legal_entity_id IS NOT NULL AND j.scheduled_by IS NOT NULL
      $body$;
    CREATE OR REPLACE FUNCTION batch_mark_schedule_fired(p_tenant uuid, p_job uuid, p_since timestamptz, p_fired timestamptz)
      RETURNS boolean
      LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
        WITH moved AS (
          UPDATE batch_job SET last_fired_at = p_fired
          WHERE tenant_id = p_tenant AND id = p_job AND coalesce(last_fired_at, created_at) = p_since
          RETURNING 1
        )
        SELECT EXISTS (SELECT 1 FROM moved)
      $body$;
  $f$, current_schema());
END
$do$;

REVOKE ALL ON FUNCTION batch_due_schedules() FROM PUBLIC;
REVOKE ALL ON FUNCTION batch_mark_schedule_fired(uuid, uuid, timestamptz, timestamptz) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION batch_due_schedules() TO depot_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION batch_mark_schedule_fired(uuid, uuid, timestamptz, timestamptz) TO depot_app';
  END IF;
END
$$;
