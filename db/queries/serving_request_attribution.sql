-- name: InsertServingRequestAttribution :exec
-- Persist the admitted tuple before dispatch; request_id joins telemetry and billing.
INSERT INTO router.serving_request_attribution (request_id, installation_id, api_key_id, scope, binding)
VALUES (@request_id::text, @installation_id::uuid, @api_key_id::uuid, @scope::jsonb, @binding::jsonb);

-- name: GetServingRequestAttribution :one
-- Feedback may recover the original tuple only within its authenticated installation.
SELECT request_id, api_key_id, scope, binding
FROM router.serving_request_attribution
WHERE request_id = @request_id::text AND installation_id = @installation_id::uuid;
