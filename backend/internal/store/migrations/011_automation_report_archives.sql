CREATE TABLE plume.automation_report_archives (
    automation_run_id bigint PRIMARY KEY REFERENCES plume.automation_runs(id) ON DELETE CASCADE,
    generated_at timestamptz NOT NULL,
    status text NOT NULL CHECK (status IN ('ready', 'failed')),
    code text CHECK (code IN ('scope_too_large', 'generation_failed')),
    summary jsonb,
    report jsonb,
    CHECK ((status = 'ready' AND code IS NULL
            AND summary IS NOT NULL AND jsonb_typeof(summary) = 'object'
            AND report IS NOT NULL AND jsonb_typeof(report) = 'object')
        OR (status = 'failed' AND code IS NOT NULL AND summary IS NULL AND report IS NULL))
);
