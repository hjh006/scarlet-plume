CREATE TABLE plume.security_platform_snapshots (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    actor_id bigint NOT NULL REFERENCES plume.users(id),
    digest text NOT NULL CHECK (digest ~ '^[0-9a-f]{64}$'),
    snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX security_platform_snapshots_workspace_idx ON plume.security_platform_snapshots(workspace_id, id DESC);

CREATE TABLE plume.security_network_bindings (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    configuration jsonb NOT NULL CHECK (jsonb_typeof(configuration) = 'object'),
    actor_id bigint NOT NULL REFERENCES plume.users(id),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id)
);
CREATE INDEX security_network_bindings_workspace_idx ON plume.security_network_bindings(workspace_id, id);

CREATE TABLE plume.security_network_changes (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    binding_id bigint NOT NULL,
    binding_revision bigint NOT NULL,
    proposal jsonb NOT NULL CHECK (jsonb_typeof(proposal) = 'object'),
    status text NOT NULL DEFAULT 'pending_review' CHECK (status IN ('pending_review', 'accepted', 'rejected')),
    created_by bigint NOT NULL REFERENCES plume.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    reviewed_by bigint REFERENCES plume.users(id),
    review_note text NOT NULL DEFAULT '',
    reviewed_at timestamptz,
    FOREIGN KEY (workspace_id, binding_id) REFERENCES plume.security_network_bindings(workspace_id, id) ON DELETE CASCADE,
    CHECK (reviewed_by IS NULL OR reviewed_by <> created_by)
);
CREATE INDEX security_network_changes_workspace_idx ON plume.security_network_changes(workspace_id, id DESC);
