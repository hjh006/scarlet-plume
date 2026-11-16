-- One durable nonce namespace for provider attempts and checked cache hits.
CREATE TABLE plume.ai_gateway_request_claims (
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 request_id text NOT NULL,
 parent_request_id text NOT NULL DEFAULT '',
 attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 4),
 application_id text NOT NULL,
 principal_id text NOT NULL,
 request_digest text NOT NULL CHECK(request_digest ~ '^[a-f0-9]{64}$'),
 source text NOT NULL CHECK(source IN ('provider','cache')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(workspace_id,request_id)
);
CREATE UNIQUE INDEX ai_gateway_claim_parent_attempt ON plume.ai_gateway_request_claims(workspace_id,parent_request_id,attempt) WHERE parent_request_id<>'';
INSERT INTO plume.ai_gateway_request_claims(workspace_id,request_id,parent_request_id,attempt,application_id,principal_id,request_digest,source)
 SELECT workspace_id,request_id,parent_request_id,attempt,application_id,authorized->>'principalId',authorized->>'requestDigest','provider' FROM plume.ai_gateway_reservations;
