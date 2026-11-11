CREATE TABLE plume.discovery_sources (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 source text NOT NULL, provider text NOT NULL CHECK(provider IN ('postgresql','mysql','mariadb','mongodb','openmetadata')),
 system text NOT NULL, database_name text NOT NULL, scopes jsonb NOT NULL DEFAULT '[]',
 service text NOT NULL DEFAULT '', interval_minutes integer NOT NULL CHECK(interval_minutes BETWEEN 1 AND 1440),
 enabled boolean NOT NULL DEFAULT true, revision bigint NOT NULL DEFAULT 1,
 token_hash text NOT NULL UNIQUE, created_by bigint NOT NULL REFERENCES plume.users(id),
 requested_mode text CHECK(requested_mode IN ('collect','test')), next_run_at timestamptz NOT NULL DEFAULT now(),
 lease_id text, lease_until timestamptz, lease_revision bigint,
 latest_run_id bigint, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workspace_id, source)
);
CREATE TABLE plume.discovery_runs (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 source_id bigint NOT NULL REFERENCES plume.discovery_sources(id) ON DELETE CASCADE,
 source_revision bigint NOT NULL, run_ref text NOT NULL, mode text NOT NULL,
 status text NOT NULL CHECK(status IN ('success','error')), error_code text NOT NULL DEFAULT '',
 observed_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 coverage jsonb NOT NULL DEFAULT '{}', payload_digest text NOT NULL,
 UNIQUE(source_id,run_ref)
);
ALTER TABLE plume.discovery_sources ADD CONSTRAINT discovery_source_latest_fk FOREIGN KEY(latest_run_id) REFERENCES plume.discovery_runs(id);
CREATE TABLE plume.discovery_rule_versions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 rule_key text NOT NULL, version integer NOT NULL, name text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('keyword','regexp','dictionary')), target text NOT NULL CHECK(target IN ('metadata','content')),
 category text NOT NULL, expression text NOT NULL DEFAULT '', dictionary jsonb NOT NULL DEFAULT '[]',
 status text NOT NULL CHECK(status IN ('draft','published','retired')), created_by bigint NOT NULL REFERENCES plume.users(id),
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(workspace_id,rule_key,version)
);
CREATE UNIQUE INDEX discovery_rule_published_idx ON plume.discovery_rule_versions(workspace_id,rule_key) WHERE status='published';
CREATE TABLE plume.catalog_entries (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 asset_id bigint NOT NULL REFERENCES plume.assets(id) ON DELETE CASCADE,
 kind text NOT NULL CHECK(kind IN ('business','application','database','table','field')),
 parent_ref text NOT NULL DEFAULT '', source text NOT NULL, source_ref text NOT NULL,
 source_id bigint REFERENCES plume.discovery_sources(id), run_id bigint REFERENCES plume.discovery_runs(id),
 source_revision bigint, system text NOT NULL DEFAULT '', database_name text NOT NULL DEFAULT '', schema_name text NOT NULL DEFAULT '', table_name text NOT NULL DEFAULT '', field_name text NOT NULL DEFAULT '',
 metadata jsonb NOT NULL DEFAULT '{}', confirmed_metadata jsonb NOT NULL DEFAULT '{}', conflicts jsonb NOT NULL DEFAULT '{}',
 revision bigint NOT NULL DEFAULT 1, observed_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,source,source_ref), UNIQUE(workspace_id,asset_id)
);
CREATE TABLE plume.catalog_history (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 entry_id bigint NOT NULL REFERENCES plume.catalog_entries(id) ON DELETE CASCADE,
 revision bigint NOT NULL, origin text NOT NULL, actor_id bigint REFERENCES plume.users(id),
 metadata jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(entry_id,revision)
);
CREATE TABLE plume.discovery_rule_hits (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 run_id bigint NOT NULL REFERENCES plume.discovery_runs(id) ON DELETE CASCADE,
 asset_id bigint NOT NULL REFERENCES plume.assets(id) ON DELETE CASCADE,
 rule_key text NOT NULL, rule_version integer NOT NULL, category text NOT NULL,
 method text NOT NULL, match_count bigint NOT NULL CHECK(match_count>0), scanned_count bigint NOT NULL CHECK(scanned_count>=0),
 scope jsonb NOT NULL, UNIQUE(run_id,asset_id,rule_key,rule_version,method)
);
CREATE TABLE plume.cmdb_imports (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 source text NOT NULL, batch_id text NOT NULL, payload_digest text NOT NULL,
 imported_count integer NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(workspace_id,source,batch_id)
);
CREATE INDEX catalog_entries_workspace_idx ON plume.catalog_entries(workspace_id,id);
CREATE INDEX discovery_runs_source_idx ON plume.discovery_runs(workspace_id,source_id,id DESC);
CREATE INDEX discovery_rule_hits_workspace_idx ON plume.discovery_rule_hits(workspace_id,asset_id,run_id);
