BEGIN;

-- Installation-wide internal-lane enrollment. Admission routes every key of an
-- enrolled installation to prod/weave-internal, so an organization dogfoods a
-- release without each operator enrolling their own credential subject. Only
-- the generation is compared on admission, so a flip re-admits live sessions.
CREATE TABLE router.installation_lane_enrollments (
    installation_id uuid PRIMARY KEY REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    internal_enrolled boolean NOT NULL DEFAULT false,
    enrollment_generation bigint NOT NULL DEFAULT 0 CHECK (enrollment_generation >= 0),
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);

COMMIT;
