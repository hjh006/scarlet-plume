ALTER TABLE plume.assets
    ADD COLUMN classification_revision bigint NOT NULL DEFAULT 0 CHECK (classification_revision >= 0);

CREATE TABLE plume.asset_classification_history (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL,
    asset_id bigint NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    actor_id bigint NOT NULL REFERENCES plume.users(id),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    category text NOT NULL CHECK (category IN ('unknown', 'business', 'personal', 'sensitive_personal')),
    business_level text NOT NULL CHECK (business_level IN ('unknown', 'internal', 'confidential')),
    statutory_category text NOT NULL CHECK (statutory_category IN ('unknown', 'general', 'important', 'core')),
    basis text NOT NULL CHECK (char_length(basis) BETWEEN 1 AND 400),
    previous_category text NOT NULL CHECK (previous_category IN ('unknown', 'business', 'personal', 'sensitive_personal')),
    previous_business_level text NOT NULL CHECK (previous_business_level IN ('unknown', 'internal', 'confidential')),
    previous_statutory_category text NOT NULL CHECK (previous_statutory_category IN ('unknown', 'general', 'important', 'core')),
    previous_basis text NOT NULL CHECK (char_length(previous_basis) <= 400),
    UNIQUE (asset_id, revision),
    FOREIGN KEY (workspace_id, asset_id) REFERENCES plume.assets(workspace_id, id) ON DELETE CASCADE
);

CREATE INDEX asset_classification_history_workspace_idx
    ON plume.asset_classification_history (workspace_id, id DESC);
