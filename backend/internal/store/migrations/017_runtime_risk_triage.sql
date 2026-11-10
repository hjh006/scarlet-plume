ALTER TABLE plume.runtime_findings ADD UNIQUE (workspace_id, id);

CREATE TABLE plume.runtime_risk_triage (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL,
    finding_id bigint NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    actor_id bigint NOT NULL REFERENCES plume.users(id),
    severity text NOT NULL CHECK (severity IN ('low', 'medium', 'high', 'critical')),
    owner_team text NOT NULL CHECK (char_length(owner_team) BETWEEN 1 AND 80),
    due_date date,
    reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 400),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (finding_id, revision),
    FOREIGN KEY (workspace_id, finding_id) REFERENCES plume.runtime_findings(workspace_id, id) ON DELETE CASCADE
);

CREATE INDEX runtime_risk_triage_audit_idx ON plume.runtime_risk_triage (workspace_id, created_at DESC, id DESC);

ALTER TABLE plume.runtime_security_sources
    ADD COLUMN revoked_by bigint REFERENCES plume.users(id);
