ALTER TABLE plume.discovery_sources DROP CONSTRAINT discovery_sources_provider_check;
ALTER TABLE plume.discovery_sources ADD CONSTRAINT discovery_sources_provider_check CHECK(provider IN ('postgresql','mysql','mariadb','mongodb','openmetadata','local_files'));
ALTER TABLE plume.catalog_entries DROP CONSTRAINT catalog_entries_kind_check;
ALTER TABLE plume.catalog_entries ADD CONSTRAINT catalog_entries_kind_check CHECK(kind IN ('business','application','database','table','field','file'));
CREATE TABLE plume.file_content_evidence (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 source_id bigint NOT NULL REFERENCES plume.discovery_sources(id) ON DELETE CASCADE,
 run_id bigint NOT NULL REFERENCES plume.discovery_runs(id) ON DELETE CASCADE,
 asset_id bigint NOT NULL REFERENCES plume.assets(id) ON DELETE CASCADE,
 file_ref text NOT NULL,evidence jsonb NOT NULL,
 UNIQUE(run_id,file_ref)
);
