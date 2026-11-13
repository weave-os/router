BEGIN;

-- Half-open recovery probes are shared across router workers. Expiry lets a
-- later request recover the slot if the worker exits before releasing it.
CREATE TABLE router.session_pin_recovery_probe_leases (
  session_key BYTEA NOT NULL CHECK (octet_length(session_key) = 16),
  model VARCHAR NOT NULL CHECK (model <> ''),
  lease_token UUID NOT NULL,
  lease_until TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (session_key, model)
);

CREATE INDEX session_pin_recovery_probe_leases_expiry_idx
  ON router.session_pin_recovery_probe_leases (lease_until);

COMMIT;
