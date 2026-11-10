CREATE TABLE plume.runtime_findings (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    check_id bigint NOT NULL UNIQUE,
    previous_check_id bigint CHECK (previous_check_id > 0),
    source_id bigint NOT NULL CHECK (source_id > 0),
    source text NOT NULL CHECK (char_length(source) BETWEEN 1 AND 80),
    baseline_scan_id bigint NOT NULL CHECK (baseline_scan_id > 0),
    service text NOT NULL CHECK (char_length(service) BETWEEN 1 AND 80),
    method text NOT NULL CHECK (method IN ('GET', 'PUT', 'POST', 'DELETE', 'OPTIONS', 'HEAD', 'PATCH', 'TRACE')),
    route_key text NOT NULL CHECK (char_length(route_key) BETWEEN 1 AND 200),
    route text NOT NULL CHECK (char_length(route) BETWEEN 1 AND 200),
    rule text NOT NULL CHECK (rule IN ('unregistered_success', 'deprecated_success')),
    first_seen timestamptz NOT NULL,
    last_positive_at timestamptz NOT NULL CHECK (last_positive_at >= first_seen),
    retest_at timestamptz CHECK (retest_at >= first_seen),
    retest_evidence_id bigint CHECK (retest_evidence_id > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((retest_at IS NULL) = (retest_evidence_id IS NULL)),
    FOREIGN KEY (workspace_id, check_id) REFERENCES plume.checks(workspace_id, id) ON DELETE CASCADE
);

CREATE INDEX runtime_findings_identity_idx
    ON plume.runtime_findings (workspace_id, service, method, route_key, rule, id DESC);

CREATE INDEX runtime_findings_workspace_idx
    ON plume.runtime_findings (workspace_id, id DESC);

ALTER TABLE plume.runtime_security_sources
    ADD COLUMN findings_warning text CHECK (findings_warning IN ('scope_limited', 'evaluation_failed')),
    ADD COLUMN findings_warning_at timestamptz,
    ADD CHECK ((findings_warning IS NULL) = (findings_warning_at IS NULL));
