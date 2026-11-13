CREATE TABLE plume.classification_knowledge (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 name text NOT NULL CHECK(char_length(name) BETWEEN 1 AND 120),
 source_url text NOT NULL DEFAULT '',
 content text NOT NULL CHECK(octet_length(content) BETWEEN 1 AND 32768),
 enabled boolean NOT NULL DEFAULT false,
 allow_ai boolean NOT NULL DEFAULT false,
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 digest text NOT NULL CHECK(char_length(digest)=64),
 created_by bigint NOT NULL REFERENCES plume.users(id),
 updated_by bigint NOT NULL REFERENCES plume.users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(workspace_id,id)
);
ALTER TABLE plume.classification_ai_suggestions ADD COLUMN knowledge_context jsonb NOT NULL DEFAULT '[]';
