CREATE TABLE plume.ai_identity_settings (
    workspace_id bigint PRIMARY KEY REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    revision bigint NOT NULL DEFAULT 0,
    enabled boolean NOT NULL DEFAULT false,
    issuer text NOT NULL DEFAULT '',
    client_id text NOT NULL DEFAULT '',
    client_secret_ref text NOT NULL DEFAULT '',
    scim_enabled boolean NOT NULL DEFAULT false,
    scim_token_hash text NOT NULL DEFAULT '',
    default_role text NOT NULL DEFAULT 'viewer' CHECK (default_role IN ('viewer','reviewer','operator')),
    group_roles jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE plume.ai_identity_users (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    user_id bigint NOT NULL UNIQUE REFERENCES plume.users(id) ON DELETE CASCADE,
    external_id text NOT NULL,
    user_name text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    active boolean NOT NULL DEFAULT true,
    revision bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, external_id),
    UNIQUE (workspace_id, user_name),
    UNIQUE (workspace_id, id)
);

CREATE UNIQUE INDEX ai_identity_user_name_folded ON plume.ai_identity_users(workspace_id, lower(user_name));

CREATE TABLE plume.ai_identity_groups (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    external_id text NOT NULL,
    display_name text NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, external_id),
    UNIQUE (workspace_id, id)
);

CREATE TABLE plume.ai_identity_group_members (
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    group_id bigint NOT NULL,
    identity_id bigint NOT NULL,
    PRIMARY KEY (workspace_id, group_id, identity_id),
    FOREIGN KEY (workspace_id, group_id) REFERENCES plume.ai_identity_groups(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, identity_id) REFERENCES plume.ai_identity_users(workspace_id, id) ON DELETE CASCADE
);

CREATE TABLE plume.ai_identity_login_states (
    state_hash text PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    settings_revision bigint NOT NULL,
    browser_hash text NOT NULL,
    nonce text NOT NULL,
    verifier text NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX ai_identity_login_states_expiry ON plume.ai_identity_login_states(expires_at);
