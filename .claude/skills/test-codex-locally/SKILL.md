---
name: test-codex-locally
description: Run the Weave router locally in docker compose and drive it with `codex exec` to reproduce and verify routing/translation/marker behavior for Codex's Responses API path. Use when verifying a router fix end-to-end for Codex, reproducing a prod Codex routing bug, confirming routing-marker / force-model / subscription-passthrough behavior, or testing a `/force-model` route — without touching the user's global Codex config.
---

# Testing the router locally with Codex

> For an **automated** pre-merge regression net (fixture-driven, asserts caching/streaming/decision-headers against real Anthropic), run `make smoke` — see [docs/SMOKE.md](../../../docs/SMOKE.md). This skill is the interactive counterpart for the **Codex CLI** (`codex exec`): stand the stack up by hand and drive it via a throwaway `CODEX_HOME` so `~/.codex/config.toml` is never edited.

Stand up the router in docker compose, point a one-off `codex exec` at it via `CODEX_HOME`, and read the local server logs to confirm behavior. Two upstream modes: the **real** provider API (needs a working key + credits, and for native GPT models a ChatGPT OAuth login) or a **mock** upstream that emits an exact SSE shape (deterministic, no credits).

The sibling skill [test-claude-locally](../test-claude-locally/SKILL.md) is the Claude Code (`claude -p`) counterpart. Stack bring-up and seeding are identical; only the client driver differs.

## Critical gotchas (read first)

- **Never edit `~/.codex/config.toml` or `~/.codex/auth.json`.** Those are the user's live Codex install (prod Weave Router + ChatGPT OAuth). Redirect with a throwaway `CODEX_HOME` directory that contains its own `config.toml` (and a *copy* of `auth.json` when ChatGPT OAuth is required). `CODEX_HOME` is how Codex finds config; there is no `--settings` flag equivalent to Claude Code.
- **`requires_openai_auth = true` is required.** Codex 0.149 hangs or never issues a `/v1/responses` request if the custom provider uses `requires_openai_auth = false` or `env_key`. Copy `~/.codex/auth.json` into the throwaway `CODEX_HOME` so Codex can attach the ChatGPT JWT + `ChatGPT-Account-ID`. The mock-router experiment that dropped OAuth never even hit the mock.
- **Codex always sends a ChatGPT JWT + `ChatGPT-Account-ID`.** The local router will classify that as a Codex subscription (`codexResponsesRequest` → native Responses passthrough for OpenAI decisions). That is the path this skill is usually exercising (routing markers on verbatim GPT frames). To force the prepaid/BYOK translation path instead, flip `subscription_routing_disabled=true` on the seeded installation (see step 3).
- **`codex exec` is one-shot.** A standalone `/force-model` call does not persist to the next `codex exec` (new session id every time, unless you `codex exec resume`). Put ` /force-model <model>` as the first line of the SAME prompt. The leading space is load-bearing — Codex consumes unknown slash tokens as local commands; a leading space makes it a normal user message the router can parse.
- **Pin via `config.toml`, not `codex -c`.** On Codex 0.149.1, `-c 'model_providers.weave-local.http_headers."x-weave-force-model"="…"'` did **not** merge into the outgoing request (no `x-weave-force-model applied` in server logs; the scorer served something else). Put `"x-weave-force-model" = "<id>"` on the same `http_headers` table in the throwaway `config.toml` (step 4). Confirm with `docker compose logs server | grep 'x-weave-force-model applied'` before drawing conclusions.
- **The router ignores the request's `model` field** for routing (cluster scorer / pins win). Codex's `-m gpt-5.5` only changes what Codex *requests*; `/force-model` or `x-weave-force-model` is the only way to pin a specific routed model. Native GPT family (`gpt-5.6-sol` / `terra` / `luna`, plus whatever `-m` Codex defaulted to) still goes through the scorer unless forced.
- **No GNU `timeout` on macOS.** Drive `codex exec` directly. If a run hangs (`Reading additional input from stdin...` with no banner), Codex is waiting on a TTY/stdin — do not pipe into it, and make sure `-s read-only` (or `--dangerously-bypass-approvals-and-sandbox`) is set.
- **Docker may not be running.** `docker compose` fails with a `unix://…/.docker/run/docker.sock` connect error if Desktop is stopped. `open -a Docker` and wait for `docker info` before step 1.
- **Other agents in the same workspace can `docker compose down` and delete `/tmp` scratch.** If health suddenly 404s or `CODEX_HOME` vanishes mid-run, re-up the stack and reseed — do not reuse a key you can no longer `/validate`.
- **Session key is `apiKeyID` + first user message.** Reusing the same curl/prompt body on the same key reuses the pin slot. `routingMarkerFor` then returns empty when `PriorServedModel == ServedIdentity()` (sticky same-model). For first-turn marker tests, seed a **new** key or change the first user text. Isolated `/validate` first: `curl -sS -o /dev/null -w '%{http_code}\n' -H "X-Weave-Router-Key: $KEY" http://localhost:8080/validate` must be `200`.
- **Native GPT passthrough + tool-only still has no badge.** `SetPassthroughBadge` can only rewrite `response.output_text.*` events. A ChatGPT-subscription turn that opens with reasoning and goes straight to a tool call never emits those, and the rewriter will not invent an `output_item` (would renumber a stream Codex expects to be byte-faithful). The translated path (`ensureBadgeItem`) *does* synthesize a leading message item ahead of a tool call. Don't treat a silent OAuth/tool-only GPT turn as a regression of the translated-path fix.
- **`GET /v1/models` 501 from a mock is fine.** Codex probes `<base_url>/models` first, logs an HTML 501, then continues to `POST /v1/responses`. The real local router implements `GET /v1/models` as Anthropic passthrough, so this only shows up against a Python mock.
- **Port 8085 conflict.** The monorepo's pubsub emulator may already own host port 8085. Drop the router's host binding with a `docker-compose.override.yml` (see workflow). The server still reaches the emulator over the compose network.
- **No credits / no key = no reproduction.** If the real upstream returns an error (e.g. OpenRouter "Insufficient credits"), use the mock-upstream path instead.

## Workflow

```
- [ ] 1. Bring up the stack (handle port 8085)
- [ ] 2. Seed an API key
- [ ] 3. Choose upstream + subscription vs prepaid path
- [ ] 4. Write a throwaway CODEX_HOME (copy auth.json)
- [ ] 5. Drive with `CODEX_HOME=... codex exec`, forcing the target model
- [ ] 6. Read local logs to verify behavior
- [ ] 7. Clean up
```

### 1. Bring up the stack

```bash
cd <router-repo>
# Drop the pubsub host-port binding to avoid an 8085 conflict:
cat > docker-compose.override.yml <<'EOF'
services:
  pubsub-emulator:
    ports: !reset []
EOF
docker compose up -d --build server   # --build picks up code changes
until curl -sf http://localhost:8080/health >/dev/null; do sleep 2; done
```

The override file is gitignored-by-intent scaffolding — delete it in cleanup.

### 2. Seed an API key

```bash
docker compose run --rm seed
```

Copy the `rk_...` key it prints — **only the line under** `Weave Router key (shown once — store it now):`. Seed stdout repeats the same token in curl examples and installer snippets; `grep -oE 'rk_[A-Za-z0-9]+'` can grab a stale key from an earlier seed in the same log. Always `/validate` before driving Codex:

```bash
curl -sS -o /dev/null -w '%{http_code}\n' \
  -H "X-Weave-Router-Key: rk_REPLACE_ME" http://localhost:8080/validate
# expect 200
```

A 401 mid-session usually means the stack was torn down and reseeded (new installation, old token dead).

### 3. Choose the upstream

**Real provider** — set the provider key in `.env.local` (e.g. `OPENAI_API_KEY=...`, `FIREWORKS_API_KEY=...`) and restart `docker compose up -d server`. Confirm the boot log shows `<Provider> provider enabled` with the real base_url. Use this to confirm a model genuinely produces the behavior.

**Mock upstream** — for a deterministic, credit-free repro of a precise SSE shape. Point the provider's base URL at a local mock and restart. Codex talks to the *router* (`POST /v1/responses`); the mock sits behind the router as the upstream the router dispatches to:

```bash
python3 .claude/skills/test-claude-locally/scripts/mock_openai_upstream.py >/tmp/mock.log 2>&1 &   # serves :8099
# In docker-compose.override.yml under `server:`, add:
#   environment:
#     FIREWORKS_BASE_URL: http://host.docker.internal:8099/v1
#     FIREWORKS_API_KEY: sk-mock
#   extra_hosts: ["host.docker.internal:host-gateway"]
docker compose up -d server
```

Edit the mock's emitted chunks to match the upstream shape you're reproducing. Provider→env-var names live in `internal/providers/provider.go`; base-URL overrides are read in `cmd/router/main.go` (`<PROVIDER>_BASE_URL`). There is **no** `ANTHROPIC_BASE_URL` / `CODEX_BASE_URL` override for the ChatGPT subscription backend (`chatgpt.com/backend-api/codex`) — native GPT subscription traffic always hits the real Codex backend unless you temporarily edit `internal/providers/openai/client.go` (`chatGPTCodexBaseURL`) and revert after.

**Subscription vs prepaid (important for marker / passthrough tests):**

Codex will attach a ChatGPT JWT. The local router then treats the turn as a Codex subscription:

- OpenAI-family decision → verbatim Responses passthrough (`SetPassthrough` / `SetPassthroughBadge`) to `chatgpt.com/backend-api/codex`. This is the path the routing-marker-on-Codex work exercises.
- Non-OpenAI decision → Chat Completions translation + `ResponsesWriter.SetBadgeText`.

To force the prepaid/BYOK translation path (no ChatGPT backend, uses `OPENAI_API_KEY` / other provider keys):

```bash
docker compose exec postgres psql -U router -d router -c \
  "SET search_path TO router; UPDATE model_router_installations SET subscription_routing_disabled=true WHERE external_id='__router_admin__';"
```

Revert with `subscription_routing_disabled=false` when done.

### 4. Throwaway CODEX_HOME

Do **not** point this at `~/.codex`. A unique temp dir keeps the user's prod Weave + ChatGPT login intact.

```bash
export CODEX_HOME=/tmp/weave-codex-local
rm -rf "$CODEX_HOME"
mkdir -p "$CODEX_HOME"
cp ~/.codex/auth.json "$CODEX_HOME/auth.json"   # required; see gotchas
cat > "$CODEX_HOME/config.toml" <<EOF
model_provider = "weave-local"

[model_providers.weave-local]
name = "Weave Router (local)"
base_url = "http://localhost:8080/v1"
wire_api = "responses"
requires_openai_auth = true
http_headers = { "X-Weave-Router-Key" = "rk_REPLACE_ME", "X-App" = "codex" }
EOF
```

Optional headers on the same `http_headers` table:

| Header | Purpose |
|---|---|
| `"X-Weave-User-Email" = "dev@localhost"` | Attribution in local logs |
| `"x-weave-force-model" = "z-ai/glm-5.1"` | **Preferred** pin. Put it here — do not rely on `codex -c` (see gotchas). |
| `"X-Weave-Routing-Marker" = "off"` | Suppress the in-band routing marker |

Do not set `X-Weave-Router-Strategy` unless you are deliberately pinning a strategy — omitting it uses the local deployment default (same rationale as `install/install.sh --codex --local`).

### 5. Drive it

```bash
cd <scratch-dir-with-files-to-act-on>
CODEX_HOME=/tmp/weave-codex-local \
  codex exec --skip-git-repo-check -s read-only -C "$(pwd)" \
  ' First send exactly: /force-model z-ai/glm-5.1
Then <task that requires tool use>, then stop.'
```

Notes:

- `--skip-git-repo-check` lets you run against `/tmp` or any non-git scratch dir.
- `-s read-only` (or `--dangerously-bypass-approvals-and-sandbox` in a throwaway dir) avoids the interactive approval TUI, which `codex exec` otherwise cannot answer.
- Leading space before `/force-model` is required (Codex otherwise treats it as an unknown local slash command and never sends it to the router).
- `codex exec` prints `provider: weave-local` in its session banner when the throwaway config took; if it prints `provider: weave` it is still reading `~/.codex` — `CODEX_HOME` was not exported.
- Confirm Codex actually reached the local router: `docker compose logs server --since=1m` should show a `POST /v1/responses` (and `ProxyOpenAIChatCompletion start`).

Headless pin, no slash command — bake the header into the throwaway `config.toml` (step 4), then:

```bash
CODEX_HOME=/tmp/weave-codex-local \
  codex exec --skip-git-repo-check -s read-only -C "$(pwd)" \
  "say hi and stop"
```

Do **not** use `codex exec -c 'model_providers.weave-local.http_headers."x-weave-force-model"=…'` as the pin. On 0.149.1 that override is silently dropped. After the run, `grep 'x-weave-force-model applied'` in server logs must fire; if it doesn't, Codex never sent the header.

### 6. Verify via logs

Codex ingress is `ProxyOpenAIResponses` → `ProxyOpenAIChatCompletion`. The decision + completion log per action is `ProxyOpenAIChatCompletion complete`. Strip ANSI first:

```bash
docker compose logs server --since=3m 2>&1 | sed -E 's/\x1b\[[0-9;]*m//g' \
  | grep 'ProxyOpenAIChatCompletion complete' | grep 'decision_model=z-ai/glm-5.1'
```

Useful fields: `decision_model`, `decision_provider`, `decision_reason`, `routing_marker`, `hard_pinned`, `cross_format`, `turn_type`, `proxy_err`, `upstream_status`. Also grep for `recovery nudge`, `tool-call loop`, `no-progress`. `SetBadgeText` is not logged — look at the Codex transcript / `codex` output for the `✦ **Weave Router** → <model>` (or `**Weave Router** — <model>` legacy) line.

**Confirm the model was actually served** before drawing conclusions:

```bash
docker compose logs server --since=3m 2>&1 | sed -E 's/\x1b\[[0-9;]*m//g' \
  | grep 'ProxyOpenAIChatCompletion complete' | grep -oE 'decision_model=[^ ]+' | sort | uniq -c
```

If you only see other models, the pin didn't take. Recheck: (1) `x-weave-force-model` is in `CODEX_HOME/config.toml` (not only a `-c` flag), (2) server log has `x-weave-force-model applied`, (3) leading space on in-prompt `/force-model` if you used that path. Codex's session banner `model: gpt-5.6-sol` is what *Codex requested*, not what the router served.

**Routing-marker specific checks (the usual reason to use this skill):**

- Cross-format (non-OpenAI decision, or prepaid OpenAI): first assistant text is `✦ **Weave Router** → <model> · <reason>`. Log field `routing_marker` is non-empty.
- Verbatim GPT passthrough (Codex subscription + OpenAI decision): same marker injected as a synthetic `response.output_text.delta` before upstream deltas (`SetPassthroughBadge`). Log still has `routing_marker`.
- `X-Weave-Routing-Marker: off` (and not subscription-only warning): no badge. Subscription-only depleted-credits warning still wins over the opt-out.
- Same model, second action (`prior_served_model` equals `decision_model`): `routing_marker=""`. Expected. Seed a new key (or change the first user text) to re-see a first-turn badge.
- Tool-call-only on the **translated** path: marker is a leading `output_item` (`output_index` 0) ahead of the function call — including `stream:false` JSON. Confirm in the SSE / JSON, not only in `codex exec` stdout (the TUI can bury the first-turn line under tool chatter).
- Tool-call-only on **verbatim GPT passthrough**: still no badge (see gotchas). Don't fail the check.
- Non-Codex client (`X-App` unset) on a translated decision still gets the `✦ **Weave Router** → …` text badge today; native OpenAI passthrough without `X-App: codex` stays byte-identical (`SetPassthrough` with no badge rewrite).
- For curl-level checks (no Codex CLI), `POST /v1/responses` with `X-Weave-Router-Key` + `X-App: codex` is enough. Isolate first-turn cases with a freshly seeded key.

### 7. Clean up

```bash
pkill -f mock_openai_upstream.py 2>/dev/null
rm -rf /tmp/weave-codex-local
rm -f docker-compose.override.yml
# `docker compose down` if you want to stop the stack
# do NOT touch ~/.codex
```

## Notes

- Local cluster version comes from `ROUTER_CLUSTER_VERSION` in `.env.local`; it may differ from prod, which is why `/force-model` (not the scorer) is the reliable way to hit one model.
- Native Codex models `gpt-5.6-sol`, `gpt-5.6-terra`, and `gpt-5.6-luna` use the caller's ChatGPT OAuth plan when subscription routing is on. Other OpenAI / Anthropic / Gemini / OpenAI-compatible models use WorkWeave deployment or BYOK credentials, same as Claude Code.
- To confirm a deploy contains a given router commit: the prod Cloud Run revision name maps to a monorepo commit; `git ls-tree <monorepo-commit> router-internal/router` shows the pinned router submodule SHA.
- `codex exec --ignore-user-config` is **not** a substitute for `CODEX_HOME`: it skips `$CODEX_HOME/config.toml` entirely (so you'd have no weave-local provider) while auth still uses `CODEX_HOME`. Always write a full throwaway config.
- Codex rollout transcripts live under `$CODEX_HOME/sessions/<yyyy>/<mm>/<dd>/rollout-*.jsonl`. Grep those for `Weave Router` when `codex exec` stdout is noisy.
- `docker-compose.override.yml` is gitignored. If you add a mock `*_BASE_URL` there, remember to rewrite the file back to the pubsub-only `ports: !reset []` (or delete it) before a real-provider Codex run — a leftover mock URL will serve `sk-mock` against production-looking model names.
