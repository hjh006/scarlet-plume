CREATE TABLE plume.classification_taxonomy (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    parent_id bigint,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
    description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 400),
    enabled boolean NOT NULL DEFAULT true,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_by bigint NOT NULL REFERENCES plume.users(id),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (workspace_id, id),
    FOREIGN KEY (workspace_id, parent_id) REFERENCES plume.classification_taxonomy(workspace_id,id),
    CHECK (parent_id IS DISTINCT FROM id)
);
ALTER TABLE plume.assets ADD COLUMN classification_taxonomy jsonb CHECK (jsonb_typeof(classification_taxonomy) = 'object');
ALTER TABLE plume.asset_classification_history ADD COLUMN taxonomy_reference jsonb CHECK (jsonb_typeof(taxonomy_reference) = 'object');

CREATE TABLE plume.classification_publications (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 120),
    note text NOT NULL DEFAULT '' CHECK (char_length(note) <= 1000),
    status text NOT NULL DEFAULT 'submitted' CHECK (status IN ('submitted','published','rejected')),
    created_by bigint NOT NULL REFERENCES plume.users(id),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    reviewed_by bigint REFERENCES plume.users(id),
    reviewed_at timestamptz,
    review_note text NOT NULL DEFAULT '' CHECK (char_length(review_note) <= 1000),
    snapshot_digest text NOT NULL,
    UNIQUE (workspace_id,id),
    CHECK ((status='submitted') = (reviewed_by IS NULL AND reviewed_at IS NULL)),
    CHECK (created_by IS DISTINCT FROM reviewed_by)
);
CREATE TABLE plume.classification_publication_items (
    workspace_id bigint NOT NULL,
    publication_id bigint NOT NULL,
    asset_id bigint NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    confirmed_by bigint NOT NULL REFERENCES plume.users(id),
    snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot) = 'object'),
    facts jsonb NOT NULL CHECK (jsonb_typeof(facts) = 'object'),
    PRIMARY KEY (publication_id,asset_id),
    FOREIGN KEY (workspace_id,publication_id) REFERENCES plume.classification_publications(workspace_id,id) ON DELETE CASCADE
);
CREATE INDEX classification_publications_workspace_idx ON plume.classification_publications(workspace_id,id DESC);
CREATE INDEX classification_publication_asset_idx ON plume.classification_publication_items(workspace_id,asset_id,publication_id DESC);
