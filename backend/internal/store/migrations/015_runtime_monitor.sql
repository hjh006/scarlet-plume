ALTER TABLE plume.runtime_security_sources
    ADD COLUMN audit_count integer NOT NULL DEFAULT 0 CHECK (audit_count BETWEEN 0 AND 20000);

CREATE TABLE plume.runtime_monitor_minutes (
    source_id bigint NOT NULL REFERENCES plume.runtime_security_sources(id) ON DELETE CASCADE,
    minute_at timestamptz NOT NULL,
    requests bigint NOT NULL CHECK (requests > 0),
    responses_4xx bigint NOT NULL CHECK (responses_4xx >= 0),
    responses_5xx bigint NOT NULL CHECK (responses_5xx >= 0),
    PRIMARY KEY (source_id, minute_at),
    CHECK (responses_4xx + responses_5xx <= requests)
);

CREATE TABLE plume.runtime_request_audit (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id bigint NOT NULL REFERENCES plume.runtime_security_sources(id) ON DELETE CASCADE,
    observed_at timestamptz NOT NULL,
    method text NOT NULL CHECK (method IN ('GET', 'PUT', 'POST', 'DELETE', 'OPTIONS', 'HEAD', 'PATCH', 'TRACE')),
    route text NOT NULL CHECK (char_length(route) BETWEEN 1 AND 200),
    route_key text NOT NULL CHECK (char_length(route_key) BETWEEN 1 AND 200),
    status integer NOT NULL CHECK (status BETWEEN 100 AND 599)
);

CREATE INDEX runtime_request_audit_source_observed_idx
    ON plume.runtime_request_audit (source_id, observed_at DESC, id DESC);
