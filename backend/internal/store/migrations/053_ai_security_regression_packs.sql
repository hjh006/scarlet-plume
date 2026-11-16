CREATE TABLE plume.ai_security_regression_packs (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 pack_id text NOT NULL, version text NOT NULL, digest text NOT NULL,
 pack jsonb NOT NULL, created_by bigint NOT NULL REFERENCES plume.users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,pack_id,version)
);
CREATE INDEX ai_security_regression_packs_workspace ON plume.ai_security_regression_packs(workspace_id,id DESC);
