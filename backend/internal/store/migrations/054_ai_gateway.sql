CREATE TABLE plume.ai_gateway_settings (
    workspace_id bigint PRIMARY KEY REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    revision bigint NOT NULL DEFAULT 0,
    configuration jsonb NOT NULL DEFAULT '{"enabled":false,"applications":[],"providers":[]}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE plume.ai_gateway_accounts (
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    application_id text NOT NULL,
    reserved_micros bigint NOT NULL DEFAULT 0 CHECK (reserved_micros >= 0),
    spent_micros bigint NOT NULL DEFAULT 0 CHECK (spent_micros >= 0),
    PRIMARY KEY(workspace_id, application_id)
);
CREATE TABLE plume.ai_gateway_credentials (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    application_id text NOT NULL,
    name text NOT NULL,
    prefix text NOT NULL,
    token_hash text NOT NULL UNIQUE CHECK (token_hash ~ '^[a-f0-9]{64}$'),
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY(workspace_id,application_id) REFERENCES plume.ai_gateway_accounts(workspace_id,application_id)
);
CREATE TABLE plume.ai_gateway_windows (
    workspace_id bigint NOT NULL,
    application_id text NOT NULL,
    window_start timestamptz NOT NULL,
    requests bigint NOT NULL CHECK(requests >= 0),
    tokens bigint NOT NULL CHECK(tokens >= 0),
    PRIMARY KEY(workspace_id,application_id,window_start),
    FOREIGN KEY(workspace_id,application_id) REFERENCES plume.ai_gateway_accounts(workspace_id,application_id) ON DELETE CASCADE
);
CREATE TABLE plume.ai_gateway_reservations (
    id text PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    application_id text NOT NULL,
    credential_id bigint NOT NULL,
    request_id text NOT NULL,
    request_hash text NOT NULL,
    model_id text NOT NULL,
    reserved_micros bigint NOT NULL CHECK(reserved_micros >= 0),
    reserved_tokens bigint NOT NULL CHECK(reserved_tokens >= 0),
    input_price bigint NOT NULL CHECK(input_price >= 0),
    output_price bigint NOT NULL CHECK(output_price >= 0),
    authorized jsonb NOT NULL,
    window_start timestamptz NOT NULL,
    state text NOT NULL DEFAULT 'reserved' CHECK(state IN ('reserved','settled')),
    actual_micros bigint CHECK(actual_micros >= 0),
    actual_tokens bigint CHECK(actual_tokens >= 0),
    settlement_hash text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    settled_at timestamptz,
    UNIQUE(workspace_id,request_id),
    FOREIGN KEY(workspace_id,application_id) REFERENCES plume.ai_gateway_accounts(workspace_id,application_id)
);
CREATE INDEX ai_gateway_reservations_pending ON plume.ai_gateway_reservations(workspace_id,created_at) WHERE state='reserved';
CREATE TABLE plume.ai_gateway_audit_heads (
    workspace_id bigint PRIMARY KEY REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    sequence bigint NOT NULL DEFAULT 0,
    head_hash text NOT NULL DEFAULT ''
);
CREATE TABLE plume.ai_gateway_audit (
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    sequence bigint NOT NULL,
    actor_id bigint NOT NULL,
    action text NOT NULL,
    target text NOT NULL,
    payload text NOT NULL,
    occurred_at timestamptz NOT NULL,
    previous_hash text NOT NULL,
    hash text NOT NULL,
    PRIMARY KEY(workspace_id,sequence)
);
CREATE FUNCTION plume.ai_gateway_audit_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    -- Workspace erasure is the sole retention boundary; individual events cannot be changed.
    IF TG_OP='DELETE' AND NOT EXISTS(SELECT 1 FROM plume.workspaces WHERE id=OLD.workspace_id) THEN RETURN OLD; END IF;
    RAISE EXCEPTION 'AI gateway audit is append only' USING ERRCODE='42501';
END $$;
CREATE TRIGGER ai_gateway_audit_immutable BEFORE UPDATE OR DELETE ON plume.ai_gateway_audit FOR EACH ROW EXECUTE FUNCTION plume.ai_gateway_audit_immutable();
CREATE TABLE plume.ai_gateway_nodes (
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    id text NOT NULL,
    status text NOT NULL CHECK(status IN ('healthy','degraded','open')),
    failures integer NOT NULL DEFAULT 0 CHECK(failures >= 0),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(workspace_id,id)
);
