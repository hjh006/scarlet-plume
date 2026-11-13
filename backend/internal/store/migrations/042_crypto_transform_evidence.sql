CREATE TABLE plume.crypto_transform_evidence (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 source_id bigint NOT NULL REFERENCES plume.runtime_security_sources(id) ON DELETE CASCADE,
 audit_id bigint NOT NULL REFERENCES plume.runtime_request_audit(id) ON DELETE CASCADE,
 event_id text NOT NULL,trace_ref text NOT NULL,observed_at timestamptz NOT NULL,
 input_key_id text NOT NULL,input_scope text NOT NULL,input_version text NOT NULL,input_digest text NOT NULL,
 output_key_id text NOT NULL,output_scope text NOT NULL,output_version text NOT NULL,output_digest text NOT NULL,
 evidence jsonb NOT NULL,
 UNIQUE(source_id,event_id)
);
CREATE INDEX crypto_transform_input_lookup_idx ON plume.crypto_transform_evidence(workspace_id,input_key_id,input_scope,input_version,input_digest);
CREATE INDEX crypto_transform_output_lookup_idx ON plume.crypto_transform_evidence(workspace_id,output_key_id,output_scope,output_version,output_digest);
