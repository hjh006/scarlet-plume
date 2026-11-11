CREATE TABLE plume.catalog_classification_assessments (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 source_id bigint NOT NULL REFERENCES plume.discovery_sources(id) ON DELETE CASCADE,
 source_revision bigint NOT NULL,
 run_id bigint NOT NULL REFERENCES plume.discovery_runs(id) ON DELETE CASCADE,
 asset_id bigint NOT NULL REFERENCES plume.assets(id) ON DELETE CASCADE,
 classification_revision bigint NOT NULL,
 fact_digest text NOT NULL,
 assessment jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(run_id,asset_id)
);
CREATE TABLE plume.catalog_check_bindings (
 check_id bigint PRIMARY KEY REFERENCES plume.checks(id) ON DELETE CASCADE,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 source_id bigint NOT NULL REFERENCES plume.discovery_sources(id) ON DELETE CASCADE,
 asset_id bigint NOT NULL REFERENCES plume.assets(id) ON DELETE CASCADE,
 source_revision bigint NOT NULL,
 previous_check_id bigint REFERENCES plume.checks(id)
);
CREATE INDEX catalog_check_bindings_source_idx ON plume.catalog_check_bindings(workspace_id,source_id,asset_id,check_id);
CREATE TABLE plume.catalog_check_evidence (
 evidence_id bigint PRIMARY KEY REFERENCES plume.check_evidence(id) ON DELETE CASCADE,
 assessment_id bigint NOT NULL REFERENCES plume.catalog_classification_assessments(id) ON DELETE CASCADE,
 cycle_id bigint NOT NULL,
 fact_digest text NOT NULL,
 UNIQUE(assessment_id,cycle_id,evidence_id)
);
CREATE INDEX catalog_assessment_workspace_idx ON plume.catalog_classification_assessments(workspace_id,asset_id,id DESC);
ALTER TABLE plume.discovery_sources ADD COLUMN lease_rules jsonb NOT NULL DEFAULT '[]';
