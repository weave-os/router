-- Creates a new external API key for an installation. The unique index on
-- (installation_id, provider) WHERE deleted_at IS NULL enforces one key per
-- provider; callers must soft-delete the existing key before inserting.
-- name: CreateExternalAPIKey :one
INSERT INTO router.model_router_external_api_keys (
    installation_id,
    external_id,
    provider,
    key_ciphertext,
    key_prefix,
    key_suffix,
    key_fingerprint,
    name,
    base_url,
    model_aliases,
    identity_header_name,
    identity_header_format,
    forwarded_client_headers,
    baggage_header,
    federation_config,
    auth_type,
    auth_account,
    auth_user,
    funding_priority,
    created_by
)
VALUES (
    @installation_id::uuid,
    @external_id::varchar,
    @provider::varchar,
    @key_ciphertext::bytea,
    @key_prefix::varchar,
    @key_suffix::varchar,
    @key_fingerprint::varchar,
    @name,
    @base_url,
    sqlc.narg('model_aliases')::jsonb,
    sqlc.narg('identity_header_name')::text,
    sqlc.narg('identity_header_format')::text,
    sqlc.narg('forwarded_client_headers')::text[],
    sqlc.narg('baggage_header')::text,
    sqlc.narg('federation_config')::jsonb,
    @auth_type::text,
    sqlc.narg('auth_account')::text,
    sqlc.narg('auth_user')::text,
    @funding_priority::text,
    @created_by
)
RETURNING *;

-- Returns all active external API keys for an installation. Used by the auth
-- cache to populate the ExternalKeys map on cache miss.
-- name: GetActiveExternalAPIKeysForInstallation :many
SELECT *
FROM router.model_router_external_api_keys
WHERE installation_id = @installation_id::uuid
  AND deleted_at IS NULL
ORDER BY provider, created_at DESC;

-- Replaces one key's model alias map without touching the stored secret, so the
-- dashboard can edit aliases without the operator re-entering the credential.
-- Cross-tenant safe via installation_id predicate.
-- name: UpdateExternalAPIKeyModelAliases :one
UPDATE router.model_router_external_api_keys
SET model_aliases = sqlc.narg('model_aliases')::jsonb, updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid
  AND installation_id = @installation_id::uuid
  AND deleted_at IS NULL
RETURNING *;

-- Sets whether one key is spent before or after the caller's subscription,
-- without touching the stored secret. Cross-tenant safe via installation_id predicate.
-- name: UpdateExternalAPIKeyFundingPriority :one
UPDATE router.model_router_external_api_keys
SET funding_priority = @funding_priority::text, updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid
  AND installation_id = @installation_id::uuid
  AND deleted_at IS NULL
RETURNING *;

-- Soft-deletes an external API key. Cross-tenant safe via installation_id predicate.
-- name: SoftDeleteExternalAPIKey :exec
UPDATE router.model_router_external_api_keys
SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid
  AND installation_id = @installation_id::uuid
  AND deleted_at IS NULL;

-- Reads the active key's funding priority for a provider without loading its secret,
-- so an upsert that omits a priority can keep it.
-- name: GetActiveExternalAPIKeyFundingPriority :one
SELECT funding_priority
FROM router.model_router_external_api_keys
WHERE installation_id = @installation_id::uuid
  AND provider = @provider::varchar
  AND deleted_at IS NULL
LIMIT 1;

-- Soft-deletes the existing key for a provider before upsert.
-- name: SoftDeleteExternalAPIKeyByProvider :exec
UPDATE router.model_router_external_api_keys
SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE installation_id = @installation_id::uuid
  AND provider = @provider::varchar
  AND deleted_at IS NULL;

-- Fire-and-forget update after successful upstream call.
-- name: MarkExternalAPIKeyUsed :exec
UPDATE router.model_router_external_api_keys
SET last_used_at = NOW()
WHERE id = @id::uuid
  AND deleted_at IS NULL;

-- Every active stored secret, across installations, for the boot-time
-- plaintext-to-ciphertext upgrade. Workload-identity rows hold no secret.
-- name: ListActiveExternalAPIKeySecrets :many
SELECT id, external_id, provider, key_ciphertext
FROM router.model_router_external_api_keys
WHERE deleted_at IS NULL
  AND octet_length(key_ciphertext) > 0;

-- Compare-and-swap on the old bytes so a concurrent rotation is not clobbered.
-- name: ReplaceExternalAPIKeyCiphertext :execrows
UPDATE router.model_router_external_api_keys
SET key_ciphertext = @key_ciphertext::bytea, updated_at = CURRENT_TIMESTAMP
WHERE id = @id::uuid
  AND key_ciphertext = @previous_ciphertext::bytea
  AND deleted_at IS NULL;
