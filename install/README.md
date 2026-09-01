# Weave Router — Claude Code + Codex + opencode installer

One command to point Claude Code, the OpenAI Codex CLI, or opencode at the
Weave Router permanently. No shell exports, no manual config edits.

## Quick start

### Hosted Weave Router

```bash
# Interactive: the installer asks Claude Code / Codex / opencode, then user vs. project
npx @workweave/router

# Skip the target picker:
npx @workweave/router --claude                     # Claude Code, user scope
npx @workweave/router --codex                      # Codex, user scope
npx @workweave/router --opencode                   # opencode, user scope

# Project scope — only when running inside this repo:
npx @workweave/router --claude   --scope project   # Claude Code
npx @workweave/router --codex    --scope project   # Codex
npx @workweave/router --opencode --scope project   # opencode
```

On npm ≤ 6 the bundled `npx` mis-parses a leading `-y` (it consumes the next
token, dropping the package name), so name the binary explicitly there — or
just upgrade with `npm i -g npm@latest`:

```bash
npx --package @workweave/router -y -- weave-router --claude
```

Or from a clone of this repo:

```bash
./router/install/install.sh                    # prompts: target, then scope
./router/install/install.sh --claude           # skip picker, Claude Code
./router/install/install.sh --codex            # skip picker, Codex
./router/install/install.sh --opencode         # skip picker, opencode
./router/install/install.sh --scope project    # team install
```

When run interactively without `--claude` / `--codex` / `--opencode`, the
installer asks which tool to target (defaults to Claude Code on Enter).
Without `--scope`, it then asks user vs. project (defaults to user).
`--non-interactive` skips both prompts (target defaults to Claude Code) —
useful for CI and `curl | sh` pipelines.

The installer also prompts for your API key (or reads `$WEAVE_ROUTER_KEY`
for non-interactive installs). Re-running it reuses the key already on disk,
so you only ever paste it once — see [Staying up to date](#staying-up-to-date).

### Self-hosted via `docker compose` (zero-config)

If you're running the router locally with the bundled `docker-compose.yml`
(`localhost:8080`), use the shortcut:

```bash
cd router
make full-setup                 # boot the stack and seed a router key
make install-cc                 # → ./install/install.sh --claude --local
claude                          # routes through your local router
```

`make install-cc` is a wrapper around `./install/install.sh --claude --local`,
which is shorthand for `--base-url http://localhost:8080`. For Codex, swap
the target flag:

```bash
./router/install/install.sh --codex --local                    # user scope Codex
./router/install/install.sh --codex --local --scope project    # team scope Codex
```

No Codex install forces a strategy header; every install keeps the target
router's configured default, so a deployment-default change reaches clients
that were installed earlier. This also matters for self-hosted routers,
including the bundled local stack, which may not run the optional HMM sidecar.
A deployment that wants a specific policy can add `X-Weave-Router-Strategy`
explicitly to its managed Codex config.

### Self-hosted on a custom URL

```bash
# Internal deploy with seeded keys (will prompt for the bearer):
./router/install/install.sh --base-url https://router.your-company.internal

# Custom local port, dev mode:
./router/install/install.sh --base-url http://localhost:9000 --dev-mode
```

## What gets written

### Claude Code (`--claude`, default)

**User scope:**

| Path                                  | Purpose                                                       |
| ------------------------------------- | ------------------------------------------------------------- |
| `~/.claude/settings.json`             | Sets `env.ANTHROPIC_BASE_URL`, `env.ANTHROPIC_CUSTOM_HEADERS` with `X-Weave-Router-Key`, `env.ENABLE_TOOL_SEARCH=auto` (a custom base URL otherwise disables Claude Code's MCP tool-search deferral), `statusLine`, and Claude Code `attribution` so commits/PRs credit Weave Router. Other keys preserved. |
| `~/.weave/cc-statusline.sh`           | The status line script. Reads the router's decisions log + the CC transcript to show routed-model + savings. |

**Project scope (`--scope project`):**

| Path                                | Committed? | Purpose                                                       |
| ----------------------------------- | ---------- | ------------------------------------------------------------- |
| `<repo>/.claude/settings.json`      | ✅ commit  | Sets `env.ANTHROPIC_BASE_URL`, `statusLine` (relative paths), and Claude Code `attribution` so commits/PRs credit Weave Router. **No token.** |
| `<repo>/.gitignore`                 | ✅ commit  | Adds the four `.claude/` paths below to the ignore list.       |
| `<repo>/.claude/cc-statusline.sh`   | ❌ ignored | Status line script — runs on every CC session.                 |
| `<repo>/.claude/settings.local.json`| ❌ ignored | Stores your local `ANTHROPIC_CUSTOM_HEADERS` router-key header and any other per-teammate overrides. |
| `<repo>/.claude/.credentials.json`  | ❌ ignored | CC's per-user credentials cache.                               |

The router key lives in `ANTHROPIC_CUSTOM_HEADERS` so Claude Code can keep
using its normal Anthropic auth (`Authorization` / `x-api-key`) for the
logged-in user's Team/Pro/Max/individual plan.

### Codex (`--codex`)

**User scope:**

| Path                       | Purpose                                                       |
| -------------------------- | ------------------------------------------------------------- |
| `~/.codex/config.toml`     | Adds a managed `[model_providers.weave]` block + sets top-level `model_provider = "weave"`, both between `# >>> weave-router managed` markers. The provider preserves the existing ChatGPT OAuth login and keeps the target router's default routing strategy. Anything outside the markers is preserved. |
| `~/.weave/codex-status.sh` | Codex `SessionStart`/`Stop` hook helper. Keeps the latest routed model in the terminal title without adding status messages to the conversation. |

The status helper is installed with mode `0700`, stores only the session's requested and routed model IDs under `${XDG_CACHE_HOME:-~/.cache}/weave-router/codex/`, and never stores prompts, credentials, or response bodies. Existing Codex hooks are preserved and the managed hooks are safe to reinstall or remove.

**Project scope (`--scope project`):**

| Path                             | Committed? | Purpose                                                       |
| -------------------------------- | ---------- | ------------------------------------------------------------- |
| `<repo>/.codex/config.toml`      | ❌ ignored | Per-teammate config (holds the router key). Each teammate runs the installer for their own key. |
| `<repo>/.codex/weave-status.sh`  | ❌ ignored | Per-teammate Codex lifecycle helper used by the managed status hooks. |
| `<repo>/.codex/.weave-router-disabled` | ❌ ignored | Local off-state marker used by the helper. |
| `<repo>/.gitignore`              | ✅ commit  | Adds the Codex config, status helper, and off-state marker to the ignore list. |

Run Codex from the repo with `CODEX_HOME=<repo>/.codex codex` so it picks
up the project-local config instead of `~/.codex/`.

Re-running the installer rewrites only the managed block (TOML between the
markers + a top-level `model_provider =` outside it). Everything else —
profiles, alternate providers, comments — stays untouched.

Routing is model-aware after HMM or force-model selection. The native Codex
models `gpt-5.6-sol`, `gpt-5.6-terra`, and `gpt-5.6-luna` use the caller's
ChatGPT OAuth plan. Other OpenAI, Anthropic, Gemini, and OpenAI-compatible
models use their matching WorkWeave deployment or BYOK credentials, just as
they do when routed from Claude Code.

Codex does not load third-party Markdown slash commands, and reserves `/…` for
its own built-ins. The installer therefore ships each router directive as a
native Codex skill, invoked with `$`:

| Skill | Sends |
| --- | --- |
| `$force-model <model-id>` / `$fm <model-id>` | ` /force-model <model-id>` |
| `$unforce-model` / `$ufm` | ` /unforce-model` |
| `$router-feedback <text>` / `$rf <text>` | ` /router-feedback <text>` |

Each skill submits a normal prompt whose first character is one literal space,
which is what reaches the router's directive parser. You can always type that
form yourself instead:

```text
 /force-model gpt-5.6-terra
 /unforce-model
 /rf - the previous response was too slow --label=high
```

To return to regular Codex, invoke the installer-provided Codex skill as
`$disable-routing`. It runs the safe local off toggle, preserves the router
configuration and ChatGPT OAuth, and takes effect when you start the next
`codex` session. A literal `/disable-routing` is not possible because Codex
reserves slash commands for its built-ins. The shell equivalent is:

```bash
npx --package @workweave/router -y -- weave-router disable-routing
```

### opencode (`--opencode`)

**User scope:**

| Path                                       | Purpose                                                       |
| ------------------------------------------ | ------------------------------------------------------------- |
| `~/.config/opencode/opencode.json`         | Merges a `provider.weave` entry backed by opencode's `@ai-sdk/openai`, pointed at `<base-url>/v1`. Headers carry `X-Weave-Router-Key` plus the identity headers (`X-Weave-User-Email`, `X-Weave-User-Name`, `X-App: opencode`). The single `weave/auto` choice delegates upstream-model selection to Weave Router. |

**Project scope (`--scope project`):**

| Path                       | Committed? | Purpose                                                       |
| -------------------------- | ---------- | ------------------------------------------------------------- |
| `<repo>/opencode.json`     | ❌ ignored | Per-teammate config (holds the router key). Each teammate runs the installer for their own key. |
| `<repo>/.gitignore`        | ✅ commit  | Adds `opencode.json` to the ignore list.                       |

OpenCode sends Responses requests through its bundled `@ai-sdk/openai`
provider, while Weave Router selects and translates to the upstream model.
Re-running the installer rewrites only the managed `provider.weave` block and
migrates legacy `weave/*` choices to `weave/auto`; other providers, MCP
servers, agents, and unrelated top-level model choices stay untouched.
`--uninstall --opencode` strips the block (and `model` only when it points at
`weave/...`).

**Onboarding flow for a new teammate (any target):**

```bash
git clone <repo>
cd <repo>
npx @workweave/router --claude --scope project   # or --codex / --opencode
export WEAVE_ROUTER_KEY=rk_...                    # in shell rc / dotenv / 1Password
claude                                             # or `CODEX_HOME=.codex codex` / `opencode`
```

The `--scope project` step only needs to run once per checkout (re-run if
`cc-statusline.sh` is updated upstream; the re-run reuses your installed key,
so no key paste is needed).

## Flags

| Flag                       | Default                       | Meaning                                                                |
| -------------------------- | ----------------------------- | ---------------------------------------------------------------------- |
| `--claude`                 | (target picker if interactive) | Skip the target picker; install for Claude Code.                       |
| `--codex`                  | (target picker if interactive) | Skip the target picker; install for the OpenAI Codex CLI.              |
| `--opencode`               | (target picker if interactive) | Skip the target picker; install for opencode.                          |
| `--scope user\|project`    | interactive prompt (default `user`) | User-level install (everywhere) vs project-level (this repo only).      |
| `--local`                  | off                           | Shortcut for the bundled docker-compose router (`localhost:8080`).      |
| `--base-url <url>`         | `https://router.workweave.ai` | Override the router endpoint. Use for self-hosted / custom port.        |
| `--non-interactive`        | off                           | Fail if `$WEAVE_ROUTER_KEY` isn't set instead of prompting. Defaults target to Claude Code so existing CI pipelines don't shift semantics. |
| `--rotate-key`             | off                           | Ignore the key already installed and prompt for a new one (or take `$WEAVE_ROUTER_KEY`). Use when rotating a key. |

Override the default base URL globally by setting `$WEAVE_ROUTER_URL` before
running the installer.

## Staying up to date

The installer is re-run periodically because the pieces it writes do change —
statusline features and pricing, slash commands, config shape. Two things make
that painless.

**Your key is remembered.** Key resolution order is `$WEAVE_ROUTER_KEY` →
the key already installed for this target and scope → interactive prompt. So a
plain re-run needs no key at all:

```bash
npx @workweave/router --claude                  # reuses the installed key
npx @workweave/router --claude --rotate-key     # ignore it, prompt for a new one
```

If the installed key turns out to be revoked, an interactive run says so and
asks once for a replacement rather than leaving a broken install behind.

**`update` is the scriptable form.** It never prompts, resolves the key from
env or disk only, refreshes the managed config and assets in place, and errors
(rather than asking) if no key can be found — safe for cron:

```bash
npx @workweave/router update --claude                    # user scope
npx @workweave/router update --claude --scope project    # in the repo
npx @workweave/router update --codex                     # Codex / opencode / pi too
```

A rejected key is an error for `update` (exit 1), not a warning, so a scheduled
run surfaces a revoked key instead of logging past it. `update` works for every
target; a plain re-run of the installer reuses your installed key the same way.

**Claude Code also refreshes itself.** `cc-statusline.sh` checks
`raw.githubusercontent.com` for a newer copy of itself at most once every
`$WEAVE_STATUSLINE_UPDATE_INTERVAL_DAYS` (default 7) in a detached background
fork, and on the same schedule refreshes the `.claude/commands/*.md` slash-command
wrappers. Both swap only on a real content change, and a wrapper is replaced
only when its bytes still match the last canonical copy — a wrapper you edited
is never overwritten, and one you deleted is never resurrected. All state
(stamps, baselines) lives under `${XDG_CACHE_HOME:-~/.cache}/weave-router/`, so
nothing lands in a repo working tree.

| Environment variable                       | Default | Effect                                                        |
| ------------------------------------------ | ------- | ------------------------------------------------------------- |
| `WEAVE_STATUSLINE_UPDATE=0`                 | on      | Disable every background network path in the statusline.       |
| `WEAVE_COMMANDS_UPDATE=0`                   | on      | Disable only the slash-command refresh.                        |
| `WEAVE_STATUSLINE_UPDATE_INTERVAL_DAYS`     | `7`     | How often either check may run.                                |
| `WEAVE_STATUSLINE_URL`                      | GitHub raw | Source for the statusline (self-hosters who fork).          |
| `WEAVE_COMMANDS_URL_BASE`                   | GitHub raw | Source directory for the slash-command wrappers.            |
| `WEAVE_CODEX_STATUS_UPDATE=0`               | on      | Disable the Codex status helper's self-refresh.                |
| `WEAVE_CODEX_STATUS_UPDATE_INTERVAL_DAYS`   | `7`     | How often the Codex helper may check for a new copy.           |
| `WEAVE_CODEX_STATUS_URL`                    | GitHub raw | Source for the Codex status helper (self-hosters who fork). |

**Codex refreshes itself too.** The status helper checks for a newer copy of
itself on the same schedule and swaps it in atomically, so an install picks up
router fixes without waiting for someone to remember to re-run the installer.
The check runs only on a real lifecycle hook — never on the `off`/`on`/`status`
toggles — in a detached fork, so no Codex turn blocks on it, and it skips the
replacement when the bytes are unchanged. The `WEAVE_STATUSLINE_*` variables
above are accepted as fallbacks for users who configure both clients together.

**Codex status integration.** Codex 0.150+ supports lifecycle hooks. The installer enables hooks and adds managed `SessionStart` and `Stop` handlers. They maintain a small local state file and set the terminal title to `Weave Router · <routed-model> ← <requested-model>` when the router provides a routed-model marker. On ordinary turns where the model is unchanged, the title remains the last known routed model; before the first routed response it shows `Weave Router · active`. The hooks intentionally emit no status messages: Codex renders hook output in the conversation, which makes a persistent router indicator noisy and easy to confuse with model output. It is not a replacement for Codex's requested-model line: that line continues to show the model selected in Codex configuration, while the Weave status identifies the model that actually served. Existing user and project hooks remain outside the managed block and are preserved on reinstall/uninstall.

**Session savings.** The title also carries `· saved $X.XX` when the router has beaten the model Codex asked for. The number comes from the router — the hook reads `GET <base-url>/v1/sessions/<session-id>/cost` with the router key already in `config.toml` — and is never computed locally: Codex records only its *requested* model on every turn, never the one that served, so client-side pricing would compare a model against itself and always report zero. The fetch is detached and its result is cached for the following turn, so no turn ever blocks on the network; a slow, unreachable, or older router simply leaves the title model-only. A session where the router spent more than the requested model would have shows no clause at all rather than a negative number, and a total under a cent reads `saved <$0.01`. Set `WEAVE_CODEX_STATUS_SAVINGS=0` to turn the lookup off entirely.

The helper requires `jq` for per-turn updates. If `jq` is unavailable, the install still succeeds and the initial active terminal title remains available; no model metadata is updated by the hook. Disable or remove the integration with the normal Codex off/uninstall commands.

## Adding or changing a directive

`install/directives.tsv` is the single registry. Every install path, the
uninstaller, the statusline refresh, and the npm packaging step read it, so a
directive is declared once and reaches every client that supports it.

```
canonical|aliases|capability|claude|codex|opencode|pi|cursor|adapter
```

- `capability` is `prompt` (the directive is text the router parses) or
  `local-toggle` (it shells out to this installer to change local config).
  Only declare a `local-toggle` for a client whose config this installer owns —
  do not invent one where the client has no equivalent.
- The client columns are `yes`/`no`; `cursor` is always `manual`, because no
  Cursor command file is ours to write.
- `adapter` names the native asset: `command` (a `commands/<name>.md` wrapper),
  `skill` (a `codex-skills/<name>/SKILL.md`), or both.

To add one: add the row, add the matching adapter file(s), run
`make embed-registry` (install.sh is served standalone for `curl | sh`, so it
carries an embedded copy of the registry), then run
`install/tests/registry_test.sh`. A registry entry with no adapter — or an
adapter with no entry, or an embedded copy that drifted — fails both that suite
and `npm pack`, so they cannot drift. pi's `/fm` and `/ufm` live in the
extension rather than in files; the registry test asserts its registered names
match the registry.

## Switching on and off

Once installed, flip a client between the Weave Router and talking to its
provider directly — without losing the router config, so switching back is
instant. These never prompt for a key and require an explicit client:

```bash
npx @workweave/router off --claude       # route Claude Code directly to Anthropic
npx @workweave/router on --claude        # route Claude Code through the router again
npx @workweave/router status --codex     # report whether Codex is on the router or direct
npx @workweave/router disable-routing    # switch Codex back to its default provider
npx @workweave/router off --opencode --scope project   # project-scoped opencode
```

Inside Claude Code you can also run the slash commands `/router-off`,
`/router-on`, `/router-status`, and `/router-session` (which prints the
session id used for telemetry correlation and transcript lookup) — installed
alongside `/force-model` (alias `/fm`), `/unforce-model` (alias `/ufm`),
`/router-feedback` (alias `/rf`), and `/router-models` (alias `/models`).

### Which directives each client gets

The installer derives this table from `install/directives.tsv`, the single
registry every client's install reads. Prompt directives reach the router
itself; local-config toggles flip config on disk, so they exist only where this
installer owns the config file.

| Directive | Claude Code | Codex | opencode | pi | Cursor |
| --- | --- | --- | --- | --- | --- |
| force-model (`fm`) | `/force-model` | `$force-model` | `/force-model` | `/fm` (native) | manual |
| unforce-model (`ufm`) | `/unforce-model` | `$unforce-model` | `/unforce-model` | `/ufm` (native) | manual |
| router-feedback (`rf`) | `/router-feedback` | `$router-feedback` | `/router-feedback` | — | manual |
| router-session | `/router-session` | — | — | — | — |
| router-off / on / status | `/router-off` … | `$router-off` … (plus `$disable-routing`) | — | — | — |
| router-models (`models`) | `/router-models` | `$router-models` | — | — | — |

- **Codex** uses `$name` skills because Codex reserves `/…` for built-ins; each
  skill sends the leading-space prompt form.
- **pi** implements `/fm` and `/ufm` in the `@workweave/router` extension rather
  than through installed files; the registry asserts its names match.
- **Cursor** exposes no command or skill file this installer owns, so its
  directives are manual: type the leading-space form ( `/force-model …`)
  yourself. The base-URL toggle lives in Cursor's own settings UI.
- The local toggles reach Claude Code and Codex, the two clients with a config
  file this installer writes. Codex's skills shell out to the same
  `weave-router off|on|status|models --codex` verbs the CLI exposes;
  `$disable-routing` predates `$router-off` and remains as an alias for it.
- `models` resolves one install's endpoint and key, so it needs a client that
  stores both: `--claude` or `--codex` (opencode and pi are not wired yet).

What each `off` does (and `on` reverses byte-for-byte):

- **Claude Code** — parks `ANTHROPIC_BASE_URL` + the key header out of
  `settings.json` so Claude Code falls back to its own Anthropic login. In
  project scope only the gitignored `settings.local.json` is touched, so the
  committed `settings.json` never shows up in `git diff`. **Claude Code reads
  env at launch, so quit and reopen it for an on/off to take effect.**
- **Codex** — comments the `model_provider = "weave"` line; the
  `[model_providers.weave]` block stays. Takes effect on the next `codex` run.
- **opencode** — parks and removes the top-level `weave/...` model so opencode
  reverts to its own default; `provider.weave` stays. Next `opencode` run.

**Cursor** has no config file we own — its base URL lives in Cursor's own
settings UI. To toggle it, open **Settings → Models → Override OpenAI Base
URL** and turn the override (`<base-url>/v1`) on or off there.

## Choosing which models the router may pick

`models` reads and edits the model selection for the installation whose key is
on disk — the same list, and the same stored setting, as the checkboxes on the
router dashboard's settings page. It writes nothing locally: the endpoint and
router key both come from the install already configured, so a self-hosted
install talks to its own router with its own key, and the key is never passed
as a command-line argument.

```bash
npx @workweave/router models --claude                          # every model, with its on/off state
npx @workweave/router models --codex                           # same, for a Codex install
npx @workweave/router models disable gpt-5.6 --claude          # take a model out of rotation
npx @workweave/router models enable gpt-5.6 --claude           # put it back
npx @workweave/router models providers --claude                # same, one row per provider
npx @workweave/router models providers disable openai --claude # drop a whole provider
npx @workweave/router models prefer claude-opus-5 --claude     # priority ranking ('clear' to drop it)
npx @workweave/router models list --json --claude              # machine-readable, for scripts
```

The list groups by provider and marks each model `[x]` (the router may pick it)
or `[ ]` (it may not):

```
Weave Router models · http://localhost:8080
2 of 3 enabled

anthropic
  [x] claude-opus-5
  [ ] claude-haiku-4-5
openai
  [x] gpt-5.6
```

Inside Claude Code, `/router-models` (alias `/models`) runs the same thing and
turns the result into a checklist you can edit conversationally — `/models
disable haiku` disables it and re-lists.

Changes take effect on the router's next routing decision; nothing restarts.
A model excluded here is never selected, so excluding everything in a cluster
leaves the router nothing to pick — re-enable rather than empty it out.

Editing requires a router that serves the model-selection API (self-hosted and
local routers do). The Weave-hosted router keeps model selection with the
organization instead, so there `models` lists what the router can pick from and
points you at <https://router.workweave.ai/dashboard/settings>. If your
deployment pins the lists via `ROUTER_EXCLUDED_MODELS` /
`ROUTER_EXCLUDED_PROVIDERS`, the router refuses the edit and says so — clear the
env var to make the setting editable.

## Verifying

**Claude Code:**

1. Run `claude`. The status line at the bottom should show
   `WEAVE ROUTER — <routed-model> ← <selected-model>` after one turn.
2. After several turns it should add `· saved $X turn / $Y session`.
3. Check `~/.weave-router/decisions.jsonl` — one row per request.

If the status line never appears, run `claude --debug` and check stderr for
errors invoking `cc-statusline.sh`. The script needs `jq` on PATH.

**Codex:**

1. Open `~/.codex/config.toml` (or `<repo>/.codex/config.toml` for project
   scope) and confirm the `# >>> weave-router managed >>>` block exists with
   your `X-Weave-Router-Key`. No install writes an `X-Weave-Router-Strategy`
   header; the router's own default applies.
2. Run `codex` and issue a turn. The terminal title should begin with
   `Weave Router · active`; after a routed-model marker it shows the latest
   actual model. The hooks do not add status messages to the conversation.
   Provider should be `Weave Router`.
3. Check the router's dashboard at `<base-url>/ui/dashboard` to see the HMM
   routed decision; Codex's `/status` shows its request model, not the
   upstream model selected by the router.

**opencode:**

1. Open `~/.config/opencode/opencode.json` (or `<repo>/opencode.json` for
   project scope) and confirm `provider.weave` exists with your
   `X-Weave-Router-Key` in `options.headers`.
2. Run `opencode` and select `weave/auto`. Issue a turn; Weave Router picks
   the upstream model for that turn.
3. Check the router's dashboard at `<base-url>/ui/dashboard` — traffic
   should be tagged `X-App: opencode`.

## Uninstall

```bash
npx @workweave/router --uninstall                       # Claude Code, user scope
npx @workweave/router --uninstall --codex               # Codex, user scope
npx @workweave/router --uninstall --opencode            # opencode, user scope
npx @workweave/router --uninstall --scope project       # Claude Code, in the repo
npx @workweave/router --uninstall --codex --scope project
npx @workweave/router --uninstall --opencode --scope project
```

Removes only the keys / block this installer added; everything else in
`settings.json` / `config.toml` is left alone.
