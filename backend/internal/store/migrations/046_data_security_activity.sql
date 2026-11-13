ALTER TABLE plume.operations_security_events ALTER COLUMN check_id DROP NOT NULL;
ALTER TABLE plume.operations_security_events ADD CONSTRAINT operations_security_event_activity_check
 CHECK ((COALESCE(metadata->>'outcome','observed')='activity') = (check_id IS NULL));
