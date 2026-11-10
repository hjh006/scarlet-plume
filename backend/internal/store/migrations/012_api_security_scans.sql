CREATE TABLE plume.api_security_scans (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    actor_id bigint NOT NULL REFERENCES plume.users(id),
    source text NOT NULL CHECK (char_length(source) BETWEEN 1 AND 80),
    scan_digest text NOT NULL CHECK (scan_digest ~ '^[0-9a-f]{64}$'),
    version text NOT NULL,
    operation_count integer NOT NULL CHECK (operation_count BETWEEN 0 AND 200),
    sensitive_field_count integer NOT NULL CHECK (sensitive_field_count >= 0),
    finding_count integer NOT NULL CHECK (finding_count >= 0),
    check_count integer NOT NULL DEFAULT 0 CHECK (check_count BETWEEN 0 AND operation_count),
    unresolved_references integer NOT NULL CHECK (unresolved_references >= 0),
    operations jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(operations) = 'array'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX api_security_scans_latest_idx ON plume.api_security_scans (workspace_id, id DESC);
CREATE INDEX api_security_scans_source_idx ON plume.api_security_scans (workspace_id, source, id DESC);

CREATE TABLE plume.api_security_check_bindings (
    asset_id bigint PRIMARY KEY REFERENCES plume.assets(id) ON DELETE CASCADE,
    check_id bigint NOT NULL UNIQUE REFERENCES plume.checks(id) ON DELETE CASCADE,
    fact_digest text NOT NULL CHECK (fact_digest ~ '^[0-9a-f]{64}$')
);
