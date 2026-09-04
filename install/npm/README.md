# @workweave/router

One command, anywhere, to point Claude Code, Codex, opencode, or pi at the Weave Router.

```bash
npx @workweave/router                       # interactive: pick Claude Code / Codex / opencode / pi, then scope
npx @workweave/router --claude              # skip the picker, target Claude Code
npx @workweave/router --codex               # skip the picker, target the OpenAI Codex CLI
npx @workweave/router setup --claude --codex # configure both native clients
npx @workweave/router accounts list --claude # list enrolled subscription accounts
npx @workweave/router login claude           # enroll Claude Pro/Max with PKCE
npx @workweave/router login codex            # enroll ChatGPT Pro/Plus device flow
npx @workweave/router status                 # connectivity, native configs, account health
npx @workweave/router --opencode            # skip the picker, target opencode
npx @workweave/router --pi                  # skip the picker, target pi + Loom UI
npx @workweave/router --scope project       # per-repo install, commit settings.json (or .codex/ / opencode.json)
npx @workweave/router --local               # self-hosted via docker-compose (localhost:8080)
npx @workweave/router --base-url https://router.acme.internal
npx @workweave/router --non-interactive     # reads $WEAVE_ROUTER_KEY, no prompts (defaults to claude)
```

Re-running the installer to pick up changes reuses the key already on disk, so
you paste it once and never again — for every client, not just Claude Code.
`update` is the never-prompting form of that (safe for cron; errors instead of
asking when no key can be found):

```bash
npx @workweave/router --claude                # reuses the installed key
npx @workweave/router --codex                 # same for Codex, opencode, and pi
npx @workweave/router --claude --rotate-key   # ignore it and prompt for a new one
npx @workweave/router update --claude         # non-interactive refresh in place
```

For Claude Code the installed statusline and `/force-model`, `/router-*` slash
commands also refresh themselves in the background about once a week (never
overwriting a wrapper you edited). Opt out with `WEAVE_STATUSLINE_UPDATE=0`, or
just the commands with `WEAVE_COMMANDS_UPDATE=0`. Codex installs native `$` skills
plus managed `SessionStart`/`Stop` hooks: the latest routed model is reflected in
the terminal title and a compact `Weave Router · …` status message is shown when
the router reports a new route. Existing Codex hooks are preserved. OpenCode and pi
have their own target-specific integrations.

Version-pin for reproducible setups:

```bash
npx @workweave/router@0.1.0 --claude --scope project
```

Switch on/off without uninstalling (keeps your config so switching back is
instant; requires an explicit client):

```bash
npx @workweave/router off --claude      # route Claude Code directly to Anthropic
npx @workweave/router on --claude       # route Claude Code through the router again
npx @workweave/router status --codex    # is Codex on the router or direct?
```

Claude Code reads its router setting at launch, so quit and reopen it after an
on/off. Codex and opencode pick it up on their next run. Inside Claude Code the
slash commands `/router-off`, `/router-on`, and `/router-status` do the same.
Codex installs `$router-status`, `$router-off`, `$router-on`, and
`$router-models` skills that call the same CLI verbs, plus a
`$disable-routing` skill that switches its next session back
to the normal provider; Codex does not support third-party `/disable-routing`
slash commands. The shell equivalent is `npx @workweave/router disable-routing`.
Cursor has no config file we own — toggle its base URL override in **Settings →
Models** instead.

Pick which models the router is allowed to route to:

```bash
npx @workweave/router models --claude                  # list every model, with its on/off state
npx @workweave/router models --codex                   # same, for a Codex install
npx @workweave/router models disable gpt-5.6 --claude  # take one out of rotation
npx @workweave/router models enable gpt-5.6 --claude   # put it back
```

Inside Claude Code that's `/router-models` (alias `/models`). Editing needs a
router that serves the model-selection API; against the Weave-hosted router the
list still prints and points you at the dashboard, where model selection is an
organization-wide setting.

Uninstall:

```bash
npx @workweave/router --uninstall                       # Claude Code, user scope
npx @workweave/router --uninstall --codex               # Codex, user scope
npx @workweave/router --uninstall --opencode            # opencode, user scope
npx @workweave/router --uninstall --pi                  # pi, user scope
npx @workweave/router --uninstall --scope project       # Claude Code, inside the repo
npx @workweave/router --uninstall --codex --scope project
```

## What it does

This package is a thin Node wrapper around [`install.sh`](../install.sh) from
the Weave Router repo. It exists so you can install from any machine with
Node ≥ 18 — no `curl | sh`, no Git clone, no PATH fiddling. Everything the
shell installer documents (targets, scopes, flags, environment variables)
works identically here.

Four install targets:

- **Claude Code** (default) — patches `~/.claude/settings.json` (or
  `<repo>/.claude/settings.json` with `--scope project`) so `claude` routes
  through Weave automatically. Anthropic plan credentials flow through to
  api.anthropic.com.
- **Codex** (`--codex`) — patches `~/.codex/config.toml` (or
  `<repo>/.codex/config.toml`) with a managed `[model_providers.weave]`
  block plus `model_provider = "weave"`. The provider preserves the existing
  ChatGPT OAuth login. No install pins `X-Weave-Router-Strategy`; every
  endpoint keeps its router's configured default. HMM or forced
  `gpt-5.6-sol`, `gpt-5.6-terra`, and `gpt-5.6-luna` turns use that plan;
  every other selected model uses its WorkWeave deployment or BYOK credential.
  The block lives between begin/end markers
  so re-running the installer rewrites it cleanly and `--uninstall --codex`
  removes it without touching the rest of your config. Codex does not load
  third-party slash-command files; the installer provides native skills
  `$force-model` (`$fm`), `$unforce-model` (`$ufm`), and `$router-feedback`
  (`$rf`), each of which execs a local `scripts/emit.sh` that prints the same
  leading-space directive Claude Code uses (for example,
  ` /force-model gpt-5.6-terra`) — you can also type that form directly. It
  also installs `$router-status`, `$router-off`, `$router-on`, and
  `$router-models`, which call this installer's own verbs, plus a
  `$disable-routing` skill that returns the next Codex session to the default
  provider without logging out or deleting the router configuration. The
  managed lifecycle hooks also keep the latest routed model in the terminal
  title and emit a compact status message when the router reports a new route.
- **opencode** (`--opencode`) — merges a `provider.weave` entry (backed by
  opencode's built-in `@ai-sdk/anthropic` provider) into
  `~/.config/opencode/opencode.json` (or `<repo>/opencode.json` with
  `--scope project`). The router speaks the Anthropic Messages API
  natively, so opencode talks to it unmodified. Re-install rewrites only
  the managed `provider.weave` block; `--uninstall --opencode` strips it
  and leaves your other providers and settings alone.
- **pi** (`--pi`) — registers the `weave` provider and installs this package as
  a pi extension. Stock pi then gets the Loom startup header, Wooly's animated
  mascot, the persistent actual-route display, cumulative session savings,
  `/fm` + `/ufm` model-pin commands with a `[forced]` status, and the
  context-isolated `dispatch` tool. There is no forked pi binary and no separate
  Loom runtime.

See the [main installer docs](https://github.com/weave-os/router/tree/main/install)
for the full reference.

## Requirements

- Node ≥ 18 (ships with `npx`)
- `bash` on PATH (macOS / Linux native; Windows needs Git Bash or WSL)
- `jq` on PATH — used by the Claude Code status line, the Codex lifecycle helper, and the opencode/pi JSON merges.

## Why npx

`npx @workweave/router` gives Windows support via Git Bash, painless version
pinning, and discoverability via the npm registry.

## Older npm

On npm ≤ 6 the bundled `npx` treats an undeclared `-y` as consuming the next
token, so `npx -y @workweave/router --claude` silently drops the package name
and resolves the following argument as the command instead. Either upgrade
(`npm i -g npm@latest`) or name the binary explicitly:

```bash
npx --package @workweave/router -y -- weave-router --claude
```

That form is correct on every npm version.
