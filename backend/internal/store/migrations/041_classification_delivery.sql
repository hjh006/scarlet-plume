CREATE TABLE plume.classification_delivery_targets (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 name text NOT NULL CHECK(char_length(name) BETWEEN 1 AND 120),
 endpoint text NOT NULL,
 enabled boolean NOT NULL DEFAULT false,
 auto_events boolean NOT NULL DEFAULT false,
 min_severity text NOT NULL DEFAULT 'high' CHECK(min_severity IN('low','medium','high','critical')),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 token_digest text NOT NULL UNIQUE CHECK(char_length(token_digest)=64),
 created_by bigint NOT NULL REFERENCES plume.users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(workspace_id,id)
);
CREATE TABLE plume.classification_deliveries (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 target_id bigint NOT NULL,
 target_revision bigint NOT NULL,
 resource_kind text NOT NULL CHECK(resource_kind IN('classification_publication','security_event')),
 resource_id bigint NOT NULL CHECK(resource_id>0),
 status text NOT NULL DEFAULT 'queued' CHECK(status IN('queued','delivering','accepted','failed','cancelled')),
 payload jsonb NOT NULL DEFAULT '{}',
 payload_digest text NOT NULL CHECK(char_length(payload_digest)=64),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),
 lease_id text NOT NULL DEFAULT '',
 lease_until timestamptz,
 remote_ref text NOT NULL DEFAULT '',
 error_code text NOT NULL DEFAULT '',
 created_by bigint NOT NULL REFERENCES plume.users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 delivered_at timestamptz,
 FOREIGN KEY(workspace_id,target_id) REFERENCES plume.classification_delivery_targets(workspace_id,id),
 UNIQUE(workspace_id,target_id,target_revision,resource_kind,resource_id)
);
CREATE INDEX classification_deliveries_queue_idx ON plume.classification_deliveries(target_id,status,id);
