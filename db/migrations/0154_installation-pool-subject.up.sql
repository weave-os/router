ALTER TABLE router.model_router_installations
    ADD COLUMN pool_subject_id UUID REFERENCES router.credential_subjects(id) ON DELETE SET NULL;
