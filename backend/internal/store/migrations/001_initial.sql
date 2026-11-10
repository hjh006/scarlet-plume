CREATE TABLE plume.users (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username text NOT NULL UNIQUE,
    password_hash text NOT NULL
);

CREATE TABLE plume.workspaces (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug text NOT NULL UNIQUE,
    name text NOT NULL
);

CREATE TABLE plume.memberships (
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES plume.users(id) ON DELETE CASCADE,
    role text NOT NULL CHECK (role IN ('owner', 'operator', 'reviewer', 'viewer')),
    PRIMARY KEY (workspace_id, user_id)
);

CREATE INDEX memberships_user_idx ON plume.memberships (user_id, workspace_id);

CREATE TABLE plume.sessions (
    token_hash text PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES plume.users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL
);

CREATE TABLE plume.assets (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    name text NOT NULL,
    system text NOT NULL,
    owner_team text NOT NULL DEFAULT '',
    source_provider text,
    source text,
    source_ref text,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (
        (source_provider IS NULL AND source IS NULL AND source_ref IS NULL)
        OR (source_provider IS NOT NULL AND source IS NOT NULL AND source_ref IS NOT NULL)
    ),
    UNIQUE (workspace_id, source_provider, source, source_ref)
);

CREATE INDEX assets_workspace_page_idx ON plume.assets (workspace_id, id);
CREATE UNIQUE INDEX assets_manual_name_idx
    ON plume.assets (workspace_id, system, name) WHERE source_ref IS NULL;

CREATE TABLE plume.import_runs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    actor_id bigint NOT NULL REFERENCES plume.users(id),
    provider text NOT NULL,
    source text NOT NULL,
    batch_id text NOT NULL,
    payload_digest text NOT NULL,
    created_count integer NOT NULL DEFAULT 0 CHECK (created_count >= 0),
    existing_count integer NOT NULL DEFAULT 0 CHECK (existing_count >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, provider, source, batch_id)
);

CREATE INDEX import_runs_workspace_latest_idx ON plume.import_runs (workspace_id, id DESC);
