ALTER TABLE plume.ai_gateway_credentials ADD COLUMN expiry_disabled_at timestamptz;
-- Unknown historical disabled rows retain an unknown reason.
CREATE TABLE plume.ai_gateway_task_execution_leases (
 workspace_id bigint NOT NULL,
 task_id text NOT NULL,
 credential_id bigint NOT NULL REFERENCES plume.ai_gateway_credentials(id),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 mode text NOT NULL DEFAULT 'initial' CHECK(mode IN ('initial','renewal','rebind')),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(workspace_id,task_id),
 FOREIGN KEY(workspace_id,task_id) REFERENCES plume.ai_gateway_tasks(workspace_id,id) ON DELETE CASCADE
);
INSERT INTO plume.ai_gateway_task_execution_leases(workspace_id,task_id,credential_id)
 SELECT workspace_id,id,credential_id FROM plume.ai_gateway_tasks;

ALTER TABLE plume.ai_security_approvals ADD COLUMN task_lease_digest text CHECK(task_lease_digest ~ '^[a-f0-9]{64}$');
-- Old task approvals lack the immutable execution identity pin. Preserve their
-- history, but do not invent a pin from the current lease and revive them.
UPDATE plume.ai_security_approvals SET status='invalidated'
 WHERE request_metadata->>'taskId'<>'' AND status IN ('proposed','approved');

CREATE TABLE plume.ai_gateway_task_lease_proposals (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 task_id text NOT NULL,
 operation_id text NOT NULL,
 intent_digest text NOT NULL,
 expected_digest text NOT NULL,
 expected_chain jsonb NOT NULL,
 changes jsonb NOT NULL,
 credential_plan jsonb NOT NULL,
 source_ids bigint[] NOT NULL,
 target_ids bigint[] NOT NULL,
 path_ids text[] NOT NULL,
 reason text NOT NULL,
 proposal_digest text NOT NULL,
 proposed_by bigint NOT NULL REFERENCES plume.users(id),
 reviewed_by bigint REFERENCES plume.users(id),
 note text NOT NULL DEFAULT '',
 status text NOT NULL DEFAULT 'proposed' CHECK(status IN ('proposed','applied','rejected','invalidated')),
 revision bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL,
 applied_at timestamptz,
 UNIQUE(workspace_id,operation_id)
);
CREATE INDEX ai_gateway_task_lease_proposals_page ON plume.ai_gateway_task_lease_proposals(workspace_id,task_id,id DESC);
CREATE INDEX ai_gateway_task_lease_proposals_sources ON plume.ai_gateway_task_lease_proposals USING gin(source_ids) WHERE status='proposed';
CREATE INDEX ai_gateway_task_lease_proposals_targets ON plume.ai_gateway_task_lease_proposals USING gin(target_ids) WHERE status='proposed';
CREATE INDEX ai_gateway_task_lease_proposals_paths ON plume.ai_gateway_task_lease_proposals USING gin(path_ids) WHERE status='proposed';
CREATE TABLE plume.ai_gateway_task_lease_changes (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
 task_id text NOT NULL,
 operation_id text NOT NULL,
 intent_digest text NOT NULL,
 mode text NOT NULL CHECK(mode IN ('renewal','rebind')),
 changed_task_ids text[] NOT NULL,
 actor_id bigint REFERENCES plume.users(id),
 actor_credential_id bigint REFERENCES plume.ai_gateway_credentials(id),
 proposal_id bigint REFERENCES plume.ai_gateway_task_lease_proposals(id),
 reason text NOT NULL,
 before_digest text NOT NULL,
 after_digest text NOT NULL,
 changes jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(workspace_id,operation_id)
);
CREATE INDEX ai_gateway_task_lease_changes_page ON plume.ai_gateway_task_lease_changes(workspace_id,task_id,id DESC);
ALTER TABLE plume.ai_gateway_task_steps DROP CONSTRAINT ai_gateway_task_steps_status_check;
ALTER TABLE plume.ai_gateway_task_steps ADD CONSTRAINT ai_gateway_task_steps_status_check CHECK(status IN ('admitted','completed','closed'));
ALTER TABLE plume.ai_gateway_task_steps ADD COLUMN closure_evidence jsonb;
ALTER TABLE plume.ai_gateway_task_steps ADD COLUMN lease_chain_digest text;
ALTER TABLE plume.ai_gateway_task_steps ADD COLUMN lease_snapshot jsonb;
-- Legacy admitted steps have no execution-epoch proof. They remain visible and
-- block switching; migration never invents a current epoch for them.
CREATE INDEX ai_gateway_reservation_task_state ON plume.ai_gateway_reservations(workspace_id,(authorized->>'taskId'),state) WHERE authorized->>'taskId'<>'';
CREATE FUNCTION plume.ai_gateway_task_lease_proposal_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(OLD.workspace_id,OLD.task_id,OLD.operation_id,OLD.intent_digest,OLD.expected_digest,OLD.expected_chain,OLD.changes,OLD.credential_plan,OLD.source_ids,OLD.target_ids,OLD.path_ids,OLD.reason,OLD.proposal_digest,OLD.proposed_by,OLD.created_at,OLD.expires_at)
 IS DISTINCT FROM ROW(NEW.workspace_id,NEW.task_id,NEW.operation_id,NEW.intent_digest,NEW.expected_digest,NEW.expected_chain,NEW.changes,NEW.credential_plan,NEW.source_ids,NEW.target_ids,NEW.path_ids,NEW.reason,NEW.proposal_digest,NEW.proposed_by,NEW.created_at,NEW.expires_at) THEN
 RAISE EXCEPTION 'task credential proposal intent is immutable'; END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER ai_gateway_task_lease_proposal_no_rewrite BEFORE UPDATE ON plume.ai_gateway_task_lease_proposals FOR EACH ROW EXECUTE FUNCTION plume.ai_gateway_task_lease_proposal_immutable();
CREATE TRIGGER ai_gateway_task_lease_change_no_update BEFORE UPDATE ON plume.ai_gateway_task_lease_changes FOR EACH ROW EXECUTE FUNCTION plume.report_archive_event_immutable();
CREATE TRIGGER ai_gateway_task_lease_change_no_delete BEFORE DELETE ON plume.ai_gateway_task_lease_changes FOR EACH ROW EXECUTE FUNCTION plume.governance_history_delete_guard();
