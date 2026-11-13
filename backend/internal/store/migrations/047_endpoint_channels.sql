CREATE TABLE plume.endpoint_devices (
 source_id bigint PRIMARY KEY,
 workspace_id bigint NOT NULL,
 device_ref text NOT NULL CHECK(device_ref ~ '^[0-9a-f]{64}$'),
 os text NOT NULL CHECK(os IN ('windows','linux','darwin')),
 heartbeat jsonb NOT NULL,
 heartbeat_digest text NOT NULL,
 observed_at timestamptz NOT NULL,
 last_seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(workspace_id,source_id) REFERENCES plume.operations_sources(workspace_id,id) ON DELETE CASCADE
);
CREATE TABLE plume.endpoint_channel_policies (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL,
 source_id bigint NOT NULL,
 channel text NOT NULL CHECK(channel IN ('usb','print','clipboard')),
 scope text NOT NULL CHECK(scope IN ('controlled_operations','native_control')),
 revision bigint NOT NULL CHECK(revision>0),
 actor_id bigint NOT NULL REFERENCES plume.users(id),
 body jsonb NOT NULL,
 digest text NOT NULL,
 status text NOT NULL CHECK(status IN ('draft','approved','rejected','revoked','superseded')),
 approved_by bigint REFERENCES plume.users(id),
 approved_at timestamptz,
 ended_at timestamptz,
 decision_note text NOT NULL DEFAULT '',
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL,
 CHECK(approved_by IS NULL OR approved_by<>actor_id),
 UNIQUE(workspace_id,id),
 UNIQUE(source_id,channel,scope,revision),
 FOREIGN KEY(workspace_id,source_id) REFERENCES plume.operations_sources(workspace_id,id) ON DELETE CASCADE
);
CREATE INDEX endpoint_channel_policies_latest_idx ON plume.endpoint_channel_policies(workspace_id,id DESC);
CREATE TABLE plume.endpoint_channel_receipts (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL,
 source_id bigint NOT NULL,
 policy_id bigint NOT NULL,
 receipt_ref text NOT NULL,
 input_digest text NOT NULL,
 receipt jsonb NOT NULL,
 snapshot jsonb NOT NULL,
 snapshot_digest text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(source_id,receipt_ref),
 FOREIGN KEY(workspace_id,source_id) REFERENCES plume.operations_sources(workspace_id,id) ON DELETE CASCADE,
 FOREIGN KEY(workspace_id,policy_id) REFERENCES plume.endpoint_channel_policies(workspace_id,id) ON DELETE CASCADE
);
CREATE INDEX endpoint_channel_receipts_policy_idx ON plume.endpoint_channel_receipts(policy_id,id DESC);
CREATE TABLE plume.endpoint_channel_exports (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 policy_id bigint NOT NULL,
 actor_id bigint NOT NULL REFERENCES plume.users(id),
 actor text NOT NULL,
 snapshot jsonb NOT NULL,
 snapshot_digest text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(workspace_id,policy_id) REFERENCES plume.endpoint_channel_policies(workspace_id,id) ON DELETE CASCADE
);
-- Content is immutable; decisions are separate state, never a new payload.
CREATE FUNCTION plume.protect_endpoint_policy_body() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.workspace_id,NEW.source_id,NEW.channel,NEW.scope,NEW.revision,NEW.actor_id,NEW.body,NEW.digest,NEW.expires_at,NEW.created_at)
    IS DISTINCT FROM (OLD.workspace_id,OLD.source_id,OLD.channel,OLD.scope,OLD.revision,OLD.actor_id,OLD.body,OLD.digest,OLD.expires_at,OLD.created_at)
    OR (OLD.approved_by IS NOT NULL AND (NEW.approved_by,NEW.approved_at) IS DISTINCT FROM (OLD.approved_by,OLD.approved_at))
    OR (OLD.ended_at IS NOT NULL AND NEW.ended_at IS DISTINCT FROM OLD.ended_at) THEN
  RAISE EXCEPTION 'endpoint policy payload is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER endpoint_policy_immutable BEFORE UPDATE ON plume.endpoint_channel_policies FOR EACH ROW EXECUTE FUNCTION plume.protect_endpoint_policy_body();
CREATE FUNCTION plume.protect_endpoint_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'endpoint evidence is immutable'; END $$;
CREATE TRIGGER endpoint_receipt_immutable BEFORE UPDATE ON plume.endpoint_channel_receipts FOR EACH ROW EXECUTE FUNCTION plume.protect_endpoint_evidence();
CREATE TRIGGER endpoint_export_immutable BEFORE UPDATE ON plume.endpoint_channel_exports FOR EACH ROW EXECUTE FUNCTION plume.protect_endpoint_evidence();
