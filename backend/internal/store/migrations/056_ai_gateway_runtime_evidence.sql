ALTER TABLE plume.ai_gateway_reservations DROP CONSTRAINT ai_gateway_reservations_state_check;
ALTER TABLE plume.ai_gateway_reservations ADD CONSTRAINT ai_gateway_reservations_state_check CHECK(state IN ('reserved','unknown','settled'));

CREATE TABLE plume.ai_gateway_runtime_events (
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    event_id text NOT NULL,
    reservation_id text NOT NULL REFERENCES plume.ai_gateway_reservations(id) ON DELETE CASCADE,
    event_hash text NOT NULL,
    PRIMARY KEY(workspace_id,event_id)
);
