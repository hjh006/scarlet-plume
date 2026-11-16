-- Denied decisions without reservations have a separate deduplication domain and
-- cannot mutate settlement evidence or account balances.
CREATE TABLE plume.ai_gateway_denied_events (
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    event_id text NOT NULL,
    event_hash text NOT NULL CHECK (event_hash ~ '^[a-f0-9]{64}$'),
    verified boolean NOT NULL,
    verification_reasons jsonb NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(workspace_id,event_id)
);
CREATE TRIGGER ai_gateway_denied_events_immutable
BEFORE UPDATE OR DELETE ON plume.ai_gateway_denied_events
FOR EACH ROW EXECUTE FUNCTION plume.ai_gateway_audit_immutable();
