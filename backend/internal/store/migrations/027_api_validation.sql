CREATE TABLE plume.api_validation_plans (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 source_id bigint NOT NULL REFERENCES plume.operations_sources(id) ON DELETE CASCADE,
 created_by bigint NOT NULL REFERENCES plume.users(id),
 name text NOT NULL,
 editor_ids bigint[] NOT NULL DEFAULT '{}',
 service text NOT NULL,
 method text NOT NULL DEFAULT 'GET' CHECK(method='GET'),
 route text NOT NULL,
 target_origin text NOT NULL,
 manifest_ref text NOT NULL,
 rules text[] NOT NULL,
 revision bigint NOT NULL DEFAULT 1,
 state text NOT NULL DEFAULT 'draft' CHECK(state IN ('draft','approved','disabled')),
 approved_by bigint REFERENCES plume.users(id),
 approved_at timestamptz,
 interval_minutes integer NOT NULL DEFAULT 60 CHECK(interval_minutes BETWEEN 5 AND 43200),
 next_run_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 run_requested boolean NOT NULL DEFAULT false,
 request_mode text NOT NULL DEFAULT 'validation' CHECK(request_mode IN ('validation','connection')),
 lease_ref text,
 lease_expires_at timestamptz,
 lease_revision bigint,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX api_validation_claim_idx ON plume.api_validation_plans(source_id,next_run_at,id) WHERE state='approved';
CREATE TABLE plume.api_validation_runs (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 plan_id bigint NOT NULL REFERENCES plume.api_validation_plans(id) ON DELETE CASCADE,
 source_id bigint NOT NULL REFERENCES plume.operations_sources(id) ON DELETE CASCADE,
 plan_revision bigint NOT NULL,
 run_ref text NOT NULL UNIQUE,
 plan_snapshot jsonb NOT NULL,
 mode text NOT NULL DEFAULT 'validation' CHECK(mode IN ('validation','connection')),
 state text NOT NULL CHECK(state IN ('leased','complete','failed')),
 result jsonb,
 result_digest text,
 event_ids text[] NOT NULL DEFAULT '{}',
 check_ids bigint[] NOT NULL DEFAULT '{}',
 error_code text NOT NULL DEFAULT '',
 projection_state text NOT NULL DEFAULT 'pending' CHECK(projection_state IN ('pending','complete','error')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 completed_at timestamptz
);
CREATE INDEX api_validation_runs_page_idx ON plume.api_validation_runs(workspace_id,plan_id,id DESC);
CREATE TABLE plume.api_validation_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 plan_id bigint NOT NULL REFERENCES plume.api_validation_plans(id) ON DELETE CASCADE,
 actor_id bigint NOT NULL REFERENCES plume.users(id),
 action text NOT NULL,
 revision bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
