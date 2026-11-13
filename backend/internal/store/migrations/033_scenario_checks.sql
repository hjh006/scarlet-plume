ALTER TABLE plume.postgres_asset_inspections ADD COLUMN source_revision bigint CHECK(source_revision>0);
CREATE TABLE plume.scenario_check_runs (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 asset_id bigint NOT NULL,
 activity_id bigint,
 scenario text NOT NULL,
 task text NOT NULL,
 created_by bigint NOT NULL REFERENCES plume.users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 snapshot_digest text NOT NULL,
 UNIQUE(workspace_id,id)
);
CREATE INDEX scenario_check_runs_workspace_idx ON plume.scenario_check_runs(workspace_id,id DESC);
CREATE TABLE plume.scenario_check_remediations (
 workspace_id bigint NOT NULL,
 run_id bigint NOT NULL,
 finding_id text NOT NULL,
 action_id bigint NOT NULL,
 PRIMARY KEY(run_id,finding_id),
 FOREIGN KEY(workspace_id,run_id) REFERENCES plume.scenario_check_runs(workspace_id,id) ON DELETE CASCADE,
 FOREIGN KEY(workspace_id,action_id) REFERENCES plume.control_actions(workspace_id,id) ON DELETE CASCADE
);
