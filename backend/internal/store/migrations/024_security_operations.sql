CREATE TABLE plume.operations_sources (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    actor_id bigint NOT NULL REFERENCES plume.users(id),
    name text NOT NULL CHECK(char_length(name) BETWEEN 1 AND 80),
    kind text NOT NULL CHECK(kind IN ('security_events','postgres_control')),
    owner_team text NOT NULL CHECK(char_length(owner_team) BETWEEN 1 AND 80),
    scope jsonb NOT NULL DEFAULT '{}',
    token_digest text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_seen timestamptz,
    revoked_at timestamptz,
    UNIQUE(workspace_id,id)
);
CREATE UNIQUE INDEX operations_source_name_idx ON plume.operations_sources(workspace_id,kind,name) WHERE revoked_at IS NULL;

CREATE TABLE plume.operations_security_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    source_id bigint NOT NULL,
    event_id text NOT NULL,
    digest text NOT NULL,
    observed_at timestamptz NOT NULL,
    category text NOT NULL,
    rule text NOT NULL,
    scope_key text NOT NULL,
    severity text NOT NULL,
    asset_id bigint NOT NULL,
    check_id bigint NOT NULL,
    metadata jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(source_id,event_id),
    FOREIGN KEY(workspace_id,source_id) REFERENCES plume.operations_sources(workspace_id,id) ON DELETE CASCADE,
    FOREIGN KEY(workspace_id,asset_id) REFERENCES plume.assets(workspace_id,id) ON DELETE CASCADE,
    FOREIGN KEY(workspace_id,check_id) REFERENCES plume.checks(workspace_id,id) ON DELETE CASCADE
);
CREATE INDEX operations_security_event_latest_idx ON plume.operations_security_events(workspace_id,id DESC);
CREATE INDEX operations_security_event_cycle_idx ON plume.operations_security_events(source_id,asset_id,rule,scope_key,id DESC);
CREATE INDEX operations_security_event_check_idx ON plume.operations_security_events(check_id,observed_at DESC,id DESC);

CREATE TABLE plume.control_actions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    actor_id bigint NOT NULL REFERENCES plume.users(id),
    approved_by bigint REFERENCES plume.users(id),
    kind text NOT NULL CHECK(kind IN ('block_api','revoke_select')),
    runtime_source_id bigint REFERENCES plume.runtime_security_sources(id),
    operations_source_id bigint,
    asset_id bigint,
    check_id bigint,
    spec jsonb NOT NULL,
    reason text NOT NULL CHECK(char_length(reason) BETWEEN 1 AND 400),
    status text NOT NULL DEFAULT 'proposed' CHECK(status IN ('proposed','approved','applied','failed','rollback_requested','rolled_back')),
    revision bigint NOT NULL DEFAULT 1,
    result jsonb,
    lease_id text,
    lease_until timestamptz,
    receipt_digest text,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK(approved_by IS NULL OR approved_by<>actor_id),
    CHECK((kind='block_api' AND runtime_source_id IS NOT NULL AND operations_source_id IS NULL)
       OR (kind='revoke_select' AND operations_source_id IS NOT NULL AND runtime_source_id IS NULL)),
    FOREIGN KEY(workspace_id,operations_source_id) REFERENCES plume.operations_sources(workspace_id,id) ON DELETE CASCADE,
    FOREIGN KEY(workspace_id,asset_id) REFERENCES plume.assets(workspace_id,id) ON DELETE CASCADE,
    FOREIGN KEY(workspace_id,check_id) REFERENCES plume.checks(workspace_id,id) ON DELETE CASCADE,
    UNIQUE(workspace_id,id)
);
CREATE INDEX control_actions_workspace_idx ON plume.control_actions(workspace_id,id DESC);
CREATE INDEX control_actions_runtime_idx ON plume.control_actions(runtime_source_id,id) WHERE kind='block_api';
CREATE TABLE plume.control_action_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL,
    action_id bigint NOT NULL,
    actor_id bigint NOT NULL REFERENCES plume.users(id),
    action text NOT NULL,
    note text NOT NULL DEFAULT '',
    automated boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY(workspace_id,action_id) REFERENCES plume.control_actions(workspace_id,id) ON DELETE CASCADE
);
