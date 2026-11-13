CREATE TABLE plume.classification_ai_profiles (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 name text NOT NULL CHECK(char_length(name) BETWEEN 1 AND 80),
 base_url text NOT NULL CHECK(char_length(base_url) BETWEEN 1 AND 2048),
 protocol text NOT NULL CHECK(protocol IN ('chat_completions','responses')),
 model text NOT NULL CHECK(char_length(model) BETWEEN 1 AND 160),
 enabled boolean NOT NULL DEFAULT false,
 allow_metadata boolean NOT NULL DEFAULT false,
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 encrypted_api_key bytea NOT NULL DEFAULT '',
 created_by bigint NOT NULL REFERENCES plume.users(id),
 updated_by bigint NOT NULL REFERENCES plume.users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(workspace_id,id)
);
CREATE TABLE plume.classification_engine_settings (
 workspace_id bigint PRIMARY KEY REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 mode text NOT NULL CHECK(mode IN ('local','hybrid')),
 default_profile_id bigint,
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 updated_by bigint NOT NULL REFERENCES plume.users(id),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(workspace_id,default_profile_id) REFERENCES plume.classification_ai_profiles(workspace_id,id),
 CHECK((mode='local' AND default_profile_id IS NULL) OR (mode='hybrid' AND default_profile_id IS NOT NULL))
);
CREATE TABLE plume.classification_ai_suggestions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 asset_id bigint NOT NULL,
 asset_revision bigint NOT NULL CHECK(asset_revision>=0),
 profile_id bigint NOT NULL,
 profile_revision bigint NOT NULL CHECK(profile_revision>0),
 model text NOT NULL,
 category text NOT NULL CHECK(category IN ('unknown','business','personal','sensitive_personal')),
 business_level text NOT NULL CHECK(business_level IN ('unknown','internal','confidential')),
 rationale text NOT NULL CHECK(char_length(rationale) BETWEEN 1 AND 1000),
 created_by bigint NOT NULL REFERENCES plume.users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(workspace_id,asset_id) REFERENCES plume.assets(workspace_id,id) ON DELETE CASCADE,
 FOREIGN KEY(workspace_id,profile_id) REFERENCES plume.classification_ai_profiles(workspace_id,id)
);
CREATE INDEX classification_ai_suggestions_asset_idx ON plume.classification_ai_suggestions(workspace_id,asset_id,id DESC);
