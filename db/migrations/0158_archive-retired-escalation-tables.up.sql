BEGIN;

COMMENT ON TABLE router.loop_escalation_events IS 'ARCHIVED: retired cyclic-loop escalation detections, kept read-only for posterity. Nothing writes here.';
COMMENT ON TABLE router.spiral_shadow_events IS 'ARCHIVED: retired shadow spiral detections, kept read-only for posterity. Nothing writes here.';
COMMENT ON TABLE router.struggle_shadow_events IS 'ARCHIVED: retired shadow struggle-escalation decisions, kept read-only for posterity. Nothing writes here.';
COMMENT ON TABLE router.struggle_escalation_events IS 'ARCHIVED: retired struggle escalations, kept read-only for posterity. Nothing writes here.';

COMMIT;
