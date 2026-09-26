# Legacy opencode Weave integration

This source is retained only for historical compatibility and is no longer
bundled or installed. Subscription enrollment now happens in the router with
`npx @weave-os/router login claude` or `npx @weave-os/router login codex`;
requests must not carry subscription headers.

The source is retained for lifecycle/directive compatibility tests only. It is
not loaded by new installs.

The installer writes only the Responses-format `weave` provider into
`opencode.json`.

## Why this source remains

Older manually-installed copies may still load this module for lifecycle and
directive hooks. It intentionally contains no OAuth login, token refresh, or
subscription-header transport; new installs use only the provider config.

## Wire shape it produces

opencode talks to one Responses-format `weave` provider. Subscription enrollment
is managed by the router (`npx @weave-os/router login claude`), not by request
headers from this plugin:

| | |
|---|---|
| `POST {router}/v1/responses` | Responses wire format (opencode's default for an `@ai-sdk/openai` provider) |
| `X-Weave-Router-Key: rk_…` | from `opencode.json` `options.headers` — the router authenticates off this |

The router routes each request across the models available to the managed
installation and its configured credentials.

## Router directives

OpenCode is a plugin, not a Codex skill runner. The plugin's `chat.message` hook
rewrites `$rf` / `$router-feedback`, `$fm` / `$force-model`, `$ufm` /
`$unforce-model`, and `$router-session` (slash forms too) into the leading-space
`/…` prompts the router already parses. Local toggles (`router-on` / `router-off`
/ `router-status` / `disable-routing`) stay CLI-only — this installer does not
own a per-session config flip the way Claude Code and Codex do.

## Enrollment

Enroll subscriptions through the managed router rather than OpenCode:

```sh
npx @weave-os/router login claude  # or: npx @weave-os/router login codex
```

The router stores and refreshes the enrollment server-side; OpenCode does not
need to carry subscription credentials in request headers.

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
| Bun integration tests | Router/lifecycle header continuity, directive rewriting, and no subscription/account header injection |
| Pinned Responses SDK fixtures | Streaming and non-streaming text/usage, two tool calls, upstream errors, malformed/unknown events, truncated streams, absent usage |
| Installed real CLI | Exact `/v1/responses`, `auto`, `X-App`, router key, nonempty session IDs, parsed text, usage, completion, two executed tools and matching tool results |
| Real lifecycle hooks | Main `build`, title `title`, task child `explore`, and automatic `compaction` followed by resumed `build`; parent/child session boundaries |
| Real plugin loader | Routing hooks remain loadable when explicitly installed as a legacy plugin |
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

The source intentionally keeps the
[legacy all-export loader](https://github.com/anomalyco/opencode/blob/v1.18.27/packages/opencode/src/plugin/index.ts)
shape for compatibility with older manually-installed copies. New installs do
not load this source.
See the current [CLI](https://opencode.ai/docs/cli/),
[plugin](https://opencode.ai/docs/plugins/), and
[server](https://opencode.ai/docs/server/) references when updating the pin.
