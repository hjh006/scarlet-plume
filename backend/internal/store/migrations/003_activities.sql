ALTER TABLE plume.assets
    ADD COLUMN category text NOT NULL DEFAULT 'unknown'
        CHECK (category IN ('unknown', 'business', 'personal', 'sensitive_personal')),
    ADD COLUMN business_level text NOT NULL DEFAULT 'unknown'
        CHECK (business_level IN ('unknown', 'internal', 'confidential')),
    ADD COLUMN statutory_category text NOT NULL DEFAULT 'unknown'
        CHECK (statutory_category IN ('unknown', 'general', 'important', 'core')),
    ADD COLUMN classification_basis text NOT NULL DEFAULT '' CHECK (char_length(classification_basis) <= 400),
    ADD COLUMN description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 600);

CREATE TABLE plume.activities (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    asset_id bigint NOT NULL,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    kind text NOT NULL CHECK (kind IN ('internal', 'sharing', 'ai', 'cross_border')),
    purpose text NOT NULL CHECK (char_length(purpose) BETWEEN 1 AND 400),
    recipient text NOT NULL DEFAULT '' CHECK (char_length(recipient) <= 100),
    region text NOT NULL DEFAULT '' CHECK (char_length(region) <= 100),
    retention_policy text NOT NULL DEFAULT '' CHECK (char_length(retention_policy) <= 400),
    transfer_path text NOT NULL DEFAULT '' CHECK (char_length(transfer_path) <= 400),
    safeguards text NOT NULL DEFAULT '' CHECK (char_length(safeguards) <= 400),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (kind <> 'cross_border' OR (btrim(recipient) <> '' AND btrim(region) <> '')),
    UNIQUE (workspace_id, asset_id, id),
    FOREIGN KEY (workspace_id, asset_id) REFERENCES plume.assets(workspace_id, id)
);

CREATE INDEX activities_workspace_page_idx ON plume.activities (workspace_id, id DESC);

ALTER TABLE plume.checks ADD COLUMN activity_id bigint;
ALTER TABLE plume.checks ADD FOREIGN KEY (workspace_id, asset_id, activity_id)
    REFERENCES plume.activities(workspace_id, asset_id, id);
CREATE INDEX checks_activity_page_idx ON plume.checks (workspace_id, activity_id, id DESC)
    WHERE activity_id IS NOT NULL;
