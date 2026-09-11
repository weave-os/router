# Fail-open runtime testing (PR2)

Runtime fault harness for an **already running, already authenticated** router.
It proves original-model fallback when the **generic policy sidecar** fails, not
when Postgres or HMM is down.

PR3 extends this stack for DB/auth failures. PR4 extends it for cold boot,
readiness, and inline roster. This document does not claim those.

## What this is

| Piece | Path after integration |
|---|---|
| Orchestrator | `scripts/smoke/fail_open.sh` |
| TCP/HTTP fault controller | `scripts/smoke/fail_open/controller.go` |
| Controller unit tests | `scripts/smoke/fail_open/controller_test.go` |
| Isolated compose overlay | `docker-compose.fail-open.yml` |

The overlay is **never auto-loaded**. It is passed with explicit `-f` plus a
unique `COMPOSE_PROJECT_NAME`. It resets developer `env_file` entries and host
port bindings so a laptop `docker compose up` is left alone.

## Invocation

From the **router module root** (`weave-os/router`), after this bundle is copied
into the tree:

```bash
# fixture unit tests (no Docker)
cd scripts/smoke/fail_open && go test -count=1 .

# runtime suite (Docker; no real inference spend)
./scripts/smoke/fail_open.sh
```

Leave the project up while iterating:

```bash
FAIL_OPEN_KEEP_STACK=1 FAIL_OPEN_PROJECT=failopen-dev ./scripts/smoke/fail_open.sh
docker compose -p failopen-dev -f docker-compose.yml -f docker-compose.fail-open.yml down -v
```

Use native `make` / `go` / `docker`. Do not use `wv`.

## Why a generic policy fixture

Frozen HMM needs an artifact package and an embedding probe. This harness
registers `ROUTER_DEFAULT_STRATEGY=failopen` with

```text
ROUTER_POLICY_SIDECARS={"failopen":"http://policy:8091"}
```

`failopen` is not a reserved strategy id. Capability discovery is optional at
boot; `/route` is the request-path dependency under test.

## Unconditional cluster scorer

`cmd/router/main.go` still builds the cluster scorer and **panics** if ONNX
assets cannot load. This harness does **not** add a runtime toggle to skip that.
If `/health` never comes up and logs contain `Cluster scorer failed to build;
refusing to boot`, populate image assets (`ROUTER_ONNX_ASSETS_DIR`, see
`docs/CONFIGURATION.md`) and rebuild. Treat that as a failed prerequisite, not PASS.

Pub/Sub client construction also panics today if the emulator is missing. PR2
starts postgres + pubsub + migrate + seed **while healthy**, then starts
`server` independently of Compose `service_healthy` gates. That matches PR2
scope (running process). Cold start with those services down is PR4.

## Isolation rules

- Unique compose project name (`failopen-$$` by default).
- Overlay `ports: !reset []` on postgres, pubsub, and server (internal Docker DNS only).
- Overlay `env_file: !reset []` so `.env.local` keys cannot reach a real provider.
- Placeholder `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` / `GOOGLE_API_KEY` only.
- `OPENAI_BASE_URL` and `GOOGLE_BASE_URL` point at stub controllers.
- Anthropic composition has **no** `ANTHROPIC_BASE_URL`; OpenAI Chat Completions
  against `gpt-4.1-mini` is the default exercised path so the original model is
  observable on the OpenAI stub. Messages/Gemini can be added the same way once
  an Anthropic base-URL override exists or caller-credential relay is used.
- Cleanup removes only project `$PROJECT` volumes/containers.

## Active faults (not container-stop only)

The controller listens on `:8091` (fault) and `:8092` (control).

| Mode | TCP/HTTP behavior |
|---|---|
| `refuse` | Fault listener closed → SYN refused |
| `stall` | Connection accepted; HTTP never completed |
| `reset` | Connection accepted; `SO_LINGER 0` close (RST) |
| `fivexx` | HTTP 502 |
| `malformed` | HTTP 200 with non-JSON body |
| `impossible` | JSON selecting `not-a-real-model-zzzz` |
| `ok` | Valid `/route` or provider fixture |

Control API (internal network):

```text
POST http://policy:8092/mode?set=refuse
GET  http://policy:8092/stats
POST http://policy:8092/reset-stats
GET  http://policy:8092/last-body
```

The same binary is used for `policy`, `openai`, `anthropic`, and `google`
roles.

## PR2 cases the script runs

1. Policy TCP refusal → one OpenAI stub call with the original `model`.
2. Policy stall → one OpenAI stub call.
3. Policy RST → one OpenAI stub call.
4. Policy 5xx, malformed JSON, impossible selection → original model, one call.
5. Restore policy `ok` without restarting `server` (`/health` still answers).
6. Client issues **one** request (no client retries). A fallback that then
   hits a refusing provider is terminal (single client attempt).

Assertions use stub `/stats` hit counts and captured `model`, plus a single
`STATUS` line from the client helper.

SSE fixtures under `internal/proxy/testdata/conformance/` remain the in-process
conformance source. This harness does not duplicate those files.

## Feature flags

Requires the PR2 wiring already in tree:

- `ROUTER_DEPENDENCY_FAIL_OPEN=true`
- response header `X-Router-Fail-Open` on a relayed response

If the flag is off, original-model fallback will not run; the script must FAIL,
not skip.

## Out of scope (do not treat a green PR2 run as these)

- Database refuse/restart/blackhole, auth cache cold/expired, spend gates (PR3).
- Cold router boot with DB/PubSub/HMM disconnected, readiness changes, inline
  roster JSON (PR4).
- Real Anthropic/OpenAI/Google spend.
- Production or customer data.

## Suggested `docs/README.md` row

Do not edit the source README from this worktree. After integration, add:

| [FAIL_OPEN_TESTING.md](FAIL_OPEN_TESTING.md) | Isolated compose + TCP fault controller for original-model fail-open (PR2 policy/sidecar faults; PR3–4 extend). |

## Main's remaining job

Copy this bundle into `weave-os/router`, wire any missing Anthropic stub base
URL if Messages coverage is required in the same PR, run:

```bash
cd scripts/smoke/fail_open && go test -count=1 .
./scripts/smoke/fail_open.sh
```

then commit on the PR2 branch. This worktree does not run that suite.
