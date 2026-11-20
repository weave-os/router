BEGIN;

COMMENT ON TABLE router.loop_escalation_events IS 'Cyclic tool-call loop detections: ops signal and (session, looping_model) -> looped training labels';
COMMENT ON TABLE router.spiral_shadow_events IS 'Shadow-mode spiral (death-march) detections: log-only fire-rate corpus measured on live traffic before escalation is armed';
COMMENT ON TABLE router.struggle_shadow_events IS NULL;
COMMENT ON TABLE router.struggle_escalation_events IS NULL;

COMMIT;
