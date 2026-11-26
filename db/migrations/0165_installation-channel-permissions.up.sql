BEGIN;

-- Serving channels an installation may be admitted to. A worker admits a
-- request on its own channel only when the installation holds it here.
-- Stable and internal keep the enrollment-decided channel
-- (installation_lane_enrollments and credential-subject internal enrollment),
-- so existing access is unchanged and stable needs no row. Beta is opt-in and
-- refused without its row. The internal channel is never granted here; it
-- stays staff-only through internal enrollment.
-- Writers take the installation row's exclusive lock; a cached admission
-- decision keeps serving for its TTL unless the writer also invalidates it.
CREATE TABLE router.installation_channel_permissions (
    installation_id uuid NOT NULL REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    channel varchar(16) NOT NULL CHECK (channel IN ('beta')),
    generation bigint NOT NULL DEFAULT 1 CHECK (generation >= 1),
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (installation_id, channel)
);

COMMENT ON TABLE router.installation_channel_permissions IS 'Opt-in serving channels an installation may be admitted to; stable and internal stay enrollment-decided';

COMMIT;
