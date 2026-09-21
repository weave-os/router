BEGIN;

ALTER TABLE router.model_router_api_keys
    ADD COLUMN harness TEXT;

COMMENT ON COLUMN router.model_router_api_keys.harness IS
    'Optional onboarding harness id (claude_code, codex, opencode, pi). Null for keys minted outside harness onboarding.';

COMMIT;
