BEGIN;

DROP TABLE router.serving_request_attribution;
DROP TABLE router.session_release_bindings;
DROP TABLE router.installation_profile_assignments;
ALTER TABLE router.model_router_api_keys DROP CONSTRAINT model_router_api_keys_personal_routing_only;
ALTER TABLE router.model_router_api_keys DROP CONSTRAINT model_router_api_keys_subject_installation_fk;
ALTER TABLE router.model_router_api_keys DROP COLUMN credential_subject_id;
DROP TABLE router.credential_subject_installations;
DROP TABLE router.credential_subjects;

COMMIT;
