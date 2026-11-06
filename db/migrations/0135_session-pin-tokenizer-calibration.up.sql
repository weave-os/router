BEGIN;

-- Per-thread evidence for converting token counts between tokenizer
-- families (see internal/router/sessionpin/tokenizer_calibration.go). The
-- same history bills ~1.5-1.8x more tokens on Claude than on GPT, varying
-- with the thread's mix of code, JSON and prose. When a thread is served by
-- both families within a couple of minutes, the router records the ratio of
-- their full-prompt counts here and uses it to report a cross-tokenizer
-- turn's usage in the requested model's tokens, so the client's
-- auto-compaction tracks the window it actually has.
-- Shape: {"observations": {family: {prompt, observed_at}},
--         "ratios": {"from->to": {ratio, measured_at}}}.
ALTER TABLE router.session_pins
  ADD COLUMN tokenizer_calibration JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMIT;
