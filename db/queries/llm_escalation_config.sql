-- name: GetEscalationSelection :one
SELECT flag_overrides FROM router.model_router_installations
WHERE id = @installation_id::uuid AND deleted_at IS NULL;

-- name: UpdateEscalationSelection :one
UPDATE router.model_router_installations
SET flag_overrides = COALESCE(flag_overrides, '{}'::jsonb) || @selection_patch::jsonb,
    updated_at = NOW()
WHERE id = @installation_id::uuid AND deleted_at IS NULL
  AND CASE WHEN flag_overrides ? 'escalation_active_classifier'
      THEN COALESCE((flag_overrides->>'escalation_epoch')::integer, 0)
      ELSE COALESCE((flag_overrides->>'escalation_xgb_epoch')::integer, 0) END = @expected_epoch::integer
RETURNING flag_overrides;
