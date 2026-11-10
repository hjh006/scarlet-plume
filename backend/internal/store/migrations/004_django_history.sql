ALTER TABLE plume.users ADD COLUMN django_id bigint UNIQUE;
ALTER TABLE plume.workspaces ADD COLUMN django_id bigint UNIQUE;
ALTER TABLE plume.assets ADD COLUMN django_id bigint UNIQUE;
ALTER TABLE plume.activities ADD COLUMN django_id bigint UNIQUE;
ALTER TABLE plume.checks ADD COLUMN django_id bigint UNIQUE;
ALTER TABLE plume.check_evidence ADD COLUMN django_id bigint UNIQUE;
ALTER TABLE plume.check_events ADD COLUMN django_id bigint UNIQUE;

CREATE TABLE plume.django_import (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    completed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    counts jsonb NOT NULL
);
