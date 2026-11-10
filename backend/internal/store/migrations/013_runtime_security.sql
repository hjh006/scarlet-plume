CREATE TABLE plume.runtime_security_sources (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    actor_id bigint NOT NULL REFERENCES plume.users(id),
    kind text NOT NULL CHECK (kind IN ('gateway', 'otlp')),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
    service text NOT NULL CHECK (char_length(service) BETWEEN 1 AND 80),
    token_digest text NOT NULL UNIQUE CHECK (token_digest ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    last_seen timestamptz,
    receipt_count integer NOT NULL DEFAULT 0 CHECK (receipt_count BETWEEN 0 AND 20000)
);

CREATE INDEX runtime_security_sources_workspace_idx
    ON plume.runtime_security_sources (workspace_id, id DESC);

CREATE UNIQUE INDEX runtime_security_sources_active_idx
    ON plume.runtime_security_sources (workspace_id, kind, name, service)
    WHERE revoked_at IS NULL;

CREATE TABLE plume.runtime_security_batches (
    source_id bigint NOT NULL REFERENCES plume.runtime_security_sources(id) ON DELETE CASCADE,
    batch_id text NOT NULL CHECK (batch_id ~ '^[0-9a-f]{64}$'),
    payload_digest text NOT NULL CHECK (payload_digest ~ '^[0-9a-f]{64}$'),
    accepted integer NOT NULL CHECK (accepted BETWEEN 1 AND 250),
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (source_id, batch_id)
);

CREATE INDEX runtime_security_batches_expiry_idx
    ON plume.runtime_security_batches (source_id, expires_at);

CREATE TABLE plume.runtime_security_routes (
    source_id bigint NOT NULL REFERENCES plume.runtime_security_sources(id) ON DELETE CASCADE,
    method text NOT NULL CHECK (method IN ('GET', 'PUT', 'POST', 'DELETE', 'OPTIONS', 'HEAD', 'PATCH', 'TRACE')),
    route_key text NOT NULL CHECK (char_length(route_key) BETWEEN 1 AND 200),
    route text NOT NULL CHECK (char_length(route) BETWEEN 1 AND 200),
    requests bigint NOT NULL CHECK (requests > 0),
    responses_4xx bigint NOT NULL CHECK (responses_4xx >= 0),
    responses_5xx bigint NOT NULL CHECK (responses_5xx >= 0),
    last_seen timestamptz NOT NULL,
    database_systems text[] NOT NULL,
    PRIMARY KEY (source_id, method, route_key),
    CHECK (responses_4xx + responses_5xx <= requests)
);
