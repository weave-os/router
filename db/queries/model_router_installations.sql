-- name: CreateModelRouterInstallation :one
INSERT INTO router.model_router_installations (
    external_id,
    name,
    created_by
)
VALUES (
    @external_id::varchar,
    @name::varchar,
    @created_by
)
RETURNING *;

-- Gets an installation by id, scoped to an external_id to prevent cross-tenant access.
-- name: GetModelRouterInstallation :one
SELECT *
FROM router.model_router_installations
WHERE id = @id::uuid
  AND external_id = @external_id::varchar
  AND deleted_at IS NULL;

-- name: ListModelRouterInstallationsForExternalID :many
SELECT *
FROM router.model_router_installations
WHERE external_id = @external_id::varchar
  AND deleted_at IS NULL
ORDER BY created_at DESC;

-- Soft-deletes an installation, scoped to an external_id to prevent cross-tenant deletes.
-- name: SoftDeleteModelRouterInstallation :exec
UPDATE router.model_router_installations
SET deleted_at = NOW()
WHERE id = @id::uuid
  AND external_id = @external_id::varchar
  AND deleted_at IS NULL;

-- Replaces the per-installation model exclusion list, scoped to an external_id
-- to prevent cross-tenant updates. Empty array means "no exclusion". Bumps
-- updated_at so dashboards see the change.
-- name: UpdateModelRouterInstallationExcludedModels :execrows
UPDATE router.model_router_installations
SET excluded_models = @excluded_models::text[],
    updated_at = NOW()
WHERE id = @id::uuid
  AND external_id = @external_id::varchar
  AND deleted_at IS NULL;

-- Replaces the per-installation positive model allowlist, scoped to an
-- external_id to prevent cross-tenant updates. Empty array means "no
-- restriction" (all models routable), NOT "no models". Bumps updated_at so
-- dashboards see the change.
-- name: UpdateModelRouterInstallationAllowedModels :execrows
UPDATE router.model_router_installations
SET allowed_models = @allowed_models::text[],
    updated_at = NOW()
WHERE id = @id::uuid
  AND external_id = @external_id::varchar
  AND deleted_at IS NULL;

-- Replaces the per-installation provider exclusion list, scoped to an
-- external_id to prevent cross-tenant updates. Empty array means "no
-- exclusion". Bumps updated_at so dashboards see the change.
-- name: UpdateModelRouterInstallationExcludedProviders :execrows
UPDATE router.model_router_installations
SET excluded_providers = @excluded_providers::text[],
    updated_at = NOW()
WHERE id = @id::uuid
  AND external_id = @external_id::varchar
  AND deleted_at IS NULL;

-- Sets the routing preference quality weight (a normalized fraction in [0, 1]),
-- scoped to an external_id to prevent cross-tenant updates. NULL clears the
-- preference so the scorer reverts to its tuned defaults.
-- name: UpdateModelRouterInstallationRoutingPreference :execrows
UPDATE router.model_router_installations
SET routing_quality_weight = sqlc.narg('routing_quality_weight'),
    updated_at = NOW()
WHERE id = @id::uuid
  AND external_id = @external_id::varchar
  AND deleted_at IS NULL;

-- Sets the subscription usage-bypass gate, scoped to an external_id to prevent
-- cross-tenant updates. enabled toggles the gate; threshold is the [0, 1]
-- utilization at/above which the gate disengages and normal routing takes over.
-- A NULL threshold means "use the deployment default" at request time.
-- name: UpdateModelRouterInstallationUsageBypass :execrows
UPDATE router.model_router_installations
SET usage_bypass_enabled = @usage_bypass_enabled::boolean,
    usage_bypass_threshold = sqlc.narg('usage_bypass_threshold'),
    updated_at = NOW()
WHERE id = @id::uuid
  AND external_id = @external_id::varchar
  AND deleted_at IS NULL;

-- Sets the per-installation capture ceiling ('off', 'hashed', or 'full'), scoped
-- to an external_id to prevent cross-tenant updates. NULL clears the override.
-- name: UpdateModelRouterInstallationContentCaptureMode :execrows
UPDATE router.model_router_installations
SET content_capture_mode = sqlc.narg('content_capture_mode'),
    updated_at = NOW()
WHERE id = @id::uuid
  AND external_id = @external_id::varchar
  AND deleted_at IS NULL;

-- Toggles subscription-aware routing for the installation, scoped to an
-- external_id to prevent cross-tenant updates. When true, the scorer's
-- subscription subsidy bonus is suppressed so routing decides on merits and
-- non-Claude models compete fairly; the subscription credential is still
-- forwarded for turns that route to Claude on their own merits.
-- name: UpdateModelRouterInstallationSubscriptionRoutingDisabled :execrows
UPDATE router.model_router_installations
SET subscription_routing_disabled = @subscription_routing_disabled::boolean,
    updated_at = NOW()
WHERE id = @id::uuid
  AND external_id = @external_id::varchar
  AND deleted_at IS NULL;

-- Toggles hiding the router's terminal surfaces (routing marker, feedback
-- footer, statusline) for the installation, scoped to an external_id to
-- prevent cross-tenant updates. Requests route identically; only what is
-- rendered in the caller's terminal changes.
-- name: UpdateModelRouterInstallationHideTerminalSurfaces :execrows
UPDATE router.model_router_installations
SET hide_terminal_surfaces = @hide_terminal_surfaces::boolean,
    updated_at = NOW()
WHERE id = @id::uuid
  AND external_id = @external_id::varchar
  AND deleted_at IS NULL;

-- Stamps first_request_served_at the first time this installation routes a
-- request. WHERE IS NULL makes the update a no-op after the first write, so
-- the timestamp records the *first* request and rotation never resets it.
-- name: MarkModelRouterInstallationFirstRequestServed :exec
UPDATE router.model_router_installations
SET first_request_served_at = NOW()
WHERE id = @id::uuid
  AND deleted_at IS NULL
  AND first_request_served_at IS NULL;

-- Replaces the per-installation behavioral flag override set, scoped to an
-- external_id to prevent cross-tenant updates. The payload is the whole sparse
-- object, not a delta: callers read-modify-write so clearing an override is
-- expressed by omitting its key. Keys are validated against internal/flags'
-- registry before this runs. Bumps updated_at so dashboards see the change.
-- name: UpdateModelRouterInstallationFlagOverrides :execrows
UPDATE router.model_router_installations
SET flag_overrides = @flag_overrides::jsonb,
    updated_at = NOW()
WHERE id = @id::uuid
  AND external_id = @external_id::varchar
  AND deleted_at IS NULL;

-- Replaces the per-installation fast-mode opt-in list, scoped to an
-- external_id to prevent cross-tenant updates. Listed models dispatch on the
-- provider's fast tier; empty array means no model runs fast. Bumps updated_at
-- so dashboards see the change.
-- name: UpdateModelRouterInstallationFastModeModels :execrows
UPDATE router.model_router_installations
SET fast_mode_models = @fast_mode_models::text[],
    updated_at = NOW()
WHERE id = @id::uuid
  AND external_id = @external_id::varchar
  AND deleted_at IS NULL;
