CREATE TABLE plume.browser_annotations (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 asset_id bigint NOT NULL REFERENCES plume.assets(id) ON DELETE CASCADE,
 actor_id bigint NOT NULL REFERENCES plume.users(id),
 page jsonb NOT NULL,
 fields jsonb NOT NULL,
 snapshot_digest text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,snapshot_digest)
);
CREATE INDEX browser_annotations_workspace_idx ON plume.browser_annotations(workspace_id,id DESC);
CREATE INDEX browser_annotations_asset_idx ON plume.browser_annotations(workspace_id,asset_id,id DESC);
