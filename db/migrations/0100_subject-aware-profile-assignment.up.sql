BEGIN;

CREATE TABLE router.credential_subject_profile_assignments (
    subject_id uuid NOT NULL,
    installation_id uuid NOT NULL,
    assignment_source varchar(32) NOT NULL CHECK (
        assignment_source IN ('cohort', 'subscriber_plan', 'lane_default')
    ),
    assignment_state varchar(32) NOT NULL CHECK (
        assignment_state IN (
            'default_following',
            'deliberately_unassigned',
            'pending',
            'failed',
            'revoked',
            'incompatible',
            'effective'
        )
    ),
    desired_generation bigint NOT NULL CHECK (desired_generation > 0),
    effective_generation bigint NOT NULL DEFAULT 0 CHECK (
        effective_generation >= 0 AND effective_generation <= desired_generation
    ),
    desired_profile_key uuid,
    effective_profile_key uuid,
    router_acknowledgement_id uuid NOT NULL DEFAULT gen_random_uuid(),
    evidence_id varchar(160) NOT NULL,
    projection_attempts integer NOT NULL DEFAULT 1 CHECK (projection_attempts > 0),
    projected_at timestamptz NOT NULL,
    effective_at timestamptz,
    last_failure_detail varchar(512),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (subject_id, installation_id, assignment_source),
    FOREIGN KEY (subject_id, installation_id)
        REFERENCES router.credential_subject_installations(subject_id, installation_id)
        ON DELETE CASCADE,
    CHECK (desired_profile_key IS NULL OR desired_profile_key <> '00000000-0000-0000-0000-000000000000'::uuid),
    CHECK (effective_profile_key IS NULL OR effective_profile_key <> '00000000-0000-0000-0000-000000000000'::uuid),
    CHECK (
        assignment_source <> 'lane_default'
        OR assignment_state IN ('default_following', 'deliberately_unassigned')
    ),
    CHECK (
        assignment_state NOT IN ('default_following', 'deliberately_unassigned')
        OR (desired_profile_key IS NULL AND effective_profile_key IS NULL)
    ),
    CHECK (
        assignment_state <> 'effective'
        OR desired_profile_key IS NOT NULL
    ),
    CHECK (
        assignment_state NOT IN ('effective', 'default_following', 'deliberately_unassigned')
        OR (
            effective_generation = desired_generation
            AND effective_profile_key IS NOT DISTINCT FROM desired_profile_key
            AND effective_at IS NOT NULL
            AND last_failure_detail IS NULL
        )
    ),
    CHECK (
        assignment_state NOT IN ('failed', 'incompatible')
        OR last_failure_detail IS NOT NULL
    )
);

CREATE INDEX credential_subject_profile_assignments_admission_idx
    ON router.credential_subject_profile_assignments (
        subject_id,
        installation_id,
        (CASE assignment_source
            WHEN 'cohort' THEN 1
            WHEN 'subscriber_plan' THEN 2
            WHEN 'lane_default' THEN 3
            ELSE 4
        END)
    );

COMMENT ON TABLE router.credential_subject_profile_assignments IS 'Opaque subject-level serving profile projection state; private account and plan tables remain outside Router';
COMMENT ON COLUMN router.credential_subject_profile_assignments.assignment_source IS 'Precedence source: organization overrides stay installation-scoped; subject rows cover cohort, subscriber plan and explicit lane-default states';
COMMENT ON COLUMN router.credential_subject_profile_assignments.effective_profile_key IS 'Previous effective key is retained when a newer desired projection is pending, failed or incompatible';

ALTER TABLE router.session_release_bindings
    ADD COLUMN subject_assignment_generation bigint NOT NULL DEFAULT 0
        CHECK (subject_assignment_generation >= 0);

COMMIT;
