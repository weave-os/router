-- Atomically claim one expired or unused recovery probe lease across all
-- router workers. An active lease produces zero affected rows. The expiry is
-- computed on the database clock that the conflict guard and sweep compare
-- against, so a worker with a skewed clock cannot shorten or extend a lease.
-- name: AcquireSessionPinRecoveryProbeLease :execrows
INSERT INTO router.session_pin_recovery_probe_leases (session_key, model, lease_token, lease_until)
VALUES (
  @session_key::bytea, @model::varchar, @lease_token::uuid,
  CURRENT_TIMESTAMP + make_interval(secs => @lease_seconds::double precision)
)
ON CONFLICT (session_key, model) DO UPDATE SET
  lease_token = EXCLUDED.lease_token,
  lease_until = EXCLUDED.lease_until
WHERE router.session_pin_recovery_probe_leases.lease_until <= CURRENT_TIMESTAMP;

-- Release only the lease acquired by this attempt, so a delayed completion
-- cannot remove a newer worker's lease.
-- name: ReleaseSessionPinRecoveryProbeLease :exec
DELETE FROM router.session_pin_recovery_probe_leases
WHERE session_key = @session_key::bytea
  AND model = @model::varchar
  AND lease_token = @lease_token::uuid;

-- Remove abandoned rows after their safety expiry; active rows remain until
-- their request releases them.
-- name: SweepExpiredSessionPinRecoveryProbeLeases :exec
DELETE FROM router.session_pin_recovery_probe_leases
WHERE lease_until <= CURRENT_TIMESTAMP;
