# Weave Router pi extension

> Bundled inside the [`@weave-os/router`](https://www.npmjs.com/package/@weave-os/router) package — not published separately. `src/` here is the source of truth; `npm run prepack` copies it into the package.

A [pi](https://pi.dev) extension that routes every request through the
[WorkWeave Router](https://github.com/weave-os/router) — a trained, per-request
LLM proxy that picks the most cost-efficient model that still solves each task.

Installed automatically by the Weave Router installer:

```bash
WEAVE_ROUTER_KEY=rk_… npx --package @weave-os/router -y -- weave-router --pi
WEAVE_ROUTER_KEY=rk_… npx --package @weave-os/router -y -- weave-router --pi --local  # local router
WEAVE_ROUTER_KEY=rk_… npx --package @weave-os/router -y -- weave-router --pi --lsp go,typescript  # + language servers for the lsp tool
```

That writes `~/.pi/agent/models.json` (the `weave` provider), adds
`npm:@weave-os/router` to `~/.pi/agent/settings.json` `packages`, and stores
the key in `~/.pi/agent/.weave_router_key`. pi auto-installs `@weave-os/router`
from npm on next start and loads this extension via its `pi.extensions` field.
Plain `pi` then connects to the installed endpoint, normally
`https://router.workweave.ai`; it does not start a local Python proxy.

## What it does

- **Loom experience on stock pi.** Replaces pi's startup header through the
  public extension API and keeps pi's own runtime/footer intact. Wooly is hidden
  by default; `/wooly` toggles the orange terminal animation for the current
  session. Wooly is visual only: there is no
  dialogue box, narration, coaching request, or separate Loom runtime.
- **Automatic model selection.** All pi traffic flows through the router, which
  selects the model per request. You don't pick a model — the router does.
- **Force-model commands.** `/fm <model>` and `/force-model <model>` pin the
  current router session; `/ufm` and `/unforce-model` resume automatic routing.
  The persistent status changes to `WEAVE ROUTER — <model> [forced]` after the
  router validates and canonicalizes the requested model.
- **Beta routing toggle.** `/beta` toggles the router's beta HMM strategy for
  the current session. The router confirms whether beta routing is enabled or
  disabled; run `/beta` again to switch back.
- **Per-process routing bias.** Static `x-weave-routing-*` knob headers bias the
  router: quality on the main loop and speed + cheap on subagents.
- **Long tool-loop compaction.** Pi can cross its context threshold inside an
  uninterrupted tool loop before its normal post-run compaction check. The
  extension preserves a usable output budget for the real continuation,
  compacts once the loop settles, and resumes that extension-owned tool loop.
  Ordinary Pi threshold compaction remains under Pi's control.
- **Sticky sessions.** `metadata.user_id = "pi:<sessionId>"` pins the main loop
  to one model for the session; subagents get their own pins.
- **`dispatch` tool — parallel, context-isolated subagents.** pi has none
  natively. `dispatch` spawns child `pi` processes (read-only by default), runs
  them concurrently, and returns only each subagent's final answer — intermediate
  tool output stays in the child, so the main context stays small.
- **`lsp` tool — code intelligence through a language server.** definition,
  references, hover, documentSymbol, and diagnostics for Go, TypeScript/
  JavaScript, Python, and Rust (gopls, typescript-language-server, pyright,
  rust-analyzer). Servers are spawned lazily per workspace root, shut down
  after idling, and shared with dispatch subagents through a parent-side local
  socket — a fan-out reuses the parent's warm servers instead of cold-starting
  its own. A bundled `lsp-guide` skill (`/skill:lsp-guide`) carries the usage
  recipes (per-operation cookbook, the locate-then-references flow, caveats).
  Disable with `WEAVE_PI_NO_LSP=1`.
- **Opt-in language-server install — nothing installs silently.** Two ways in:
  pass `--lsp go,typescript` to the installer, or let the assistant offer.
  When the workspace contains a language whose server is missing, the
  assistant offers once in conversation ("I can enable go LSP support if
  you'd like — just say the word!"); a yes runs the install through the
  `lsp_enable` tool (main process only — subagents can't trigger installs), a
  "don't ask again" is persisted per language in `<agentDir>/.weave_lsp.json`
  and respected across sessions. Installs need that language's toolchain
  (`go`, `npm`, or `rustup`) on PATH; when the toolchain itself is missing,
  the bundled `install-lsps` skill (`/skill:install-lsps`) walks the agent
  through installing it — again only with explicit consent.
- **Persistent route + savings display.** Shows
  `WEAVE ROUTER — <routed> ← <selected> · saved $X.XX` below pi's native footer
  data. Savings compare the selected and routed catalog prices against the same
  input/output/cache usage, accumulate across the reachable session branch,
  and survive resume. Unknown catalog prices are labeled `unpriced` instead of
  silently contributing zero; costlier routing is labeled `extra`, not savings.
- **No duplicate in-band badge.** Sets `X-Weave-Routing-Marker: off` because the
  persistent status already conveys the actual model.
- **Safety backstop.** Blocks a few catastrophic shell commands (`rm -rf /`,
  `mkfs`, `dd of=/dev/…`, fork bombs, force-push to main). Disable with
  `WEAVE_NO_SAFETY=1`.

## Configuration (environment)

| Variable | Default | Purpose |
|---|---|---|
| `WEAVE_ROUTER_URL` | installed `models.json` endpoint, normally hosted | Override router base URL (children inherit it); an unconfigured extension falls back to `http://localhost:8080` |
| `WEAVE_ROUTER_KEY` | — | Router key (else read from `.weave_router_key`) |
| `WEAVE_ROUTER_KEY_FILE` | `<agentDir>/.weave_router_key` | Override key file path |
| `WEAVE_USER_EMAIL` / `WEAVE_USER_NAME` | from `git config` | Identity headers for attribution |
| `WEAVE_PI_SUBAGENT_MODEL` | `claude-sonnet-4-6` | `weave/<model>` handle children launch with (router re-routes) |
| `WEAVE_PI_DISPATCH_CONCURRENCY` | `4` | Max concurrent subagents |
| `WEAVE_PI_SUBAGENT_TIMEOUT_MS` | `600000` | Per-subagent timeout |
| `WEAVE_PI_ALLOW_SUBAGENT_TOOLS` | unset | `1` lets `dispatch` grant subagents write/exec tools (bash, write, edit); default strips them |
| `WEAVE_ROUTING_ALPHA` / `…_SPEED_WEIGHT` / `…_OUTPUT_COST_RATIO` / `…_EXPECTED_OUTPUT_TOKENS` | role preset | Override individual routing knobs (main process only — children always use their role preset) |
| `WEAVE_NO_SAFETY` | unset | `1` disables the catastrophic-bash gate |
| `WEAVE_PI_AUTO_COMPACTION` | unset | `0` disables the routed tool-loop compaction safeguard |
| `WEAVE_PI_ESCALATION_COMPACTION` | enabled | `0` disables preparation and compaction before upward complexity model changes; requires Pi 0.83+ and the matching router deployment |
| `WEAVE_PI_NO_LSP` | unset | `1` disables the `lsp` tool, the server pool, and the subagent broker |
| `WEAVE_PI_LSP_IDLE_MS` | `300000` | Idle window before an unused language server is shut down |
| `WEAVE_PI_LSP_REQUEST_TIMEOUT_MS` | `15000` | Per-request budget once a server is warm |
| `WEAVE_PI_LSP_WARMUP_TIMEOUT_MS` | `60000` | Budget for initialize + the first query (indexing) |
| `WEAVE_PI_LSP_DIAGNOSTICS_WAIT_MS` | `3000` | How long `diagnostics` waits for a fresh publish |
| `WEAVE_PI_LSP_MAX_SERVERS` | `4` | LRU cap on concurrently live language servers |
| `WEAVE_PI_LSP_MAX_REFERENCES` | `100` | Cap on reported reference sites |

Internal: `WEAVE_PI_SUBAGENT=1`, `WEAVE_PI_SUBAGENT_ID`, `WEAVE_PI_LSP_BROKER`,
and `WEAVE_PI_LSP_BROKER_TOKEN` are set by `dispatch` on child processes; don't
set them yourself.

## Escalation compaction

Escalation compaction is enabled by default on Pi 0.83 or newer. Older Pi
versions warn at startup and continue routing without escalation compaction.
The router requires the same
`ROUTER_PI_HANDOFF_SECRET` (at least 32 bytes) on every replica. Set
`WEAVE_PI_ESCALATION_COMPACTION=0` to opt out. An enabled client stops
with a visible error if preparation is unavailable; it never falls back to
silently sending the full unreserved request.

`POST /v1/route/handoff` uses the authenticated Messages middleware and serving
route selection, including session pins and confidence guards. It stops before
provider dispatch and returns a signed continuation ticket valid for ten
minutes. Preparation can update routing pins; it is not a side-effect-free
preview. The ticket is bound to the key, installation, session, strategy,
requested model, tools and reasoning configuration. Dispatch rechecks current
model eligibility and explicit force state. Expired or stale tickets return
409; a new user turn prepares again. Model-selection tickets remain in memory
and are never saved in the Pi transcript.

Preparation also returns a signed thread-continuity token (`session_token`).
Pi saves it as a session entry and sends it as `weave_session` on preparation,
summary and continuation requests. It preserves the original first-message
digest across compaction and process restarts, and binds it to the API key,
installation, client session and metadata identity. A `pi:` prefix alone never
bypasses the first-message discriminator. This token has no model selection or
credentials and lasts for the conversation; signing-secret rotation invalidates
it. Every request still requires normal authentication. Deploy the router before
using the updated extension, which requires this field in preparation responses.

The main Pi session compacts when the observed class increases through
`low → medium → high → maximum`, the served model changes, and estimated context
is at least 32,768 tokens. Unknown classes and the initial turn establish a
baseline. Utility turns, forced models, subagents, subscription bypass, native
server tools and non-authoritative routing retain their existing behavior.

A separate ticket directs summarization to the previously served model without
repinning the session. The summary request preserves the original system,
tools, thinking settings and message prefix, appending a checkpoint instruction
so existing provider caches can be reused. Pi owns the compaction boundary and
retained recent messages; the original transcript remains on disk. Its normal
summary generation can use separate summaries for a split turn. Compaction
failure stops the handoff and leaves the transcript available. New user input,
session navigation or model selection cancels a pending continuation.

The continuation carries the selected model ticket, so the shortened history
is not reclassified. Summarization still costs tokens and adds latency; actual
cache hits and net savings require a provider-backed measurement. The client
savings display does not yet include these auxiliary summary calls.

Offline validation:

```bash
bash install/pi-router/test/e2e.sh
python3 install/pi-router/test/handoff-e2e.py
go test ./internal/proxy ./internal/api/anthropic ./internal/server
```

Development dependencies alias the legacy Pi module names to Pi 0.83. The
TypeScript `pi-ai` path targets its compatibility entrypoint, matching Pi's
extension loader; this preserves the runtime's shared provider registry.

## Billing

Routing through the router switches pi from Claude **subscription OAuth** to
**per-token** billing on the router deployment's key (or your BYOK key). BYOK
skips cross-provider failover; deployment-key billing is the default.

The displayed savings are a client-side estimate from the router's generated
model-price catalog. Cache writes use 1.25× input price and cache reads use 0.1×
input price, matching the Claude Code statusline. The ledger stores its catalog
version with each response so resumed totals remain auditable.

## Notes

- Actual SDK/router probes keep their small output budget. Only a probe-sized
  request carrying a real tool-result continuation is repaired.
