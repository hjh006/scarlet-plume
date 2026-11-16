CREATE TABLE plume.governance_ops_records (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('rights_request','consent','vendor','audit','incident','labeling')),
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','in_progress','in_review','needs_work','completed','withdrawal_pending','withdrawn','cancelled')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    input jsonb NOT NULL CHECK (jsonb_typeof(input)='object'),
    regulations jsonb NOT NULL CHECK (jsonb_typeof(regulations)='array'),
    evidence jsonb NOT NULL CHECK (jsonb_typeof(evidence)='array'),
    contributors bigint[] NOT NULL,
    created_by bigint NOT NULL REFERENCES plume.users(id),
    updated_by bigint NOT NULL REFERENCES plume.users(id),
    withdrawing boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(workspace_id,id)
);
CREATE INDEX governance_ops_page_idx ON plume.governance_ops_records(workspace_id,id DESC);
CREATE INDEX governance_ops_kind_page_idx ON plume.governance_ops_records(workspace_id,kind,id DESC);

CREATE TABLE plume.governance_ops_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint NOT NULL,
    record_id bigint NOT NULL,
    revision bigint NOT NULL,
    action text NOT NULL CHECK (action IN ('created','updated','start','submit','approve','request_changes','cancel','withdraw')),
    actor_id bigint NOT NULL REFERENCES plume.users(id),
    note text NOT NULL CHECK (char_length(note) <= 600),
    snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
    snapshot_digest text NOT NULL CHECK(snapshot_digest ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY(workspace_id,record_id) REFERENCES plume.governance_ops_records(workspace_id,id) ON DELETE CASCADE,
    UNIQUE(record_id,revision)
);
CREATE INDEX governance_ops_history_idx ON plume.governance_ops_events(workspace_id,record_id,id DESC);

-- Historic revisions are append-only. Workspace deletion still cascades as it
-- does for the platform's other tenant-owned governance records.
CREATE FUNCTION plume.governance_ops_immutable_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='UPDATE' THEN RAISE EXCEPTION 'governance operation history is immutable'; END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER governance_ops_event_no_update BEFORE UPDATE ON plume.governance_ops_events
FOR EACH ROW EXECUTE FUNCTION plume.governance_ops_immutable_event();
