# CI performance

This document records the September 2026 CI timing investigation, the changes
made from it, and the remaining optimization backlog. The baseline uses wall
time from `run_started_at` to `updated_at` for successful pull-request runs in
the latest 100 workflow entries available on 2026-09-11.

## Baseline

| Workflow | Successful PR runs | Median | p90 | Maximum |
|---|---:|---:|---:|---:|
| Test | 70 | 3m 57s | 4m 24s | 5m 14s |
| Smoke | 82 | 4m 26s | 4m 51s | 6m 15s |

[Test run 34648736869](https://github.com/weave-os/router/actions/runs/34648736869)
showed why the Test critical path was longer than any individual gate:

| Job | Duration |
|---|---:|
| Inference boundary | 60s |
| Test frozen HMM sidecar | 59s |
| Test installer | 43s |
| Test cc-statusline | 27s |
| Test (Go checks, after the first two gates) | 173s |

The Go job did not start until the inference-boundary and HMM jobs completed,
so the workflow paid roughly 60s plus 173s on its critical path.

## Completed changes

The main Go checks now start at the same time as the other jobs. A small final
job retains the required check name `Test` and fails unless every constituent
job succeeds. This keeps branch protection stable while changing the critical
path to the longest constituent job plus the fan-in job's startup time.

All `actions/setup-go` uses now install Go 1.25.9 explicitly, matching the
`toolchain go1.25.9` directive in [`go.mod`](../go.mod). Previously setup-go
installed the `go 1.25.0` directive first. Go then downloaded 1.25.9 into the
module cache before setup-go restored its roughly 299 MB archive. Cache
extraction repeatedly failed with `Cannot open: File exists`, and the restored
cache was discarded. Keep the workflow pins synchronized when the toolchain
directive changes.

## Follow-up backlog

### P0: repair the nightly cassette refresh

The scheduled Smoke workflow is functionally broken. The smoke tests record
fresh cassettes successfully, but the Docker container writes files that the
runner cannot read. The subsequent Git step fails with errors such as:

```text
error: open("smoke/mitmproxy/cassettes/...json"): Permission denied
fatal: cannot hash smoke/mitmproxy/cassettes/...json
fatal: updating files failed
```

All 20 scheduled runs inspected failed this way. Public example:
[run 34574571427](https://github.com/weave-os/router/actions/runs/34574571427).

Make the recording container write with the runner's UID and GID. An ownership
normalization step before Git operations is a smaller fallback, but aligning
the writer avoids producing inaccessible workspace files in the first place.
The fix is complete when a scheduled record run can read and stage every
cassette, then either report no drift or open the refresh PR.

### 3. Make Smoke's Docker cache effective and remove the cold seed container

The Smoke Compose overlay declares a GitHub Actions cache, but sampled BuildKit
logs did not contain cache import or export activity. Expensive layers rebuilt
on every run: `npm ci` and the UI build took roughly 32s each, two
`go mod download` layers took 27-32s, and the Go builds took roughly 13s and
41s. The separate `golang:1.25-bookworm` seed service added about 47s while it
downloaded and compiled on a cold container.

Use an explicit `docker buildx bake` or `docker/build-push-action` build step
with stable per-image `type=gha` scopes, then start Compose with `--no-build`.
Import the cache from `main` so a new PR branch can reuse it. Build the seed
binary into an existing image, or a small dedicated image, and invoke that
artifact instead of `go run` in a fresh SDK container.

Verify this by checking that warm-run logs show both cache import and export,
and by comparing the build and seed phases across at least ten comparable PR
runs.

### 4. Cancel superseded Test runs

The Smoke workflow already cancels an older run when a PR is updated, but the
Test workflow does not. Eleven overlapping stale Test runs in the inspected
sample consumed 23.1 runner-minutes after a newer commit existed.

Add workflow concurrency keyed by PR number, falling back to the ref for push
runs, with `cancel-in-progress: true`. Keep scheduled or manually dispatched
workflows in separate groups where cancellation would change their semantics.

### 5. Remove real retry delays from proxy tests

Fourteen tests under [`internal/proxy`](../internal/proxy) spent 15.04s sleeping
inside a package whose complete test time was 17.18s. They exercise the real
250ms exponential retry delay, including overload-exhaustion cases. Other tests
already inject the package's no-op sleep function.

Inject the no-op sleep consistently in tests that validate retry decisions,
and reserve real-clock coverage for a narrowly scoped timing test if it is
needed. Assertions must continue to verify attempt counts, failover, and
provider-disable behavior. The package should fall to low single-digit seconds
without reducing behavioral coverage.

### 6. Collapse redundant Go compilation passes

With isolated cold build caches, the current sequence
`go vet ./...`, `go build -o /dev/null ./...`, and
`go test -count=1 ./...` took 45.89s locally. A single
`go test -vet=all -count=1 ./...` took 40.36s, about 12% less.

After confirming the repaired CI cache's warm behavior, remove the separate Vet
and Typecheck steps and run tests with full vetting. Keep golangci-lint separate.
Compare both cold and warm runs because eliminating the early steps changes
which command populates the Go build cache.

### 7. Path-gate component-specific jobs

The statusline, installer, and frozen HMM sidecar jobs run for every pull
request, even when their inputs cannot have changed. Add a changed-files
classifier and job-level conditions for their owning paths. The final `Test`
fan-in must treat an intentionally skipped optional job as acceptable while
still failing on cancellation or failure. Include the workflow and shared build
inputs in every component's path set so CI changes cannot bypass coverage.

This primarily saves runner capacity rather than wall time because these jobs
already run in parallel.

### 8. Bound jobs and expose phase timings

Set `timeout-minutes: 10` on the Test and HMM jobs and
`timeout-minutes: 15` on Smoke after confirming those limits leave headroom over
the measured p90. Split Smoke reporting into build, boot, seed, and assertion
phases, and publish their elapsed times in the job summary. This makes a cache
regression or service-startup stall visible without downloading the complete
log.

## Measuring future changes

Use successful pull-request runs with comparable changed paths. Report median,
p90, and maximum wall time, plus runner-minutes when a change affects parallel
work rather than the critical path. Treat cancelled, skipped, and
`action_required` runs separately so they do not appear as zero-duration
successes.
