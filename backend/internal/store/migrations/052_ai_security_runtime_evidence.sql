CREATE TABLE plume.ai_security_runtime_sources (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    created_by bigint NOT NULL REFERENCES plume.users(id),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 160),
    application_id text NOT NULL,
    catalog_digest text NOT NULL CHECK (catalog_digest ~ '^[0-9a-f]{64}$'),
    token_hash text NOT NULL UNIQUE CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    receipt_count integer NOT NULL DEFAULT 0 CHECK (receipt_count BETWEEN 0 AND 20000),
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    last_seen timestamptz
);
CREATE INDEX ai_security_runtime_sources_workspace_idx ON plume.ai_security_runtime_sources(workspace_id,id DESC);
CREATE TABLE plume.ai_security_runtime_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id bigint NOT NULL REFERENCES plume.ai_security_runtime_sources(id) ON DELETE CASCADE,
    event_id text NOT NULL,
    payload_digest text NOT NULL CHECK (payload_digest ~ '^[0-9a-f]{64}$'),
    record jsonb NOT NULL CHECK (jsonb_typeof(record) = 'object'),
    received_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(source_id,event_id)
);
CREATE INDEX ai_security_runtime_events_source_idx ON plume.ai_security_runtime_events(source_id,id DESC);
