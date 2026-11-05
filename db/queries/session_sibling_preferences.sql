-- A conversation's group preferences outlive the idle pin TTL and survive
-- compaction; installation deletion removes them via the foreign key.
-- name: GetSessionSiblingPreferences :one
SELECT preferences FROM router.session_sibling_preferences
WHERE installation_id = @installation_id::uuid
  AND session_key = @session_key::bytea
  AND routing_strategy = @routing_strategy::varchar;

-- Atomically merge displacements within one group while preserving the other
-- groups. A request that started earlier cannot overwrite a later success.
-- name: RecordSessionSiblingPreference :execrows
INSERT INTO router.session_sibling_preferences (installation_id, session_key, routing_strategy, preferences)
VALUES (
  @installation_id::uuid, @session_key::bytea, @routing_strategy::varchar,
  jsonb_build_object(@policy_group::text, jsonb_build_object(
    'model', @preferred_model::text,
    'started_at', @started_at::timestamptz,
    'displaced_models', @displaced_models::jsonb
  ))
)
ON CONFLICT (installation_id, session_key, routing_strategy) DO UPDATE
SET preferences = jsonb_set(
  router.session_sibling_preferences.preferences,
  ARRAY[@policy_group::text],
  jsonb_build_object(
    'model', @preferred_model::text,
    'started_at', @started_at::timestamptz,
    'displaced_models', COALESCE((
      SELECT jsonb_agg(model ORDER BY model)
      FROM (
        SELECT DISTINCT model
        FROM jsonb_array_elements_text(
          COALESCE(router.session_sibling_preferences.preferences -> @policy_group::text -> 'displaced_models', '[]'::jsonb)
          || @displaced_models::jsonb
        ) AS models(model)
        WHERE model <> @preferred_model::text
      ) AS displaced
    ), '[]'::jsonb)
  )
)
WHERE COALESCE((router.session_sibling_preferences.preferences -> @policy_group::text ->> 'started_at')::timestamptz, '-infinity'::timestamptz) <= @started_at::timestamptz;
