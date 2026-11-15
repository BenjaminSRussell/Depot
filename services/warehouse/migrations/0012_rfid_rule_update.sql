-- An RFID rule can be edited (#32): the application role may now UPDATE it. A rule is still one per reader, antenna and
-- action (the unique index), so an edit that would make a second one is refused with the same conflict as a create.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE ON rfid_rule TO passage_app';
  END IF;
END
$$;
