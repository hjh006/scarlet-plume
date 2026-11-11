ALTER TABLE plume.assets
    ADD COLUMN classification_origin text NOT NULL DEFAULT 'manual'
        CHECK (classification_origin IN ('manual', 'automatic'));

ALTER TABLE plume.asset_classification_history
    ADD COLUMN origin text NOT NULL DEFAULT 'manual'
        CHECK (origin IN ('manual', 'automatic'));

ALTER TABLE plume.postgres_asset_inspections
    ADD COLUMN classification jsonb
        CHECK (jsonb_typeof(classification) = 'object');
