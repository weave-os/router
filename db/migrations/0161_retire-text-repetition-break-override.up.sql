BEGIN;

UPDATE router.model_router_installations
SET flag_overrides = flag_overrides - 'text_repetition_break_enabled'
WHERE flag_overrides ? 'text_repetition_break_enabled';

COMMIT;
