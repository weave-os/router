# Verify staging routing observations

Use this procedure to check that staging recorded routing decisions for a known
set of requests or to summarize a historical window. The command reads
`router.model_router_request_telemetry` in a read-only transaction. It uses the
existing telemetry rows and writes only its JSON report to stdout.

## Run the smoke test

1. Record the staging Router revision, routing profile or policy, test
   installation UUID, and UTC start time. Use an installation with no unrelated
   traffic during the test. Use a staging database role with read access to the
   telemetry table and set `DATABASE_URL` explicitly.
2. Send a test request on a route known to emit cluster metadata. Send a second
   request on a route known to bypass cluster scoring. Record the expected
   `decision_model` values and observable upstream attempts, including any retry
   or failover. The current Router has retired the legacy cluster strategy.
   When the deployed profile emits no cluster IDs, verify counts and model picks
   in the live smoke and use a historical window to check cluster IDs.
3. After telemetry has arrived, choose an exclusive UTC end time after the last
   attempt. Run the command from the Router repository. Replace the example UUID
   and timestamps with the recorded values.

```sh
go run ./cmd/verify-routing-observations \
  -installation 00000000-0000-0000-0000-000000000001 \
  -since 2026-01-01T12:00:00Z \
  -until 2026-01-01T12:05:00Z
```

Example output for two upstream actions:

```json
{
  "installation_id": "00000000-0000-0000-0000-000000000001",
  "since": "2026-01-01T12:00:00Z",
  "until": "2026-01-01T12:05:00Z",
  "upstream_rows": 2,
  "distinct_cluster_ids": [2, 7],
  "rows_without_clusters": 1,
  "model_picks": [
    {"decision_model": "model-a", "upstream_rows": 1, "percent": 50},
    {"decision_model": "model-b", "upstream_rows": 1, "percent": 50}
  ]
}
```

## Check the result

- Reconcile `upstream_rows` with the persisted upstream actions expected for the
  installation and UTC window. A request can lead to multiple actions. An empty
  window returns zero rows and empty lists. If you expected traffic, check the
  installation, timestamps, and telemetry arrival before recording a pass.
- Check that the cluster IDs expected from the clustered action appear. The
  report deduplicates IDs across all rows. Arrays without any recorded ID count
  toward `rows_without_clusters`, which can be expected on paths that bypass
  cluster scoring.
- Compare the model counts with the expected picks. Counts should sum to
  `upstream_rows`. A null `decision_model` reports a row with no recorded pick,
  and its share is included in the total.

For a historical window, use the same command with a closed interval and compare
the model distribution against the policy and traffic from that period. Traffic
mix and retries affect the distribution, so use the controlled smoke for exact
expectations. The retired decision logs did not capture cluster IDs, so they
cannot recover missing historical cluster IDs.

Record these fields with the smoke results:

```text
Staging revision and routing policy:
Installation and UTC window:
Requests sent and expected upstream actions:
Expected and observed row counts:
Expected and observed cluster IDs and missing-cluster count:
Expected and observed model picks:
Result and gaps:
```
