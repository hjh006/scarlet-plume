ALTER TABLE plume.workspaces
    ADD COLUMN classification_standard_id text
        CHECK (classification_standard_id ~ '^[a-z0-9][a-z0-9-]{0,79}$');

ALTER TABLE plume.assets
    ADD COLUMN classification_standard jsonb
        CHECK (jsonb_typeof(classification_standard) = 'object');

ALTER TABLE plume.asset_classification_history
    ADD COLUMN standard_reference jsonb
        CHECK (jsonb_typeof(standard_reference) = 'object');
