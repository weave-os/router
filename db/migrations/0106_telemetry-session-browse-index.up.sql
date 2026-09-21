BEGIN;

-- Browsing sessions newest-first has no index to stand on: the planner falls back
-- to idx_router_request_telemetry_api_key_id, which orders by the wrong leading
-- column and pays a heap read per candidate row. Measured on the prod replica, a
-- 24h `GROUP BY session_id ORDER BY max(timestamp) DESC` ran 4.5s, of which 4.2s
-- was shared-buffer reads (26.6k blocks) plus an external merge sort to disk.
--
-- Ordering on timestamp with session_id as a payload column makes that an
-- index-only scan over just the turn-serving rows, so the aggregate never
-- touches the heap. INCLUDE rather than a second key column keeps the index at
-- one tuple per row without widening the btree's comparison path.
--
-- Partial because ~96% of rows qualify but the exclusions are exactly the rows a
-- session browse must never surface: non-upstream spans and sessionless traffic.
CREATE INDEX model_router_request_telemetry_session_browse_idx
    ON router.model_router_request_telemetry ("timestamp" DESC)
    INCLUDE (session_id)
    WHERE span_type = 'router.upstream' AND session_id IS NOT NULL;

COMMENT ON INDEX router.model_router_request_telemetry_session_browse_idx IS
    'Serves the admin session browse: newest-first turn-serving spans, session_id inline for index-only aggregation.';

-- Resolving which installation owns a session is the same gap from the other
-- direction, and it has no index at all: looking a session up by id alone seq
-- scans the table, measured at 3.0s on the prod replica. The browse index cannot
-- serve it -- session_id is an INCLUDE payload there, not a searchable key.
--
-- Needed because an operator following a session id out of a log or a feedback
-- note has no installation to scope by, and requiring one is the thing this
-- unblocks. installation_id rides along so the lookup stays index-only.
CREATE INDEX model_router_request_telemetry_session_lookup_idx
    ON router.model_router_request_telemetry (session_id)
    INCLUDE (installation_id)
    WHERE span_type = 'router.upstream' AND session_id IS NOT NULL;

COMMENT ON INDEX router.model_router_request_telemetry_session_lookup_idx IS
    'Resolves a bare session id to its installation without an installation-scoped predicate.';

COMMIT;
