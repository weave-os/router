BEGIN;

-- Weave-maintained projection of the person behind a request email, scoped to
-- one installation. The router holds no account table, so an inbound
-- X-Weave-User-Email can only become a credential subject through this row.
-- Installation scoping is what stops one organization's email from resolving
-- into another's subject.
CREATE TABLE router.credential_subject_identities (
    subject_id uuid NOT NULL,
    installation_id uuid NOT NULL,
    email varchar(254) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    revoked_at timestamptz,
    PRIMARY KEY (subject_id, installation_id),
    FOREIGN KEY (subject_id, installation_id)
        REFERENCES router.credential_subject_installations(subject_id, installation_id) ON DELETE CASCADE,
    CHECK (email = lower(email))
);

-- One live person per address: the request path resolves on equality, so the
-- writer stores the same lower-cased form proxy.NormalizeEmail produces.
CREATE UNIQUE INDEX credential_subject_identities_installation_email_unique
    ON router.credential_subject_identities(installation_id, email) WHERE revoked_at IS NULL;

COMMENT ON TABLE router.credential_subject_identities IS 'Email to credential subject per installation; lets a shared routing key serve each caller their own subscriptions and allowance';

COMMIT;
