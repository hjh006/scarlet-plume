CREATE TABLE plume.ai_security_configurations (
 workspace_id bigint PRIMARY KEY REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 revision bigint NOT NULL DEFAULT 0 CHECK(revision>=0), configuration jsonb NOT NULL,
 updated_by bigint NOT NULL REFERENCES plume.users(id), updated_at timestamptz NOT NULL DEFAULT now(),
 published_revision bigint NOT NULL DEFAULT 0, published_configuration jsonb,
 published_digest text NOT NULL DEFAULT '', published_by bigint REFERENCES plume.users(id), published_at timestamptz
);
CREATE TABLE plume.ai_security_decisions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 actor_id bigint NOT NULL REFERENCES plume.users(id), configuration_revision bigint NOT NULL,
 decision jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ai_security_decisions_workspace ON plume.ai_security_decisions(workspace_id,id DESC);
CREATE TABLE plume.ai_security_evaluations (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 actor_id bigint NOT NULL REFERENCES plume.users(id), configuration_revision bigint NOT NULL,
 catalog_digest text NOT NULL, status text NOT NULL CHECK(status IN ('passed','failed')),
 result jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ai_security_evaluations_workspace ON plume.ai_security_evaluations(workspace_id,id DESC);
CREATE TABLE plume.ai_security_approvals (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 configuration_revision bigint NOT NULL, catalog_digest text NOT NULL, request_digest text NOT NULL,
 request_metadata jsonb NOT NULL, requested_by bigint NOT NULL REFERENCES plume.users(id),
 reviewed_by bigint REFERENCES plume.users(id), status text NOT NULL DEFAULT 'proposed' CHECK(status IN ('proposed','approved','rejected','invalidated')),
 note text NOT NULL DEFAULT '', expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), reviewed_at timestamptz
);
CREATE INDEX ai_security_approvals_workspace ON plume.ai_security_approvals(workspace_id,id DESC);
