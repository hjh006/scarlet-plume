ALTER TABLE plume.automation_report_archives
    ADD COLUMN retention_state text NOT NULL DEFAULT 'review_required' CHECK(retention_state IN ('review_required','retained','rotation_allowed')),
    ADD COLUMN retain_until timestamptz,
    ADD COLUMN legal_hold boolean NOT NULL DEFAULT false,
    ADD COLUMN hold_reason text NOT NULL DEFAULT '' CHECK(char_length(hold_reason)<=600),
    ADD COLUMN retention_basis text NOT NULL DEFAULT '用途和保留依据尚待核定；暂不参与数量轮换。' CHECK(char_length(retention_basis)<=600),
    ADD COLUMN retention_revision bigint NOT NULL DEFAULT 1 CHECK(retention_revision>0),
    ADD COLUMN retention_contributors bigint[] NOT NULL DEFAULT '{}',
    ADD COLUMN retention_updated_by bigint REFERENCES plume.users(id),
    ADD COLUMN retention_updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    ADD COLUMN content_digest text CHECK(content_digest ~ '^[0-9a-f]{64}$'),
    ADD COLUMN summary_digest text CHECK(summary_digest ~ '^[0-9a-f]{64}$'),
    ADD COLUMN content_bytes bigint CHECK(content_bytes>=0);

-- Preserve all existing snapshots. Unknown purpose is protected rather than
-- inventing one legal retention duration for all kinds of report.
UPDATE plume.automation_report_archives a SET
    retention_contributors=ARRAY[j.actor_id],retention_updated_by=j.actor_id,
    content_digest=CASE WHEN a.report IS NOT NULL THEN encode(sha256(convert_to(a.report::text,'UTF8')),'hex') END,
    summary_digest=CASE WHEN a.summary IS NOT NULL THEN encode(sha256(convert_to(a.summary::text,'UTF8')),'hex') END,
    content_bytes=CASE WHEN a.report IS NOT NULL THEN octet_length(convert_to(a.report::text,'UTF8')) END
FROM plume.automation_runs r JOIN plume.automation_jobs j ON j.id=r.job_id
WHERE r.id=a.automation_run_id;

CREATE TABLE plume.report_archive_retention_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    archive_id bigint NOT NULL,
    actor_id bigint REFERENCES plume.users(id),
    action text NOT NULL CHECK(action IN ('hold','extend','allow_rotation','release_hold','rotation_purged','export_prepared')),
    note text NOT NULL CHECK(char_length(note)<=600),
    evidence_id bigint,
    snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX report_archive_retention_history_idx ON plume.report_archive_retention_events(workspace_id,archive_id,id DESC);

CREATE FUNCTION plume.protect_report_archive() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE ws bigint; source_actor bigint;
BEGIN
    IF TG_OP='INSERT' THEN
        SELECT j.workspace_id,j.actor_id INTO ws,source_actor FROM plume.automation_runs r JOIN plume.automation_jobs j ON j.id=r.job_id WHERE r.id=NEW.automation_run_id;
        NEW.retention_contributors=ARRAY[source_actor];
        NEW.retention_updated_by=source_actor;
        IF NEW.report IS NOT NULL THEN
            NEW.content_digest=encode(sha256(convert_to(NEW.report::text,'UTF8')),'hex');
            NEW.summary_digest=encode(sha256(convert_to(NEW.summary::text,'UTF8')),'hex');
            NEW.content_bytes=octet_length(convert_to(NEW.report::text,'UTF8'));
        END IF;
        RETURN NEW;
    ELSIF TG_OP='UPDATE' THEN
        IF ROW(OLD.automation_run_id,OLD.generated_at,OLD.status,OLD.code,OLD.summary,OLD.report,OLD.content_digest,OLD.summary_digest,OLD.content_bytes)
           IS DISTINCT FROM ROW(NEW.automation_run_id,NEW.generated_at,NEW.status,NEW.code,NEW.summary,NEW.report,NEW.content_digest,NEW.summary_digest,NEW.content_bytes) THEN
            RAISE EXCEPTION 'report archive content and digest are immutable' USING ERRCODE='23514',CONSTRAINT='report_archive_content_guard';
        END IF;
        RETURN NEW;
    ELSE
        IF OLD.retention_state<>'rotation_allowed' OR OLD.legal_hold OR (OLD.retain_until IS NOT NULL AND OLD.retain_until>clock_timestamp()) THEN
            RAISE EXCEPTION 'report archive requires retention review, hold release or expiry before deletion' USING ERRCODE='23514',CONSTRAINT='report_archive_retention_guard';
        END IF;
        SELECT j.workspace_id INTO ws FROM plume.automation_runs r JOIN plume.automation_jobs j ON j.id=r.job_id WHERE r.id=OLD.automation_run_id;
        IF ws IS NULL THEN
            -- Prevent run/job cascade from silently removing the decision trail.
            RAISE EXCEPTION 'remove eligible report archives before deleting their run or job' USING ERRCODE='23514',CONSTRAINT='report_archive_retention_guard';
        END IF;
        INSERT INTO plume.report_archive_retention_events(workspace_id,archive_id,action,note,snapshot)
        VALUES(ws,OLD.automation_run_id,'rotation_purged',OLD.retention_basis,jsonb_build_object('contentDigest',OLD.content_digest,'summaryDigest',OLD.summary_digest,'generatedAt',OLD.generated_at,'retentionRevision',OLD.retention_revision,'basis',OLD.retention_basis,'retainUntil',OLD.retain_until,'legalHold',OLD.legal_hold,'decisionActor',OLD.retention_updated_by));
        RETURN OLD;
    END IF;
END;
$$;
CREATE TRIGGER report_archive_protection BEFORE INSERT OR UPDATE OR DELETE ON plume.automation_report_archives
FOR EACH ROW EXECUTE FUNCTION plume.protect_report_archive();

CREATE FUNCTION plume.report_archive_event_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'report archive retention history is immutable'; END;
$$;
CREATE TRIGGER report_archive_event_no_update BEFORE UPDATE ON plume.report_archive_retention_events
FOR EACH ROW EXECUTE FUNCTION plume.report_archive_event_immutable();

CREATE FUNCTION plume.governance_history_delete_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS(SELECT 1 FROM plume.workspaces WHERE id=OLD.workspace_id) THEN
        RAISE EXCEPTION 'governance history is append-only; direct deletion is prohibited';
    END IF;
    RETURN OLD;
END;
$$;
CREATE TRIGGER report_archive_event_no_delete BEFORE DELETE ON plume.report_archive_retention_events
FOR EACH ROW EXECUTE FUNCTION plume.governance_history_delete_guard();
CREATE TRIGGER governance_ops_event_no_delete BEFORE DELETE ON plume.governance_ops_events
FOR EACH ROW EXECUTE FUNCTION plume.governance_history_delete_guard();
