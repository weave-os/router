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

## Follow-up status

### P0: repair the nightly cassette refresh (implemented)

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

The recorder now changes its temporary cassette file from `0600` (the default
from Go's `os.CreateTemp`) to `0644` before the atomic rename. That preserves
the bind-mounted write path while allowing the host runner to read and hash
the file after the container exits. The fix is complete when a scheduled
record run can read and stage every cassette, then either report no drift or
open the refresh PR.

### 3. Make Smoke's Docker cache effective and remove the cold seed container (implemented)

The Smoke Compose overlay declares a GitHub Actions cache, but sampled BuildKit
logs did not contain cache import or export activity. Expensive layers rebuilt
on every run: `npm ci` and the UI build took roughly 32s each, two
`go mod download` layers took 27-32s, and the Go builds took roughly 13s and
41s. The separate `golang:1.25-bookworm` seed service added about 47s while it
downloaded and compiled on a cold container.

The CI workflow now uses `docker/bake-action` with explicit
[`docker-bake.smoke.hcl`](../docker-bake.smoke.hcl) targets to build the
server, MITM proxy, and seed images with `type=gha` cache scopes, loads those
images, and starts Compose without `--build`. Keeping the Bake definition
explicit avoids Compose-profile discovery differences between runner Docker
versions. Local runs retain an explicit Compose build. The seed service builds
a small `seed-runtime` target instead of launching a fresh
`golang:1.25-bookworm` SDK container and running `go run`.

The first PR run after the change imported and exported all three cache scopes
and took 274s in the image-build phase (5m47s job wall time). A same-PR rerun
hit the imported layers (`CACHED` in the BuildKit log), reduced image-build
time to 27s, and reduced the job to 1m48s. The smoke summary now exposes the
build, boot/health, seed, and assertion phases so future regressions are easy
to separate from cache-transfer time. Continue tracking at least ten
comparable PR runs before treating those two samples as a stable new baseline.

### 4. Cancel superseded Test runs

The Test workflow now follows the same policy as Smoke. Before this change,
eleven overlapping stale Test runs in the inspected sample consumed 23.1
runner-minutes after a newer commit existed.

The concurrency group is keyed by PR number, falling back to the ref for push
runs, with `cancel-in-progress: true`. If Test later gains scheduled or manual
triggers, keep those in separate groups where cancellation would change their
semantics.

### 5. Remove real retry delays from proxy tests (implemented)

Fourteen tests under [`internal/proxy`](../internal/proxy) spent 15.04s sleeping
inside a package whose complete test time was 17.18s. They exercise the real
250ms exponential retry delay, including overload-exhaustion cases. Other tests
already inject the package's no-op sleep function.

The retry backoff is now injectable through `Service.WithRetrySleep`. The
external integration tests that exercise overload, rescue, and retry paths use
the same no-op function as the internal dispatch tests. Assertions still verify
attempt counts, failover, and provider-disable behavior. A local package run
fell from about 23s to about 4s.

### 6. Collapse redundant Go compilation passes (implemented)

With isolated cold build caches, the current sequence
`go vet ./...`, `go build -o /dev/null ./...`, and
`go test -count=1 ./...` took 45.89s locally. A single
`go test -vet=all -count=1 ./...` took 40.36s, about 12% less.

The separate Vet and Typecheck steps were removed. `go test -vet=all -count=1
./...` now performs the compilation, vetting, and test pass in one command;
golangci-lint remains separate.

### 7. Path-gate component-specific jobs (implemented)

The workflow now classifies changed paths before the component jobs run. The
statusline, installer, and frozen HMM sidecar jobs are skipped when their inputs
are untouched. The final `Test` fan-in accepts an intentional skip but still
fails on a required job failure, cancellation, or classifier failure. Each
filter includes the workflow and shared build inputs so CI changes cannot
bypass coverage.

This primarily saves runner capacity rather than wall time because these jobs
already run in parallel.

### 8. Bound jobs and expose phase timings (implemented)

Test jobs now have ten-minute timeouts and Smoke jobs have fifteen-minute
timeouts. The smoke runner records build, boot/health, seed, and assertion
durations; CI writes them to the GitHub job summary. This makes a cache
regression or service-startup stall visible without downloading the complete
log.

## Measuring future changes

Use successful pull-request runs with comparable changed paths. Report median,
p90, and maximum wall time, plus runner-minutes when a change affects parallel
work rather than the critical path. Treat cancelled, skipped, and
`action_required` runs separately so they do not appear as zero-duration
successes.
