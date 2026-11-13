CREATE TABLE plume.classification_training_models (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 name text NOT NULL CHECK(char_length(name) BETWEEN 1 AND 120),
 model jsonb NOT NULL,
 dataset jsonb NOT NULL,
 dataset_digest text NOT NULL CHECK(char_length(dataset_digest)=64),
 model_digest text NOT NULL CHECK(char_length(model_digest)=64),
 evaluation jsonb NOT NULL,
 created_by bigint NOT NULL REFERENCES plume.users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(workspace_id,id)
);
CREATE TABLE plume.classification_training_predictions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 model_id bigint NOT NULL,
 asset_id bigint NOT NULL,
 asset_revision bigint NOT NULL CHECK(asset_revision>=0),
 snapshot jsonb NOT NULL,
 created_by bigint NOT NULL REFERENCES plume.users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(workspace_id,model_id) REFERENCES plume.classification_training_models(workspace_id,id),
 FOREIGN KEY(workspace_id,asset_id) REFERENCES plume.assets(workspace_id,id) ON DELETE CASCADE
);
