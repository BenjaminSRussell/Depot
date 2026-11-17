-- Release rows and outbox events are append-only and environments are read-only, so
-- depot_app gets no UPDATE or DELETE on them. Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON platform_release, outbox TO depot_app';
    EXECUTE 'GRANT SELECT ON platform_environment TO depot_app';
  END IF;
END
$$;
