CREATE TABLE plume.managed_postgres_jobs (
    job_id bigint PRIMARY KEY REFERENCES plume.automation_jobs(id) ON DELETE CASCADE,
    host text NOT NULL CHECK (char_length(host) BETWEEN 1 AND 253),
    port integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
    database_name text NOT NULL CHECK (char_length(database_name) BETWEEN 1 AND 63),
    username text NOT NULL CHECK (char_length(username) BETWEEN 1 AND 63),
    schemas text[] NOT NULL CHECK (cardinality(schemas) BETWEEN 1 AND 64),
    encrypted_password bytea NOT NULL CHECK (octet_length(encrypted_password) BETWEEN 1 AND 4096),
    interval_minutes integer NOT NULL CHECK (interval_minutes BETWEEN 1 AND 1440),
    enabled boolean NOT NULL,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    next_run_at timestamptz,
    requested_at timestamptz,
    lease_id text CHECK (lease_id ~ '^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$'),
    lease_until timestamptz,
    CHECK ((lease_id IS NULL) = (lease_until IS NULL)),
    CHECK (enabled = (next_run_at IS NOT NULL))
);

CREATE INDEX managed_postgres_due_idx ON plume.managed_postgres_jobs
    (LEAST(requested_at, next_run_at), job_id)
    WHERE requested_at IS NOT NULL OR next_run_at IS NOT NULL;
