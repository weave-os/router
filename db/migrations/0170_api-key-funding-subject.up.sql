BEGIN;

ALTER TABLE router.model_router_api_keys
    ADD COLUMN funding_subject_id UUID REFERENCES router.credential_subjects(id) ON DELETE SET NULL;

COMMIT;
