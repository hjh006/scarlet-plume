ALTER TABLE plume.api_validation_plans ADD COLUMN probe_policy jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(probe_policy)='object');
