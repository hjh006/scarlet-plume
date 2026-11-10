ALTER TABLE plume.managed_postgres_jobs
    ADD COLUMN auto_checks boolean NOT NULL DEFAULT false,
    ADD COLUMN auto_reports boolean NOT NULL DEFAULT false;

ALTER TABLE plume.automation_runs
    DROP CONSTRAINT automation_runs_code_check,
    ADD CONSTRAINT automation_runs_code_check CHECK (code IN (
        'collection_failed', 'collection_timeout', 'check_closed', 'unchanged',
        'awaiting_remediation', 'stale_observation', 'automation_scope_limit',
        'collection_schema_unavailable'
    ));
