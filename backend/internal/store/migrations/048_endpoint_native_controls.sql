CREATE TABLE plume.endpoint_native_leases (
 policy_id bigint PRIMARY KEY,
 workspace_id bigint NOT NULL,
 source_id bigint NOT NULL,
 channel text NOT NULL CHECK(channel IN ('usb','print','clipboard')),
 manifest jsonb NOT NULL,
 lease_ref text NOT NULL UNIQUE CHECK(lease_ref ~ '^[0-9a-f]{64}$'),
 state text NOT NULL DEFAULT 'issued' CHECK(state IN ('issued','prepared','configured','readback','functional_verified','rolled_back','conflict','failed')),
 prior_state_digest text NOT NULL DEFAULT '',
 planned_state_digest text NOT NULL DEFAULT '',
 prepared_at timestamptz,
 applied_state_digest text NOT NULL DEFAULT '',
 latest_observed_at timestamptz,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(workspace_id,policy_id) REFERENCES plume.endpoint_channel_policies(workspace_id,id) ON DELETE CASCADE,
 FOREIGN KEY(workspace_id,source_id) REFERENCES plume.operations_sources(workspace_id,id) ON DELETE CASCADE
);
CREATE INDEX endpoint_native_leases_source_idx ON plume.endpoint_native_leases(source_id,policy_id);
CREATE FUNCTION plume.protect_endpoint_native_manifest() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.policy_id,NEW.workspace_id,NEW.source_id,NEW.channel,NEW.manifest,NEW.lease_ref)
 IS DISTINCT FROM (OLD.policy_id,OLD.workspace_id,OLD.source_id,OLD.channel,OLD.manifest,OLD.lease_ref)
 OR (OLD.prior_state_digest<>'' AND NEW.prior_state_digest<>OLD.prior_state_digest)
 OR (OLD.planned_state_digest<>'' AND NEW.planned_state_digest<>OLD.planned_state_digest)
 OR (OLD.prepared_at IS NOT NULL AND NEW.prepared_at IS DISTINCT FROM OLD.prepared_at)
 OR (OLD.applied_state_digest<>'' AND NEW.applied_state_digest<>OLD.applied_state_digest) THEN
 RAISE EXCEPTION 'endpoint native manifest and original state are immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER endpoint_native_manifest_immutable BEFORE UPDATE ON plume.endpoint_native_leases FOR EACH ROW EXECUTE FUNCTION plume.protect_endpoint_native_manifest();
