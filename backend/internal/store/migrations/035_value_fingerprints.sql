CREATE TABLE plume.value_fingerprints (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 source_id bigint NOT NULL REFERENCES plume.discovery_sources(id) ON DELETE CASCADE,
 run_id bigint NOT NULL REFERENCES plume.discovery_runs(id) ON DELETE CASCADE,
 asset_id bigint NOT NULL REFERENCES plume.assets(id) ON DELETE CASCADE,
 key_id text NOT NULL,scope text NOT NULL,version text NOT NULL CHECK(version='utf8_exact_v1'),
 digest text NOT NULL CHECK(digest ~ '^[a-f0-9]{64}$'),locator text NOT NULL,category text NOT NULL,
 UNIQUE(run_id,asset_id,key_id,scope,version,digest,locator,category)
);
CREATE INDEX value_fingerprints_lookup_idx ON plume.value_fingerprints(workspace_id,key_id,scope,version,digest,id DESC);
