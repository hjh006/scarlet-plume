ALTER TABLE plume.checks ADD UNIQUE (workspace_id, id);

CREATE TABLE plume.automation_jobs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    actor_id bigint NOT NULL REFERENCES plume.users(id),
    provider text NOT NULL CHECK (provider IN ('postgresql', 'openmetadata', 'feishu')),
    source text,
    check_id bigint,
    token_digest text NOT NULL UNIQUE CHECK (token_digest ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    FOREIGN KEY (workspace_id, check_id) REFERENCES plume.checks(workspace_id, id) ON DELETE CASCADE,
    CHECK ((provider = 'feishu' AND check_id IS NOT NULL AND source IS NULL)
        OR (provider <> 'feishu' AND check_id IS NULL AND source IS NOT NULL AND char_length(source) BETWEEN 1 AND 80))
);

CREATE INDEX automation_jobs_workspace_idx ON plume.automation_jobs (workspace_id, id DESC);
CREATE INDEX automation_jobs_check_idx ON plume.automation_jobs (check_id) WHERE check_id IS NOT NULL;
CREATE UNIQUE INDEX automation_jobs_active_source_idx ON plume.automation_jobs (workspace_id, provider, source)
    WHERE revoked_at IS NULL AND source IS NOT NULL;
CREATE UNIQUE INDEX automation_jobs_active_check_idx ON plume.automation_jobs (workspace_id, check_id)
    WHERE revoked_at IS NULL AND check_id IS NOT NULL;

CREATE TABLE plume.automation_runs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id bigint NOT NULL REFERENCES plume.automation_jobs(id) ON DELETE CASCADE,
    run_id text NOT NULL CHECK (run_id ~ '^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$'),
    payload_digest text NOT NULL CHECK (payload_digest ~ '^[0-9a-f]{64}$'),
    status text NOT NULL CHECK (status IN ('success', 'error', 'skipped')),
    code text CHECK (code IN ('collection_failed', 'collection_timeout', 'check_closed', 'unchanged', 'awaiting_remediation', 'stale_observation')),
    observed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_count integer NOT NULL DEFAULT 0 CHECK (created_count >= 0),
    existing_count integer NOT NULL DEFAULT 0 CHECK (existing_count >= 0),
    import_id bigint REFERENCES plume.import_runs(id) ON DELETE SET NULL,
    evidence_id bigint UNIQUE REFERENCES plume.check_evidence(id) ON DELETE SET NULL,
    member_present boolean,
    permission text,
    UNIQUE (job_id, run_id),
    CHECK ((status = 'success' AND code IS NULL) OR (status <> 'success' AND code IS NOT NULL)),
    CHECK ((member_present IS TRUE AND permission IS NOT NULL AND permission IN ('view', 'edit', 'full_access'))
        OR (member_present IS NOT TRUE AND permission IS NULL))
);

CREATE INDEX automation_runs_latest_idx ON plume.automation_runs (job_id, id DESC);
CREATE INDEX automation_runs_observed_idx ON plume.automation_runs (job_id, observed_at DESC, id DESC)
    WHERE member_present IS NOT NULL;
