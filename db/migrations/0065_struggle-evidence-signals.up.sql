BEGIN;

-- Two more behavioral signals for the shadow spiral corpus: alternation
-- between exactly two actions, and tool calls made since the last successful
-- file mutation. Recorded on every spiral event (not just the ones they fire)
-- so their operating points can be picked offline like the existing four.
ALTER TABLE router.spiral_shadow_events
    ADD COLUMN ping_pong_len INT NOT NULL DEFAULT 0,
    ADD COLUMN steps_since_progress INT NOT NULL DEFAULT 0;

COMMENT ON COLUMN router.spiral_shadow_events.ping_pong_len IS 'Length of the trailing A/B/A/B alternation between exactly two tool-call signatures';
COMMENT ON COLUMN router.spiral_shadow_events.steps_since_progress IS 'Tool calls made since the last non-errored edit/write tool_result; 0 when the session has never attempted an edit';

-- Which execution evidence armed the escalation, and under which arming mode
-- ('turn_wall' for the turn/wall timer, 'evidence' for the behavioral gate).
-- Both are needed to attribute a win to the signal that produced it.
ALTER TABLE router.struggle_escalation_events
    ADD COLUMN arming_mode VARCHAR NOT NULL DEFAULT '',
    ADD COLUMN evidence_reasons VARCHAR[] NOT NULL DEFAULT '{}';

COMMENT ON COLUMN router.struggle_escalation_events.arming_mode IS 'What armed this escalation: turn_wall (turn/wall thresholds) or evidence (behavioral signals)';
COMMENT ON COLUMN router.struggle_escalation_events.evidence_reasons IS 'Spiral signal classes present at arming time (err_streak, same_file_thrash, repetition, monologue, ping_pong, no_progress)';

COMMIT;
