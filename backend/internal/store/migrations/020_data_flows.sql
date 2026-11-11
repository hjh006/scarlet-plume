CREATE TABLE plume.data_flow_observations (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    source_id bigint NOT NULL REFERENCES plume.runtime_security_sources(id) ON DELETE CASCADE,
    batch_id text NOT NULL,
    ordinal integer NOT NULL,
    observed_at timestamptz NOT NULL,
    observation jsonb NOT NULL CHECK (jsonb_typeof(observation) = 'object'),
    UNIQUE (source_id, batch_id, ordinal)
);
CREATE INDEX data_flow_observations_source_idx ON plume.data_flow_observations(source_id, observed_at DESC, id DESC);
CREATE INDEX data_flow_observations_workspace_idx ON plume.data_flow_observations(workspace_id, observed_at DESC, id DESC);
CREATE TABLE plume.data_flow_snapshots (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    actor_id bigint NOT NULL REFERENCES plume.users(id),
    status text NOT NULL DEFAULT 'ready' CHECK(status IN ('ready', 'error')),
    origin text NOT NULL,
    scope jsonb NOT NULL,
    digest text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX data_flow_snapshots_workspace_idx ON plume.data_flow_snapshots(workspace_id, id DESC);
CREATE TABLE plume.data_flow_snapshot_status (
    workspace_id bigint PRIMARY KEY REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    status text NOT NULL CHECK(status IN ('ready','error')),
    last_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    error text NOT NULL DEFAULT ''
);
