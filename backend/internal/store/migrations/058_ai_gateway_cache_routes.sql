CREATE TABLE plume.ai_gateway_cache (
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 application_id text NOT NULL,
 scope_hash text NOT NULL CHECK(scope_hash ~ '^[a-f0-9]{64}$'),
 key_version text NOT NULL,
 ciphertext bytea NOT NULL,
 size_bytes bigint NOT NULL CHECK(size_bytes > 0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 last_used_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL,
 PRIMARY KEY(workspace_id,application_id,scope_hash),
 FOREIGN KEY(workspace_id,application_id) REFERENCES plume.ai_gateway_accounts(workspace_id,application_id) ON DELETE CASCADE
);
CREATE INDEX ai_gateway_cache_eviction ON plume.ai_gateway_cache(workspace_id,application_id,last_used_at,scope_hash);
ALTER TABLE plume.ai_gateway_reservations ADD COLUMN parent_request_id text NOT NULL DEFAULT '', ADD COLUMN root_model_id text NOT NULL DEFAULT '', ADD COLUMN attempt integer NOT NULL DEFAULT 0, ADD COLUMN previous_reservation_id text NOT NULL DEFAULT '', ADD COLUMN last_audit jsonb;
CREATE UNIQUE INDEX ai_gateway_attempt_unique ON plume.ai_gateway_reservations(workspace_id,parent_request_id,attempt) WHERE parent_request_id<>'';
CREATE TABLE plume.ai_gateway_route_health (
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 provider_id text NOT NULL,
 failures integer NOT NULL DEFAULT 0 CHECK(failures>=0),
 open_until timestamptz,
 last_outcome text NOT NULL DEFAULT '',
 PRIMARY KEY(workspace_id,provider_id)
);
ALTER TABLE plume.ai_gateway_nodes ADD COLUMN capabilities jsonb NOT NULL DEFAULT '[]'::jsonb, ADD COLUMN version text NOT NULL DEFAULT '';
