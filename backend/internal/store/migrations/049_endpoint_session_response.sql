ALTER TABLE plume.endpoint_channel_policies DROP CONSTRAINT endpoint_channel_policies_scope_check;
ALTER TABLE plume.endpoint_channel_policies ADD CONSTRAINT endpoint_channel_policies_scope_check CHECK(scope IN ('controlled_operations','native_control','session_response'));
CREATE TABLE plume.endpoint_session_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL,
 source_id bigint NOT NULL,
 policy_id bigint NOT NULL,
 event_ref text NOT NULL,
 operation_ref text NOT NULL,
 input_digest text NOT NULL,
 event jsonb NOT NULL,
 snapshot jsonb NOT NULL,
 snapshot_digest text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(source_id,event_ref),
 FOREIGN KEY(workspace_id,source_id) REFERENCES plume.operations_sources(workspace_id,id) ON DELETE CASCADE,
 FOREIGN KEY(workspace_id,policy_id) REFERENCES plume.endpoint_channel_policies(workspace_id,id) ON DELETE CASCADE
);
CREATE INDEX endpoint_session_events_policy_idx ON plume.endpoint_session_events(workspace_id,policy_id,id DESC);
CREATE INDEX endpoint_session_events_operation_idx ON plume.endpoint_session_events(source_id,policy_id,operation_ref,id);
CREATE TRIGGER endpoint_session_event_immutable BEFORE UPDATE ON plume.endpoint_session_events FOR EACH ROW EXECUTE FUNCTION plume.protect_endpoint_evidence();
CREATE FUNCTION plume.end_revoked_endpoint_sessions() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL THEN
 UPDATE plume.endpoint_channel_policies SET ended_at=COALESCE(ended_at,NEW.revoked_at)
 WHERE source_id=NEW.id AND scope='session_response' AND ended_at IS NULL;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER endpoint_session_source_revoked AFTER UPDATE OF revoked_at ON plume.operations_sources FOR EACH ROW EXECUTE FUNCTION plume.end_revoked_endpoint_sessions();
