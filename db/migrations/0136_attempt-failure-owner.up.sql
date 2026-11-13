BEGIN;

ALTER TABLE router.model_router_inference_attempts
    ADD COLUMN error_class VARCHAR,
    ADD COLUMN failure_owner VARCHAR,
    ADD COLUMN body_translated BOOLEAN;

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN failure_owner VARCHAR;

COMMENT ON COLUMN router.model_router_inference_attempts.error_class IS 'How the attempt failed (TurnErrorClass vocabulary); NULL on a served attempt';
COMMENT ON COLUMN router.model_router_inference_attempts.failure_owner IS 'Who owns the attempt failure: provider, router, client or unknown; NULL on a served attempt';
COMMENT ON COLUMN router.model_router_inference_attempts.body_translated IS 'TRUE when the router rewrote the request body it sent (format translation or same-format rewrite); NULL on rows written before this column';
COMMENT ON COLUMN router.model_router_request_telemetry.failure_owner IS 'Who owns the turn failure: provider, router, client or unknown; NULL on a normal completion';

COMMIT;
