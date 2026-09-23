# opencode Weave subscription plugin

Lets a caller's own **AI subscriptions** pay for their **opencode** turns, routed
through the Weave Router. A subscription is a **credential scoped to the model
family it can pay for**, not a provider you pick: you connect your ChatGPT
(Codex) and/or Claude (Pro/Max) plan once, the router routes every request to the
best model, and bills the plan that matches the model it served — ChatGPT pays
for GPT/Codex turns, Claude pays for Claude turns, your Weave key pays for
everything else.

Bundled into `@weave-os/router`; the installer (`--opencode`) drops
`src/index.ts` into the user's opencode plugins dir and writes a single
Responses-format `weave` provider (plus a login-only `weave-claude` provider)
into `opencode.json`.

## Why a plugin (config alone can't do it)

opencode removed built-in subscription auth in 1.3.0 and binds OAuth to its own
first-party providers, so a custom router provider can't reuse it. And
subscription tokens expire hourly — a static `options.headers` string can't
refresh them, nor carry two subscriptions whose tokens rotate independently.

## Wire shape it produces

opencode talks to one Responses-format `weave` provider; the plugin's loader
attaches whichever subscriptions are connected to **every** request via the
router's dedicated headers:

| | |
|---|---|
| `POST {router}/v1/responses` | Responses wire format (opencode's default for an `@ai-sdk/openai` provider) |
| `X-Weave-OpenAI-Subscription: <ChatGPT JWT>` | pays GPT/Codex turns, refreshed on expiry |
| `X-Weave-OpenAI-Account-ID: <id>` | paired account id (required by the Codex backend) |
| `X-Weave-Anthropic-Subscription: <sk-ant-oat token>` | pays Claude turns, refreshed on expiry |
| `X-Weave-Router-Key: rk_…` | from `opencode.json` `options.headers` — the router authenticates off this |

The router routes each request across every model the caller's subs + key can pay
for and resolves the subscription matching the chosen provider, so a sub is
never billed for a request outside its family.

## Two storage slots, one request provider

opencode stores one credential per provider id and the loader's `getAuth()` is
scoped to its own provider, so the two logins live in two slots:

- **`weave`** — the request provider. Owns the **ChatGPT** login and the loader
  that attaches both subscriptions. Connecting ChatGPT activates sub-routing.
- **`weave-claude`** — login-only (no models, serves no requests). Owns the
  **Claude** login. Its token is read from opencode's on-disk auth store by the
  `weave` loader (the SDK exposes no get-by-id).

With neither connected, `weave` is a plain router provider and the Weave key
pays. Either login activates its matching subscription independently, so a
Claude-only setup pays Claude turns from that plan without requiring ChatGPT.

## Router directives

OpenCode is a plugin, not a Codex skill runner. The plugin's `chat.message` hook
rewrites `$rf` / `$router-feedback`, `$fm` / `$force-model`, `$ufm` /
`$unforce-model`, and `$router-session` (slash forms too) into the leading-space
`/…` prompts the router already parses. Local toggles (`router-on` / `router-off`
/ `router-status` / `disable-routing`) stay CLI-only — this installer does not
own a per-session config flip the way Claude Code and Codex do.

## Login

`opencode auth login` → **Weave Router — Codex plan** → *ChatGPT Pro/Plus*
(browser or headless device code) and/or **Weave Router — Claude plan** →
*Claude Pro/Max* (browser; paste the `code#state` shown after authorizing).

These logins populate OpenCode's provider-keyed `auth.json`, which is how this
plugin reads and forwards the subscription credentials. The standalone
`npx @weave-os/router login codex|claude` commands enroll server-side
subscription accounts instead; they do not populate OpenCode's auth store and
are not a replacement for these plugin logins.

## Env overrides (self-host + tests)

- `WEAVE_OPENCODE_LLM_CLASSIFIER=1` — enroll only sessions created while this
  plugin is running. The plugin stores a separate handshake UUID and router
  ticket for each parent/child session under OpenCode's data directory. An
  enrollment failure or missing new-session event blocks inference while the
  opt-in is set; resume an older session with the opt-in unset. Classifier
  sessions cannot compact or change router origins; start
  a new session instead. Requires a router deployment with the exact release
  and installation explicitly allowlisted. Deploy the router's non-retryable
  client-failure response before enabling this opt-in on developer machines.
- `WEAVE_CODEX_OAUTH_ISSUER` — OpenAI auth issuer.
- `WEAVE_ANTHROPIC_OAUTH_AUTHORIZE` / `WEAVE_ANTHROPIC_OAUTH_TOKEN` — Anthropic
  OAuth authorize host / token endpoint.
- `WEAVE_OPENCODE_AUTH_FILE` — path to opencode's `auth.json` (the `weave`
  loader reads the `weave-claude` slot from here; defaults to
  `$XDG_DATA_HOME/opencode/auth.json`).

## Verification

The required CI contract pins **OpenCode and `@opencode-ai/plugin` 1.18.27**.
The CLI install reads that pin from `package.json`; the direct Responses tests
use **`@ai-sdk/openai` 3.0.84**, matching the
[pinned OpenCode dependencies](https://github.com/anomalyco/opencode/blob/v1.18.27/packages/opencode/package.json).
Neither required dependency floats. The separate weekly/manual
[latest compatibility workflow](../../.github/workflows/opencode_latest.yml)
is not a merge gate.

From the router repository root (Node, Bun, Python 3.11+, and jq required):

```bash
npm ci --prefix install/opencode-weave --ignore-scripts
npm run --prefix install/opencode-weave typecheck
bun test install/opencode-weave/test/
make test-install
npm install --global opencode-ai@1.18.27
bash install/pi-router/test/opencode_smoke.sh
go test ./internal/proxy ./internal/translate -count=1
```

`OPENCODE_BIN` selects an already-installed CLI. To explicitly test another
version, set `OPENCODE_EXPECTED_VERSION` to its exact version; the smoke otherwise
rejects anything other than the supported pin. The driver gives each command
60 seconds, kills its process group on timeout, limits mock requests per session,
and isolates HOME, XDG directories, provider selection, and credentials. Package
bootstrap may use the network; inference and OAuth tests never use real credentials
or real providers.

| Layer | Contract |
|---|---|
| Bun plugin tests | All four subscription combinations; expiry, refresh failure isolation, late login; header/session continuity; no secrets logged |
| Pinned Responses SDK fixtures | Streaming and non-streaming text/usage, two tool calls, upstream errors, malformed/unknown events, truncated streams, absent usage |
| Installed real CLI | Exact `/v1/responses`, `auto`, `X-App`, router key, nonempty session IDs, parsed text, usage, completion, two executed tools and matching tool results |
| Real lifecycle hooks | Main `build`, title `title`, task child `explore`, and automatic `compaction` followed by resumed `build`; parent/child session boundaries |
| Real plugin loader | Both providers' OAuth methods visible through `opencode serve` → `/provider/auth`, even without an OAuth store |
| Installer/package | Install/toggle/uninstall, packed npm `--opencode` entrypoint; existing Claude/Codex/Pi installer coverage |
| Go proxy/translation | Native and translated Responses history hygiene, actionable tool-output commands, Codex feedback-skill preservation, wire lifecycle and usage |

The lifecycle observer is a **test-only** `chat.headers` hook recording OpenCode's
actual `input.agent`, not a prompt heuristic or a router-policy change. A synthetic
high-usage response triggers automatic compaction without a huge prompt. Title
generation uses the configured local `small_model`; task execution uses the actual
`task` tool and a child session. No separate classifier/probe invocation was
observed in these headless flows. Tool-less SDK fixtures cover that wire shape
without claiming a classifier fingerprint. Non-streaming requests are direct SDK
fixtures: the tested CLI flows use streaming.

The pinned SDK silently ignores an ill-shaped known event, treats an unfinished
stream as `other`, and tolerates unknown event types. It also requires usage on a
streaming `response.completed`: missing usage produces `other` rather than `stop`
(and can cause CLI continuation), while non-streaming missing usage remains
unknown token counts. These are explicit compatibility observations, **not** claims
of clean failure or successful completion. The normal CLI cases require both a
successful terminal event and exact usage, so removing completion/usage fails them.

The plugin intentionally uses the
[legacy all-export loader](https://github.com/anomalyco/opencode/blob/v1.18.27/packages/opencode/src/plugin/index.ts):
all exported values are plugin functions, `default === WeaveCodex` is deduplicated,
and `WeaveClaude` registers separately. Converting the default to a V1 plugin
object would bypass those named exports; the loader tests guard against that.
See the current [CLI](https://opencode.ai/docs/cli/),
[plugin](https://opencode.ai/docs/plugins/), and
[server](https://opencode.ai/docs/server/) references when updating the pin.
