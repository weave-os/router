-- Serializes one session's clock advances, including its first. Run it in the
-- advance's transaction: the advance's snapshot is taken after the lock, so it
-- sees a racing advance that committed first. Hash collisions only over-serialize.
-- name: LockSessionTurnClock :exec
SELECT pg_advisory_xact_lock(hashtextextended(@session_lock_key::text, 0));

-- Records that a session's main thread finished a response and returns the
-- finish recorded before it, so the caller can time the next typed prompt.
-- Racing completions keep the latest finish. Every CTE reads the same
-- snapshot, so previous sees the row before advanced rewrites it; the anchor
-- row turns a session's first advance into a NULL result instead of no rows.
-- name: AdvanceSessionTurnClock :one
WITH previous AS (
    SELECT last_response_ended_at, last_served_model
    FROM router.session_turn_clocks
    WHERE installation_id = @installation_id::uuid
      AND session_key = @session_key::bytea
),
advanced AS (
    INSERT INTO router.session_turn_clocks (
        installation_id,
        session_key,
        last_response_ended_at,
        last_served_model
    ) VALUES (
        @installation_id::uuid,
        @session_key::bytea,
        @response_ended_at::timestamptz,
        @served_model::varchar
    )
    ON CONFLICT (installation_id, session_key) DO UPDATE
    SET last_response_ended_at = EXCLUDED.last_response_ended_at,
        last_served_model = EXCLUDED.last_served_model,
        updated_at = now()
    WHERE router.session_turn_clocks.last_response_ended_at < EXCLUDED.last_response_ended_at
)
SELECT
    previous.last_response_ended_at AS previous_response_ended_at,
    previous.last_served_model AS previous_served_model
FROM (SELECT 1) AS anchor
LEFT JOIN previous ON true;

-- Deletes clocks for sessions idle more than seven days. A prompt after that
-- long starts a new sitting rather than answering the previous response.
-- name: SweepStaleSessionTurnClocks :exec
DELETE FROM router.session_turn_clocks
WHERE updated_at < now() - interval '7 days';
