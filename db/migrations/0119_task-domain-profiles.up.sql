BEGIN;

CREATE TABLE router.task_domain_profiles (
    conversation_key text NOT NULL,
    root_sha256 text NOT NULL,
    release_sha256 text NOT NULL,
    evidence_sha256 text NOT NULL,
    outcome jsonb,
    expires_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP + INTERVAL '30 days',
    PRIMARY KEY (conversation_key, root_sha256, release_sha256, evidence_sha256)
);
CREATE INDEX task_domain_profiles_expiry ON router.task_domain_profiles (expires_at);

COMMIT;
