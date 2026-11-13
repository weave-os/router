BEGIN;

-- A subscription seat's newest quota reading has no index to stand on: the
-- only scoped option is (installation_id, timestamp DESC), so each seat walks
-- every turn in its installation until it finds one of its own rows. Measured
-- on the prod replica, reading the latest limit headers for the busiest
-- installation's 27 seats over 8 days ran 21.9s, filtering 261k rows per seat;
-- seats idle since their weekly limit ran out pay the full walk.
--
-- Partial because only subscription-served upstream spans carry a seat, which
-- keeps the index to a fraction of the table. upstream_status_code rides along
-- so counting a seat's served turns over a window stays index-only.
CREATE INDEX model_router_request_telemetry_subscription_account_idx
    ON router.model_router_request_telemetry (subscription_account_id, "timestamp" DESC)
    INCLUDE (upstream_status_code)
    WHERE span_type = 'router.upstream' AND subscription_account_id IS NOT NULL;

COMMENT ON INDEX router.model_router_request_telemetry_subscription_account_idx IS
    'Serves per-seat subscription usage: a seat''s newest turn-serving spans, their limit headers, and served-turn counts.';

COMMIT;
