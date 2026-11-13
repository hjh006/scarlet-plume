CREATE TABLE plume.operations_investigation_reports (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 title text NOT NULL CHECK(char_length(title) BETWEEN 1 AND 120),
 created_by bigint NOT NULL REFERENCES plume.users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 source_id bigint,
 service text NOT NULL DEFAULT '',
 snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 snapshot_digest text NOT NULL,
 UNIQUE(workspace_id,id)
);
CREATE INDEX operations_reports_workspace_idx ON plume.operations_investigation_reports(workspace_id,id DESC);
CREATE TABLE plume.operations_report_schedules (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 title text NOT NULL CHECK(char_length(title) BETWEEN 1 AND 120),
 period text NOT NULL CHECK(period IN ('daily','weekly','monthly')),
 timezone text NOT NULL,
 scope jsonb NOT NULL,
 enabled boolean NOT NULL,
 revision bigint NOT NULL DEFAULT 1,
 created_by bigint NOT NULL REFERENCES plume.users(id),
 updated_by bigint NOT NULL REFERENCES plume.users(id),
 next_run_at timestamptz NOT NULL,
 last_report_id bigint,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(workspace_id,id)
);
ALTER TABLE plume.operations_investigation_reports ADD COLUMN schedule_id bigint;
ALTER TABLE plume.operations_investigation_reports ADD COLUMN period_start timestamptz;
ALTER TABLE plume.operations_investigation_reports ADD COLUMN period_end timestamptz;
CREATE UNIQUE INDEX operations_report_schedule_period_idx ON plume.operations_investigation_reports(workspace_id,schedule_id,period_start,period_end) WHERE schedule_id IS NOT NULL;
CREATE INDEX operations_report_schedule_due_idx ON plume.operations_report_schedules(next_run_at,id) WHERE enabled;
