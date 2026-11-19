BEGIN;

CREATE TABLE router.model_router_user_seat_usage (
  router_user_id           UUID NOT NULL REFERENCES router.model_router_users(id) ON DELETE CASCADE,
  provider                 VARCHAR NOT NULL,
  primary_used_percent     DOUBLE PRECISION NOT NULL,
  primary_window_minutes   BIGINT NOT NULL,
  primary_resets_at        TIMESTAMPTZ,
  secondary_used_percent   DOUBLE PRECISION NOT NULL,
  secondary_window_minutes BIGINT NOT NULL,
  secondary_resets_at      TIMESTAMPTZ,
  overage_in_use           BOOLEAN NOT NULL,
  observed_at              TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (router_user_id, provider)
);

COMMIT;
