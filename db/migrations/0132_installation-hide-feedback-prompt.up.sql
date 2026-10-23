BEGIN;

-- Per-installation toggle to hide only the trailing "/rf" feedback hint. The
-- routing marker and statusline stay visible, and /rf submissions are still
-- recorded. hide_terminal_surfaces continues to hide all three.
ALTER TABLE router.model_router_installations
  ADD COLUMN hide_feedback_prompt BOOLEAN NOT NULL DEFAULT FALSE;

COMMIT;
