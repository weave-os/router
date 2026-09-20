BEGIN;

ALTER TABLE router.session_release_bindings
    DROP COLUMN subject_assignment_generation;

DROP TABLE router.credential_subject_profile_assignments;

COMMIT;
