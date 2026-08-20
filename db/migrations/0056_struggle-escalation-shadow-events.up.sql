BEGIN;

-- Durable record of shadow-mode "struggle" detections (see
-- internal/proxy/struggle_detection.go). A struggling session is one that is
-- taking far too long without the literal loop signatures the existing
-- detectors catch (identical tool calls, cyclic re-reads with no edits).
-- Shadow mode takes no routing action; these rows ARE the deliverable, joined
-- offline by session_key against model_router_request_telemetry / session
-- outcomes to measure fire rate and lead time before any escalation is armed.
--
-- Phase 0 (offline mining, 14d of telemetry) established the two operating
-- points recorded as separate reasons:
--   * early: turn_count >= 30 AND wall >= 10m  — where users actually bail
--     (median mid-session force-model is turn ~39); too loose to escalate on
--     alone, so reserved for a cheap same-cluster "sideways" move.
--   * late:  turn_count >= 80 AND wall >= 30m  — high precision (4.85%
--     mid-session human takeover vs 0.05% baseline), reserved for the
--     expensive "up" move (next cluster).
-- Both are recorded here so the armed threshold can be re-tuned offline
-- without re-running traffic.
CREATE TABLE router.struggle_shadow_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    installation_id UUID NOT NULL REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    session_key     BYTEA NOT NULL,
    role            VARCHAR NOT NULL,
    routed_model    VARCHAR NOT NULL,
    turn_type       VARCHAR NOT NULL,
    -- Which operating point crossed: 'early' (turn_count >= 30, wall >= 10m)
    -- or 'late' (turn_count >= 80, wall >= 30m). One row per (session, role,
    -- reason); the late event is suppressed if an early event already exists
    -- for the same session, so escalation stages map to distinct rows.
    reason          VARCHAR NOT NULL,
    -- Full signal snapshot at fire time, recorded regardless of which reason
    -- fired, so thresholds can be re-tuned offline without re-running traffic.
    turn_count      INT NOT NULL,
    -- Wall-clock seconds since the session pin was created (its first routed
    -- turn), as measured at fire time.
    wall_seconds    BIGINT NOT NULL,
    -- Whether the session has already served more than one model. Capacity
    -- rotations (context-window eviction, provider strikes) switch models
    -- without any quality signal, so a struggling session that has already
    -- churned is a materially different case from one that has not.
    session_ever_switched BOOLEAN NOT NULL,
    -- Estimated inbound prompt tokens for the firing turn: a size proxy for
    -- how much context the session is dragging per turn.
    est_input_tokens INT NOT NULL
);

-- The shadow handler's once-per-(session, role, reason) budget query and the
-- offline joins that measure fire rate / precision / lead time all key on
-- (session_key, role) — index it so the per-turn budget check stays cheap.
CREATE INDEX struggle_shadow_events_session_role_idx
    ON router.struggle_shadow_events (session_key, role);

COMMIT;
