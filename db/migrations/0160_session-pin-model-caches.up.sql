BEGIN;

-- Per-model prompt cache state for a thread: for each model that served it,
-- when, at what prompt size and message count. The switch policy prices a
-- move by the destination model's own cache (see
-- internal/routingpolicy/switch_eviction.go).
-- Shape: {model: {served_at, prompt_tokens, message_count, write_ttl_s}}.
ALTER TABLE router.session_pins
  ADD COLUMN model_caches JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMIT;
