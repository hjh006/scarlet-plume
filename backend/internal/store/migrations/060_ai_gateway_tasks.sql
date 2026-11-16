CREATE TABLE plume.ai_gateway_tasks (
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 id text NOT NULL,
 parent_task_id text NOT NULL DEFAULT '',
 root_task_id text NOT NULL,
 depth integer NOT NULL CHECK(depth BETWEEN 0 AND 4),
 application_id text NOT NULL,
 principal_id text NOT NULL,
 credential_id bigint NOT NULL REFERENCES plume.ai_gateway_credentials(id),
 configuration jsonb NOT NULL,
 scope_digest text NOT NULL CHECK(scope_digest ~ '^[a-f0-9]{64}$'),
 status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','cancelled','completed')),
 revision bigint NOT NULL DEFAULT 1,
 steps_used bigint NOT NULL DEFAULT 0 CHECK(steps_used>=0),
 reserved_micros bigint NOT NULL DEFAULT 0 CHECK(reserved_micros>=0),
 spent_micros bigint NOT NULL DEFAULT 0 CHECK(spent_micros>=0),
 observed_categories jsonb NOT NULL DEFAULT '[]',
 sources jsonb NOT NULL DEFAULT '[]',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(workspace_id,id)
);
CREATE INDEX ai_gateway_tasks_parent ON plume.ai_gateway_tasks(workspace_id,parent_task_id);
CREATE TABLE plume.ai_gateway_task_steps (
 id bigint GENERATED ALWAYS AS IDENTITY,
 workspace_id bigint NOT NULL,
 task_id text NOT NULL,
 step_id text NOT NULL,
 step_digest text NOT NULL CHECK(step_digest ~ '^[a-f0-9]{64}$'),
 kind text NOT NULL CHECK(kind IN ('model','rag','tool')),
 model_id text NOT NULL DEFAULT '',
 tool_id text NOT NULL DEFAULT '',
 document_ids jsonb NOT NULL DEFAULT '[]',
 categories jsonb NOT NULL DEFAULT '[]',
 status text NOT NULL DEFAULT 'admitted' CHECK(status IN ('admitted','completed')),
 completion_hash text NOT NULL DEFAULT '',
 sources jsonb NOT NULL DEFAULT '[]',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 completed_at timestamptz,
 PRIMARY KEY(workspace_id,task_id,step_id),
 UNIQUE(workspace_id,step_id),
 FOREIGN KEY(workspace_id,task_id) REFERENCES plume.ai_gateway_tasks(workspace_id,id) ON DELETE CASCADE
);
CREATE INDEX ai_gateway_task_steps_recent ON plume.ai_gateway_task_steps(workspace_id,task_id,id DESC);
