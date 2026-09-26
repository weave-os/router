-- GetCompactionCheckpoint returns an unexpired checkpoint for a single credential, session, and endpoint.
-- name: GetCompactionCheckpoint :one
SELECT credential_identity, session_key, endpoint, prefix_digest, policy_digest,
  boundary, summary_ciphertext, summary_model, expires_at
FROM router.compaction_checkpoints
WHERE credential_identity = @credential_identity::varchar
  AND session_key = @session_key::bytea
  AND endpoint = @endpoint::varchar
  AND expires_at > NOW();

-- UpsertCompactionCheckpoint replaces a session's verified prefix and encrypted summary.
-- name: UpsertCompactionCheckpoint :exec
INSERT INTO router.compaction_checkpoints (
  credential_identity, session_key, endpoint, prefix_digest, policy_digest,
  boundary, summary_ciphertext, summary_model, expires_at
) VALUES (
  @credential_identity::varchar, @session_key::bytea, @endpoint::varchar,
  @prefix_digest::bytea, @policy_digest::bytea, @boundary::int,
  @summary_ciphertext::bytea, @summary_model::varchar, @expires_at::timestamptz
)
ON CONFLICT (credential_identity, session_key, endpoint) DO UPDATE SET
  prefix_digest = EXCLUDED.prefix_digest,
  policy_digest = EXCLUDED.policy_digest,
  boundary = EXCLUDED.boundary,
  summary_ciphertext = EXCLUDED.summary_ciphertext,
  summary_model = EXCLUDED.summary_model,
  expires_at = EXCLUDED.expires_at;

-- DeleteExpiredCompactionCheckpoints removes summaries after their reuse window.
-- name: DeleteExpiredCompactionCheckpoints :exec
DELETE FROM router.compaction_checkpoints WHERE expires_at <= NOW();
