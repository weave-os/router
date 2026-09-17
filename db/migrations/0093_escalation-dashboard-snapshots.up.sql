BEGIN;

CREATE TABLE router.escalation_dashboard_snapshots (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    captured_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    summary JSONB NOT NULL,
    outcome_breakdown JSONB NOT NULL,
    progress_distribution JSONB NOT NULL,
    first_recommendation_distribution JSONB NOT NULL,
    floor_distribution JSONB NOT NULL,
    organizations JSONB NOT NULL,
    matching_sessions INTEGER NOT NULL
);

CREATE INDEX escalation_dashboard_snapshots_expires_at_idx
    ON router.escalation_dashboard_snapshots (expires_at);

CREATE TABLE router.escalation_dashboard_snapshot_sessions (
    snapshot_id UUID NOT NULL REFERENCES router.escalation_dashboard_snapshots(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    session JSONB NOT NULL,
    PRIMARY KEY (snapshot_id, position)
);

COMMIT;
