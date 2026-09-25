BEGIN;

CREATE TABLE router.compaction_checkpoints (
  credential_identity VARCHAR NOT NULL,
  session_key BYTEA NOT NULL,
  endpoint VARCHAR NOT NULL,
  prefix_digest BYTEA NOT NULL,
  policy_digest BYTEA NOT NULL,
  boundary INTEGER NOT NULL,
  summary_ciphertext BYTEA NOT NULL,
  summary_model VARCHAR NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (credential_identity, session_key, endpoint)
);

CREATE INDEX compaction_checkpoints_expires_at_idx
  ON router.compaction_checkpoints (expires_at);

COMMIT;