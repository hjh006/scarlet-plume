ALTER TABLE plume.activities ADD COLUMN revision bigint NOT NULL DEFAULT 1;
ALTER TABLE plume.activities ADD COLUMN updated_at timestamptz NOT NULL DEFAULT clock_timestamp();
UPDATE plume.activities SET updated_at=created_at;
CREATE TABLE plume.activity_changes (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    activity_id bigint NOT NULL REFERENCES plume.activities(id) ON DELETE CASCADE,
    revision bigint NOT NULL,
    actor_id bigint NOT NULL REFERENCES plume.users(id),
    before_snapshot jsonb NOT NULL,
    after_snapshot jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(activity_id,revision)
);
CREATE INDEX activity_changes_page_idx ON plume.activity_changes(workspace_id,activity_id,id DESC);
