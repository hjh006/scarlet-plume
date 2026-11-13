CREATE TABLE plume.data_security_cases (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 actor_id bigint NOT NULL REFERENCES plume.users(id),
 title text NOT NULL CHECK(char_length(title) BETWEEN 1 AND 120),
 scope jsonb NOT NULL,
 snapshot jsonb NOT NULL,
 snapshot_digest text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(workspace_id,id)
);
CREATE INDEX data_security_cases_latest_idx ON plume.data_security_cases(workspace_id,id DESC);
CREATE TABLE plume.data_security_case_notes (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL,
 case_id bigint NOT NULL,
 actor_id bigint NOT NULL REFERENCES plume.users(id),
 kind text NOT NULL CHECK(kind IN ('feedback','ai_reference')),
 snapshot jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(workspace_id,case_id) REFERENCES plume.data_security_cases(workspace_id,id) ON DELETE CASCADE
);
CREATE INDEX data_security_case_notes_idx ON plume.data_security_case_notes(case_id,id);
