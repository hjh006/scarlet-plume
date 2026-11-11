ALTER TABLE plume.runtime_findings DROP CONSTRAINT runtime_findings_rule_check;
ALTER TABLE plume.runtime_findings ADD CONSTRAINT runtime_findings_rule_check CHECK (rule IN (
 'unregistered_success','deprecated_success','sensitive_response','credential_response','missing_auth',
 'subject_bulk_access','error_spike','off_hours','sql_injection_signal','xss_signal','path_traversal_signal'));
ALTER TABLE plume.runtime_findings DROP CONSTRAINT runtime_findings_baseline_scan_id_check;
ALTER TABLE plume.runtime_findings ADD CONSTRAINT runtime_findings_baseline_scan_id_check CHECK (baseline_scan_id >= 0);
ALTER TABLE plume.runtime_findings ADD COLUMN detection jsonb NOT NULL DEFAULT '{}';
ALTER TABLE plume.runtime_findings ADD COLUMN retest_policy_id bigint CHECK (retest_policy_id >= 0);

CREATE TABLE plume.runtime_detection_policies (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 rule text NOT NULL CHECK (rule IN ('sensitive_response','credential_response','missing_auth','subject_bulk_access','error_spike','off_hours','sql_injection_signal','xss_signal','path_traversal_signal')),
 revision integer NOT NULL CHECK (revision > 0),
 enabled boolean NOT NULL,
 config jsonb NOT NULL,
 actor_id bigint NOT NULL REFERENCES plume.users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(workspace_id,rule,revision)
);

CREATE TABLE plume.runtime_detection_samples (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 source_id bigint NOT NULL REFERENCES plume.runtime_security_sources(id) ON DELETE CASCADE,
 batch_id text NOT NULL,
 ordinal integer NOT NULL,
 observed_at timestamptz NOT NULL,
 observation jsonb NOT NULL,
 UNIQUE(source_id,batch_id,ordinal)
);
CREATE INDEX runtime_detection_samples_window_idx ON plume.runtime_detection_samples(source_id,observed_at DESC,id DESC);
