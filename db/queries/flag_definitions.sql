-- Upserts one row of the published flag registry. Called once per flag at router
-- boot. Idempotent by design: every replica boots with the same compiled-in
-- registry and writes identical rows, so concurrent boots converge rather than
-- conflict. During a deploy that changes an env default, last writer wins on
-- deployment_default -- acceptable because the column is display-only and the
-- routing path reads the live in-process value.
-- name: UpsertFlagDefinition :exec
INSERT INTO router.flag_definitions (
    key,
    kind,
    env_var,
    deployment_default,
    org_overridable,
    description,
    registry_version,
    updated_at
)
VALUES (
    @key::text,
    @kind::text,
    @env_var::text,
    @deployment_default::text,
    @org_overridable::boolean,
    @description::text,
    @registry_version::integer,
    NOW()
)
ON CONFLICT (key) DO UPDATE
SET kind = EXCLUDED.kind,
    env_var = EXCLUDED.env_var,
    deployment_default = EXCLUDED.deployment_default,
    org_overridable = EXCLUDED.org_overridable,
    description = EXCLUDED.description,
    registry_version = EXCLUDED.registry_version,
    updated_at = NOW();

-- Drops rows for flags no longer in the compiled-in registry. The registry
-- version guard makes this safe during rolling deploys: an older revision may
-- prune definitions at its own version, but cannot remove rows published by a
-- newer revision whose registry may have grown.
-- name: DeleteFlagDefinitionsNotIn :exec
DELETE FROM router.flag_definitions
WHERE registry_version <= @registry_version::integer
  AND key <> ALL(@keys::text[]);

-- name: ListFlagDefinitions :many
SELECT *
FROM router.flag_definitions
ORDER BY key;
