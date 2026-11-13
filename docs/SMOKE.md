# Router pre-merge smoke suite

The smoke suite boots the real router (docker compose stack) and drives it with
deterministic, Claude-Code-shaped request fixtures, asserting the behavior that
in-process unit and conformance tests cannot see: HTTP status, response/usage
shape, prompt-cache accounting, decision headers, and tool-schema translation
to OpenAI's structured-output format. Replay checks the recorded provider
contract; only explicitly authorized recording checks the current live API.

It exists because the regression class it targets is invisible to `go test`. Two
concrete examples that motivated it:

- #820 turned on router `cache_control` breakpoint injection for the
  Anthropic→Anthropic path and could emit breakpoint combinations that only the
  *real* Anthropic API rejects (a 5th breakpoint past the 4-cap, or a router `5m`
  breakpoint ordered before a client `ttl=1h` one → hard 400). #821 fixed it hours
  later. The conformance suite stops at `proxy.Service` with a mock upstream, so it
  never observed the 400.
- A tool with a genuinely typeless optional parameter (no `type`/`anyOf`/`enum`
  at all — by design, meaning "accept any JSON value") 400'd against the real
  OpenAI Responses API: `strictifyOpenAISchema`'s nullable-wrapping fallback
  wrapped the bare node in an invalid `anyOf` without checking it carried a
  strict-expressible type first. Caught unit-level in
  `internal/translate/strictify_openai_test.go`, and end-to-end in
  `smoke/openai_test.go` — the unit test proves the translator produces the
  right JSON; recording confirms live acceptance, and replay guards that shape.

## Architecture: a record/replay proxy sits between the router and its providers

`smoke/mitmproxy/` is a small MITM (man-in-the-middle) forward proxy. The
router's HTTP transport already honors `http.ProxyFromEnvironment`
(`internal/providers/httputil`), so pointing the `server` container at it via
`HTTPS_PROXY` — and trusting its ephemeral CA via `SSL_CERT_DIR` — intercepts
every outbound call with **zero router code changes**. It mints a TLS leaf cert
per CONNECT-target hostname, so it's not Anthropic-specific: the same proxy
intercepts calls to `api.anthropic.com` and `api.openai.com` alike.

Three modes (`SMOKE_PROXY_MODE`):

| Mode | What it does | Needs a key? |
|---|---|---|
| `replay-only` (local and CI default) | Serves cassettes committed under `smoke/mitmproxy/cassettes/`; a cache miss is a clean 502, not a hang | No |
| `record` | Always calls the real API and (re)writes cassettes | Yes |
| `replay-or-record` (explicit opt-in) | Serves from cache, falls back to live + record on a miss | Yes, before starting |

Cassettes are keyed by `sha256(method + path + body)`, with volatile fields
removed from the body first (`normalizeRequestBody` in
`smoke/mitmproxy/store.go` — today only `prompt_cache_key`, the router's
session-affinity hint, which is derived from the API key id the smoke script
mints fresh on every run). The fixtures are otherwise byte-deterministic
(`smoke/fixtures/system_prompt.txt` never changes), so a given scenario hashes
identically run to run — this is what makes `replay-only` CI runs deterministic
and free. A request field that varies per run has to be added to
`volatileBodyFields` or every cassette for that path becomes a permanent miss.

Response headers are sanitized before a cassette is written (`Authorization` /
`x-api-key` / org identifiers / rate-limit and request-id noise are removed).
**Response bodies are not anonymized.** Only record authored synthetic scenarios;
never import production conversations, customer identifiers, or captured prompts
into this public repository. Review cassette content before publication. Header
scrubbing alone does not establish that a capture is safe to commit.

This means the CI job needs **no provider API keys at all** for its normal
path-gated run — it replays what's already checked in. Keys are only needed to
*record*, which happens locally or in a scheduled nightly refresh.

## When it runs

- **Selected per PR.** The workflow (`.github/workflows/smoke.yml`) uses the same
  `scripts/agent_checks.py selected smoke` contract as local validation. Request
  execution paths include dispatch, policy, ingress, SSE, gateway/serving, and
  provider/translation changes—not just the older proxy package. The workflow
  has no separate top-level path list that can silently omit a new component.
  Replay needs no provider secret; orchestration safety tests run before selection.
- **On demand** via the workflow's `workflow_dispatch` button.
- **Locally** before merging a risky router change, or to refresh cassettes:
  `make smoke` (replay-only by default) or
  `ANTHROPIC_API_KEY=… SMOKE_PROXY_MODE=record make smoke` (real API, updates
  cassettes).

## Running locally

```bash
make smoke                                          # replay-only, no key needed
ANTHROPIC_API_KEY=sk-ant-… SMOKE_PROXY_MODE=record make smoke   # refresh Anthropic cassettes
ANTHROPIC_API_KEY=sk-ant-… OPENAI_API_KEY=sk-… SMOKE_PROXY_MODE=record make smoke   # refresh both providers' cassettes
```

That runs `scripts/smoke/run.sh`, which:

1. Requires Python 3.11+ and Compose 2.24.4+; reserves a UUID-named project only
   after verifying no containers, volumes, or networks already have that name.
2. Creates an owner-only temporary override outside the checkout. It removes
   all developer `env_file` inputs and disables implicit `.env` loading, replaces
   the server environment with fixture settings, removes database/pubsub host
   ports, and asks Docker for an available loopback-only router port. Existing
   Compose projects, `.env.local`, and user overrides are never adopted or edited.
3. In replay mode, ignores exported provider keys, mounts cassettes read-only,
   and keeps the router/proxy on an internal Docker network without an Internet
   route. A fixed-destination BusyBox TCP relay (from the existing Postgres image)
   joins a separate ingress network and publishes only a loopback port. It forwards
   only to `server:8080`; neither runtime service joins the ingress network.
   Explicit recording supplies only exported Anthropic/OpenAI credentials and
   allows egress/cassette writes. Building images may download dependencies in
   either mode; zero provider calls is not zero build-network access.
   Direct execution uses the same child-environment safety rules as the agent
   validator: ambient live-test/database settings and credentials are stripped,
   and approved Go flags replace flags that could skip tests. Existing Go cache
   configuration is preserved. Explicit model pins remain supported; the base
   URL and router key always come from the newly created fixture stack.
4. Builds uniquely tagged local images (or uses CI's prebuilt images), boots the
   isolated project, discovers its port, waits for `/health`, and seeds a local key.
5. Runs `go test -tags smoke -count=1 -v ./smoke/` against only that port.
6. On success or failure, verifies resource ownership labels before removing
   that project's containers/volumes/networks and invocation-built image tags.
   Prebuilt/shared images are never removed. Cleanup errors fail the run and
   retain its override for recovery. It never runs an unscoped `down -v` or emits
   raw seed output/automatic container log dumps. BuildKit's shared layer cache remains.

Commands have bounded deadlines: 20 minutes for image builds, 3 minutes for
startup, 10 minutes for assertions, and 2 minutes for other commands. On timeout
or interruption, the runner terminates its command process group (including
compiler/test children), allows 10 seconds for graceful exit, then kills any
remaining processes before checking ownership and cleaning up. A timeout or
failed cleanup cannot be reported as a passing run.

Iterating on a scenario? Keep the stack up between runs:

```bash
SMOKE_KEEP_STACK=1 make smoke
# ...edit a scenario...
# Source the exact owner-only test.env path printed by the runner:
source /printed/run/directory/test.env
go test -tags smoke -count=1 -v ./smoke/ -run TestCaching
# Use the exact quoted, project-scoped teardown command printed by the runner.
# Then remove that run's retained temporary directory.
```

The printed teardown command retains every required override path, even when
the checkout path contains spaces. `test.env` contains a fixture router key,
not production credentials. Each invocation creates a new stack; keeping one
does not make the next invocation reuse it. `SMOKE_BASE_URL` is supported by the
Go test client only and rejected by the orchestrator, to prevent accidental
tests or cleanup against an existing/local/production service.

Validate the lifecycle without starting containers or calling providers:

```bash
python3 -m unittest discover -s scripts/smoke -p 'test_*.py' -v
```

The opt-in network regression creates and removes its own two-container fixture;
CI runs it before the full smoke build:

```bash
SMOKE_TEST_DOCKER=1 python3 scripts/smoke/test_runner.py ComposeMergeTest.test_replay_ingress_reaches_internal_server_without_default_route
```

These tests drive the real entrypoint with inert Docker/curl/Go executables and,
when Compose is installed, verify the actual merged config without a daemon.

## Cost

`replay-only` runs make zero upstream calls. `record`/`replay-or-record` pin
most Anthropic scenarios to the cheapest model (`claude-haiku-4-5`); the
mid-conversation tool-change scenario uses `claude-opus-5`, the minimum model
that supports that beta. OpenAI scenarios use the cheapest reasoning tier
(`gpt-5.4-nano`). All pins use `x-weave-force-model` and cap `max_tokens` — a
full refresh is ~15 real calls across both providers, a few cents. Skip
recording OpenAI by omitting
`OPENAI_API_KEY`; `smoke/openai_test.go` skips itself
(`SMOKE_OPENAI_ENABLED=0`, set automatically by `run.sh` in that case).

## What it covers

| File | Scenario |
|---|---|
| `smoke/boot_test.go` | `/health`, `/v1/version`, `/v1/router/models` respond and are well-formed |
| `smoke/basic_test.go` | `/force-model` command turn; non-stream turn (usage + decision headers); streamed turn well-ordered; unknown `x-weave-force-cluster` (against the stub classifier in `smoke/hmmstub`) / unknown `x-weave-force-model` refused with 400 pre-dispatch |
| `smoke/cache_test.go` | router-injected caching warms then reads; client-at-capacity doesn't over-inject; `ttl=1h` breakpoint not poisoned; overflow rejected cleanly by the router |
| `smoke/streaming_test.go` | tool-use stream lifecycle: balanced `content_block_start/stop`, exactly one `message_stop`, `stop_reason` present |
| `smoke/tool_delta_test.go` | non-system `tool_addition` and `tool_removal` blocks are normalized and accepted by Anthropic |
| `smoke/openai_test.go` | OpenAI Responses-API translation path (gpt-5.x + tools): a genuinely typeless optional tool param round-trips without a 400; basic turn served correctly |

## Regression proof

The suite is built to catch the #820 class. Prove regressions in a detached
temporary worktree, never by overwriting the current checkout's files:

```bash
git worktree add --detach /chosen/unused/regression-worktree <buggy-revision>
# Apply only the new synthetic regression test and safe runner to that worktree.
# Run the targeted test: it must fail for the incident's behavioral reason.
# Run the same test in the fixed checkout: it must pass.
```

Do not use an old smoke runner with shared-stack cleanup. A missing dependency,
old fixture mismatch, or build failure is not a successful fail-before proof.

## Adding a scenario

1. Build the request with `newRequest(userID)` in `smoke/request_builder_test.go`
   — chain `.tokens()`, `.streaming()`, `.sysCache()`, `.msgCache()`,
   `.toolCache()`, `.cachedTools()`, `.withTool()` etc. to shape breakpoints,
   turn size, and the tool registry. The large stable prefix
   (`smoke/fixtures/system_prompt.txt`) is prepended automatically so caching
   engages.
2. Add a `t.Run(...)` subtest to the relevant `*_test.go`, using `call(t, body)`
   (Anthropic, the suite-wide pin) or `callModel(t, body, model)` (any other
   model/provider — see `smoke/openai_test.go`) and the shared assertions
   (`requireOKMessage`, `assertServedByPin`/`assertServedByModel`,
   `assertStreamWellFormed`).
3. Keep it cheap: pin the cheap tier for whichever provider you're targeting,
   cap `max_tokens`, avoid multi-turn loops.
4. Record the new cassette: `ANTHROPIC_API_KEY=… [OPENAI_API_KEY=…]
   SMOKE_PROXY_MODE=record make smoke`, then review and commit the new
   file(s) under `smoke/mitmproxy/cassettes/`.

## Refreshing cassettes

Cassettes go stale when a fixture, scenario, or a provider's own response shape
changes. See `smoke/mitmproxy/cassettes/README.md`. Refresh with:

```bash
ANTHROPIC_API_KEY=sk-ant-… OPENAI_API_KEY=sk-… SMOKE_PROXY_MODE=record make smoke
git status smoke/mitmproxy/cassettes/   # review the diff, then commit
```

## Config (env)

| Var | Default | Meaning |
|---|---|---|
| `SMOKE_PROXY_MODE` | `replay-only` | `replay-only` \| `record` \| `replay-or-record` |
| `ANTHROPIC_API_KEY` | — | required only when `SMOKE_PROXY_MODE` isn't `replay-only` |
| `OPENAI_API_KEY` | — | optional even in `record` mode — omit to skip recording/refreshing the OpenAI-path scenarios |
| `SMOKE_PIN_MODEL` | `claude-haiku-4-5` | Anthropic model the default scenarios force |
| `SMOKE_OPENAI_PIN_MODEL` | `gpt-5.4-nano` | OpenAI model `smoke/openai_test.go` forces |
| `SMOKE_BASE_URL` | Docker-allocated loopback port | set by the runner for tests; rejected as an orchestrator input |
| `SMOKE_KEEP_STACK` | `0` | leave the stack up after the run |
| `SMOKE_CI_CACHE` | `0` | layer-cache the server/mitmproxy builds via the GitHub Actions cache backend. **CI-only** — set only by `.github/workflows/smoke.yml`; hard-errors outside a real GitHub Actions runner, never set locally |
| `SMOKE_PREBUILT` | `0` | use CI's `router-server`, `router-seed`, `router-mitmproxy`, `router-hmm-stub` images; otherwise build per-run image tags |

## CI build caching

The router's own `Dockerfile` is expensive to build cold: a Next.js build for
the mini UI, then the Go compile. `.github/workflows/smoke.yml` sets `SMOKE_CI_CACHE=1`, which makes
`run.sh` add a fourth compose overlay,
`smoke/mitmproxy/docker-compose.ci-cache.yml`, layering `cache_from`/`cache_to:
type=gha,mode=max` onto both the `server` and `mitmproxy` builds.

That overlay is deliberately **separate** from the always-on
`smoke/mitmproxy/docker-compose.yml` — `type=gha` needs
`ACTIONS_CACHE_URL`/`ACTIONS_RUNTIME_TOKEN`, which only exist inside an actual
GitHub Actions run, and it hard-errors (not silently skips) without them. Never
add `cache_from`/`cache_to: type=gha` to a compose file `make smoke` loads by
default.

Expected effect: a cold build (new runner, or a change touching the
Dockerfile/go.mod) still pays the full ~8-12 minutes. A warm-cache PR run
(most PRs — only the Go source under `internal/`/`cmd/` changed) drops to low
single-digit minutes, since `mode=max` caches every intermediate build stage,
not just the final layer.

## CI secret

The normal path-gated PR run needs **no secrets** — it replays committed
cassettes. `ANTHROPIC_API_KEY`/`OPENAI_API_KEY` are only supplied for an explicitly
selected manual recording mode or the scheduled nightly refresh. The replay PR
step does not receive them, including on same-repository PRs. Only maintainers
can configure recording secrets; fork PRs cannot record.

## Relationship to `test-claude-locally`

The `.claude/skills/test-claude-locally` skill drives an interactive client against
the real API and requires separate live-call authorization. Prefer hermetic
unit/conformance regressions and isolated replay for the default agent fix
workflow. A production incident is not permission to record customer content,
change client routing, or spend against a provider.
