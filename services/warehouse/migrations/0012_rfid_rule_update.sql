-- An RFID rule can be edited (#32): the application role may now UPDATE it, with a version for If-Match and who changed it
-- last. A rule is still one per reader, antenna and action (the unique index), so an edit that would make a second one is
-- refused with the same conflict as a create.
ALTER TABLE rfid_rule ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1 CHECK (version >= 1);
ALTER TABLE rfid_rule ADD COLUMN IF NOT EXISTS updated_by text;
ALTER TABLE rfid_rule ADD COLUMN IF NOT EXISTS updated_at timestamptz;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT UPDATE ON rfid_rule TO depot_app';
  END IF;
END
$$;
