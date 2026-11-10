CREATE TABLE plume.postgres_asset_inspections (
    asset_id bigint PRIMARY KEY REFERENCES plume.assets(id) ON DELETE CASCADE,
    job_id bigint NOT NULL REFERENCES plume.managed_postgres_jobs(job_id) ON DELETE CASCADE,
    automation_run_id bigint NOT NULL REFERENCES plume.automation_runs(id) ON DELETE CASCADE,
    source_host text NOT NULL,
    source_port integer NOT NULL,
    source_database text NOT NULL,
    inspection jsonb NOT NULL CHECK (jsonb_typeof(inspection) = 'object')
);

CREATE TABLE plume.postgres_check_bindings (
    check_id bigint PRIMARY KEY REFERENCES plume.checks(id) ON DELETE CASCADE,
    job_id bigint NOT NULL REFERENCES plume.managed_postgres_jobs(job_id) ON DELETE CASCADE,
    source_host text NOT NULL,
    source_port integer NOT NULL,
    source_database text NOT NULL
);

CREATE INDEX postgres_check_bindings_job_idx ON plume.postgres_check_bindings (job_id, check_id);

CREATE TABLE plume.postgres_check_evidence (
    evidence_id bigint PRIMARY KEY REFERENCES plume.check_evidence(id) ON DELETE CASCADE,
    automation_run_id bigint NOT NULL REFERENCES plume.automation_runs(id) ON DELETE CASCADE,
    cycle_id bigint NOT NULL CHECK (cycle_id >= 0),
    fact_digest text NOT NULL CHECK (fact_digest ~ '^[0-9a-f]{64}$'),
    observation jsonb NOT NULL CHECK (jsonb_typeof(observation) = 'object')
);

ALTER TABLE plume.check_events
    DROP CONSTRAINT check_events_action_check,
    ADD CONSTRAINT check_events_action_check
    CHECK (action IN ('created', 'evidence', 'submitted', 'changes', 'verified', 'postgres_binding'));
