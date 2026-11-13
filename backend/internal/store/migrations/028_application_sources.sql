CREATE TABLE plume.application_sources (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 actor_id bigint NOT NULL REFERENCES plume.users(id),
 name text NOT NULL CHECK(char_length(name) BETWEEN 1 AND 80),
 asset_ids bigint[] NOT NULL CHECK(cardinality(asset_ids) BETWEEN 1 AND 50),
 max_age_minutes integer NOT NULL CHECK(max_age_minutes BETWEEN 5 AND 43200),
 token_digest text NOT NULL UNIQUE CHECK(token_digest ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT now(), revoked_at timestamptz,
 latest_batch_id bigint,
 UNIQUE(workspace_id,id)
);
CREATE UNIQUE INDEX application_source_name_idx ON plume.application_sources(workspace_id,name) WHERE revoked_at IS NULL;
CREATE TABLE plume.application_batches (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 source_id bigint NOT NULL,
 batch_id text NOT NULL, payload_digest text NOT NULL,
 status text NOT NULL CHECK(status IN ('success','error')),
 observed_at timestamptz NOT NULL, items jsonb NOT NULL DEFAULT '[]',
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(source_id,batch_id), UNIQUE(source_id,id),
 FOREIGN KEY(workspace_id,source_id) REFERENCES plume.application_sources(workspace_id,id) ON DELETE CASCADE
);
ALTER TABLE plume.application_sources ADD CONSTRAINT application_latest_batch_fk FOREIGN KEY(id,latest_batch_id) REFERENCES plume.application_batches(source_id,id);
CREATE INDEX application_sources_workspace_idx ON plume.application_sources(workspace_id,id);
