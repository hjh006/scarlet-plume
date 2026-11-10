CREATE TABLE plume.check_templates (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id bigint REFERENCES plume.workspaces(id) ON DELETE CASCADE,
    template_key text NOT NULL CHECK (template_key ~ '^[a-z0-9][a-z0-9_-]{0,59}$'),
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 100),
    kind text NOT NULL CHECK (kind IN ('sharing', 'cross_border', 'ai')),
    version integer NOT NULL CHECK (version > 0),
    created_by bigint REFERENCES plume.users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE NULLS NOT DISTINCT (workspace_id, template_key, version)
);

CREATE TABLE plume.check_template_items (
    template_revision_id bigint NOT NULL REFERENCES plume.check_templates(id) ON DELETE CASCADE,
    item_key text NOT NULL CHECK (item_key ~ '^[a-z0-9][a-z0-9_-]{0,59}$'),
    position integer NOT NULL CHECK (position BETWEEN 1 AND 25),
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 100),
    requirement_code text NOT NULL REFERENCES plume.requirements(code),
    applicability text NOT NULL CHECK (char_length(applicability) BETWEEN 1 AND 1000),
    evidence_requirements text NOT NULL CHECK (char_length(evidence_requirements) BETWEEN 1 AND 1000),
    acceptance_criteria text NOT NULL CHECK (char_length(acceptance_criteria) BETWEEN 1 AND 1000),
    PRIMARY KEY (template_revision_id, item_key),
    UNIQUE (template_revision_id, position)
);

ALTER TABLE plume.checks
    ADD COLUMN template_revision_id bigint,
    ADD COLUMN template_item_key text,
    ADD CONSTRAINT checks_template_item_fk FOREIGN KEY (template_revision_id, template_item_key)
        REFERENCES plume.check_template_items(template_revision_id, item_key),
    ADD CONSTRAINT checks_template_activity CHECK (
        (template_revision_id IS NULL AND template_item_key IS NULL)
        OR (template_revision_id IS NOT NULL AND template_item_key IS NOT NULL AND activity_id IS NOT NULL)
    );
