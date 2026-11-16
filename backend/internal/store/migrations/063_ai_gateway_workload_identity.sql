CREATE TABLE plume.ai_gateway_workload_bindings (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    name text NOT NULL,
    application_id text NOT NULL,
    issuer text NOT NULL,
    audience text NOT NULL,
    subject text NOT NULL,
    jwks jsonb NOT NULL CHECK (jsonb_typeof(jwks)='object'),
    max_ttl_seconds bigint NOT NULL CHECK (max_ttl_seconds BETWEEN 1 AND 300),
    enabled boolean NOT NULL DEFAULT true,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(workspace_id,id),
    UNIQUE(workspace_id,issuer,audience,subject)
);
CREATE INDEX ai_gateway_workload_bindings_recent ON plume.ai_gateway_workload_bindings(workspace_id,id DESC);
ALTER TABLE plume.ai_gateway_credentials
    ADD COLUMN expires_at timestamptz,
    ADD COLUMN workload_binding_id bigint,
    ADD CONSTRAINT ai_gateway_credentials_workload FOREIGN KEY(workspace_id,workload_binding_id)
        REFERENCES plume.ai_gateway_workload_bindings(workspace_id,id),
    ADD CONSTRAINT ai_gateway_credentials_workload_expiry CHECK(workload_binding_id IS NULL OR expires_at IS NOT NULL);
CREATE INDEX ai_gateway_workload_credentials ON plume.ai_gateway_credentials(workspace_id,workload_binding_id) WHERE workload_binding_id IS NOT NULL;
CREATE INDEX ai_gateway_expiring_credentials ON plume.ai_gateway_credentials(workspace_id,expires_at,id) WHERE enabled AND expires_at IS NOT NULL;
CREATE TABLE plume.ai_gateway_workload_exchanges (
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    binding_id bigint NOT NULL,
    issuer_digest text NOT NULL CHECK(issuer_digest ~ '^[a-f0-9]{64}$'),
    jti_digest text NOT NULL CHECK(jti_digest ~ '^[a-f0-9]{64}$'),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(workspace_id,issuer_digest,jti_digest),
    FOREIGN KEY(workspace_id,binding_id) REFERENCES plume.ai_gateway_workload_bindings(workspace_id,id) ON DELETE CASCADE
);
CREATE INDEX ai_gateway_workload_exchanges_expiry ON plume.ai_gateway_workload_exchanges(workspace_id,expires_at);
