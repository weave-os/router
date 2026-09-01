#!/usr/bin/env bash
#
# Weave Router installer for Claude Code, Codex, opencode, and pi.
#
# Configures Claude Code (default), the OpenAI Codex CLI (`--codex`),
# opencode (`--opencode`), or pi (`--pi`) to permanently route through the
# Weave Router. For Claude Code this writes the router base URL, router auth
# header, and a status line into Claude Code's settings.json. For Codex it
# writes a `model_providers.weave` entry plus `model_provider = "weave"` into
# ~/.codex/config.toml (managed block delimited by markers). For opencode
# it merges a `provider.weave` block (anthropic-compatible) into
# opencode.json — since the file is JSON, install/uninstall are structural
# (jq) rather than marker-delimited. For pi it merges a `weave` provider into
# ~/.pi/agent/models.json, sets it as the default in settings.json, and adds
# the @workweave/router extension (which also adds a parallel subagent
# `dispatch` tool) — all structural (jq) merges.
#
# Two scopes (apply to all targets):
#   - user (default):  ~/.claude/settings.json  + ~/.weave/cc-statusline.sh
#                      ~/.codex/config.toml                       (with --codex)
#                      ~/.config/opencode/opencode.json           (with --opencode)
#                      ~/.pi/agent/{models,settings}.json         (with --pi)
#   - project:         <repo>/.claude/settings.json + <repo>/.claude/cc-statusline.sh
#                      <repo>/.codex/config.toml                  (with --codex)
#                      <repo>/opencode.json                       (with --opencode)
#                      <repo>/.pi/ (run: PI_CODING_AGENT_DIR=<repo>/.pi pi)  (with --pi)
#
# Or pass --dir to install into any directory:
#   - dir:              <dir>/.claude/settings.json + <dir>/.claude/cc-statusline.sh
#                       <dir>/.codex/config.toml                  (with --codex)
#                       <dir>/opencode.json                       (with --opencode)
#                       <dir>/.pi/ (run: PI_CODING_AGENT_DIR=<dir>/.pi pi)   (with --pi)
#
# Usage:
#   npx @workweave/router                                  # interactive picker (Claude Code, Codex, opencode)
#   npx @workweave/router --claude                         # skip the picker, target Claude Code
#   npx @workweave/router --codex                          # skip the picker, target Codex
#   npx @workweave/router --opencode                       # skip the picker, target opencode
#   npx @workweave/router --pi                              # skip the picker, target pi
#   npx @workweave/router --pi --lsp go,typescript          # also install language servers for pi's lsp tool
#                                                             (go/typescript/python/rust; needs that language's toolchain)
#   npx @workweave/router --scope project                  # commit-with-team install
#   npx @workweave/router --dir /tmp/my-sandbox            # isolated throwaway install
#   npx @workweave/router --local                          # local router on localhost:8080
#   npx @workweave/router --base-url http://localhost:8080 # self-hosted, custom port
#   npx @workweave/router --non-interactive                # require WEAVE_ROUTER_KEY env var (defaults target to claude)
#   npx @workweave/router --quiet                          # suppress banner, ping check, and trailing tips
#   npx @workweave/router --rotate-key                     # ignore the installed key and prompt for a new one
#   npx @workweave/router --uninstall                      # remove a previous install (delegates to uninstall.sh)
#
# Re-running the installer reuses the key already on disk, so you only paste it
# once — for every client, not just Claude Code. `update` is the scriptable form
# of that: it never prompts, refreshes the managed config + assets in place, and
# errors (rather than asking) when no key can be found:
#   npx @workweave/router update --claude                  # refresh the Claude Code install in place
#   npx @workweave/router update --codex                   # same for Codex / opencode / pi
#
# Toggle an existing install on/off without losing the router config (so
# switching back is instant). These never prompt for a key and require an
# explicit client (--claude / --codex / --opencode); they only flip config
# that install.sh already wrote:
#   npx @workweave/router off --claude                     # route directly to Anthropic again (Claude Code)
#   npx @workweave/router on --codex                       # route through the Weave Router again (Codex)
#   npx @workweave/router status --opencode                # report whether opencode is on the router or direct
#   npx @workweave/router disable-routing                   # Codex shortcut: use Codex's normal provider again
# Claude Code reads env at launch, so an off/on takes effect on the next
# `claude` start; Codex and opencode re-read config every invocation.
# Cursor's base URL lives in its own settings UI (no file we own), so there's
# nothing to toggle here — flip "Override OpenAI Base URL" in Cursor settings.
#
# Which router directives each client gets is declared once in
# install/directives.tsv and installed from there: Claude Code and opencode get
# Markdown slash commands, Codex gets native `$name` skills (it reserves `/…`
# for built-ins), pi registers /fm and /ufm in its extension, and Cursor is
# manual. See install/README.md for the full matrix.
#
# Inspect and edit which models this installation lets the router pick from —
# the same lists the router dashboard's settings page renders. The endpoint and
# router key both come from the install already on disk, so nothing is prompted
# and no key is ever passed on the command line:
#   npx @workweave/router models --claude                            # every model, with its on/off state
#   npx @workweave/router models disable gpt-5.6 --claude            # take a model out of rotation
#   npx @workweave/router models enable gpt-5.6 --claude             # put it back
#   npx @workweave/router models providers --claude                  # same, one row per provider
#   npx @workweave/router models providers disable openai --claude   # drop a whole provider
#   npx @workweave/router models prefer claude-opus-5 --claude       # set the preferred-model ranking
#   npx @workweave/router models list --json --claude                # machine-readable, for scripts
# Editing needs a router that mounts the model-selection API (self-hosted and
# local routers do). The Weave-hosted router keeps model selection with the
# organization, in the Weave dashboard — there `models` lists and points you at
# it rather than editing.

set -euo pipefail

# ---------- defaults ----------

# The public hosted Weave Router URL. Override with --base-url for self-hosted.
HOSTED_BASE_URL="https://router.workweave.ai"
DEFAULT_BASE_URL="${WEAVE_ROUTER_URL:-$HOSTED_BASE_URL}"


scope="user"
scope_explicit="false"
install_dir=""
base_url=""
base_url_explicit="false"
non_interactive="false"
quiet="false"
router_key_header="X-Weave-Router-Key"
# Target tool whose config we patch. "claude" (default) writes Claude Code
# settings.json; "codex" writes ~/.codex/config.toml; "opencode" merges a
# provider block into opencode.json. Each target carries its own
# credential-passthrough story in the router: Claude Code's logged-in
# Anthropic key flows through unchanged, Codex's `OPENAI_API_KEY` flows
# through via the same header path, and opencode talks to the router via
# its anthropic-compatible API surface. target_explicit tracks whether
# --claude / --codex / --opencode was passed so an interactive run can
# prompt for the choice.
target="claude"
target_explicit="false"

# Operation mode. "install" (default) writes/refreshes config. "update" is a
# never-prompting install that reuses the key already on disk. "off"/"on"/
# "status" toggle or report an existing install without touching the router
# key/identity — see the toggle_* helpers and the dispatch block below.
# "models" reads and edits the installation's model selection over the router's
# admin API; it touches no local config at all.
mode="install"
disable_routing_alias="false"

# `models` sub-verb plus its operands (model / provider ids), newline-delimited
# in argument order. Catalog ids and provider names never contain whitespace,
# so a newline-delimited string stays readable where a bash 3.2 array under
# `set -u` would not. --json switches the output to the raw API payload.
models_args=""
models_json="false"

# --lsp <langs>: comma-separated languages whose language servers to install
# for the pi extension's `lsp` tool (pi target only). Empty = don't install any.
lsp_langs=""

# --rotate-key forces the interactive key prompt even when a usable key is
# already installed, so a rotated key can replace it.
rotate_key="false"
# Where $api_key came from: env | disk | prompt. Drives the fallback re-prompt
# when /validate rejects a key we read back off disk.
api_key_source=""

# ---------- helpers ----------

# Detect whether stdout is a real terminal that grokks ANSI escapes. Pipes,
# CI logs, and `curl ... | sh` redirects all fail this check, so we degrade
# to plain ASCII output instead of leaking raw escape bytes.
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  tty_out="true"
else
  tty_out="false"
fi

# Brand color (#FF6C47) plus a few supporting shades. Truecolor escapes work
# on every modern terminal (iTerm2, Apple Terminal, vscode, ghostty, alacritty,
# wezterm, kitty); on TTY-less output we blank them out.
if [ "$tty_out" = "true" ]; then
  C_BRAND=$'\033[38;2;255;108;71m'
  C_DIM=$'\033[2m'
  C_BOLD=$'\033[1m'
  C_RED=$'\033[31m'
  C_YELLOW=$'\033[33m'
  C_GREEN=$'\033[32m'
  C_CYAN=$'\033[36m'
  C_RESET=$'\033[0m'
else
  C_BRAND=""; C_DIM=""; C_BOLD=""; C_RED=""; C_YELLOW=""; C_GREEN=""; C_CYAN=""; C_RESET=""
fi

err()  { printf "%serror:%s %s\n" "$C_RED" "$C_RESET" "$*" >&2; }
warn() { printf "%swarning:%s %s\n" "$C_YELLOW" "$C_RESET" "$*" >&2; }
info() { printf "%s==>%s %s\n" "$C_CYAN" "$C_RESET" "$*"; }
ok()   { printf "%s✓%s %s\n" "$C_GREEN" "$C_RESET" "$*"; }
skip() { printf "%s⊙%s %s%s%s\n" "$C_DIM" "$C_RESET" "$C_DIM" "$*" "$C_RESET"; }

# uninstall_cmd echoes the npx one-liner that reverses the current install,
# matching the target (--codex/--opencode), scope, and --dir so the hint we
# print after a successful install is copy-paste-correct. Kept in sync with
# uninstall.sh's flag surface.
uninstall_cmd() {
  # --package + `--` is load-bearing: npm <= 6's bundled npx treats an
  # undeclared `-y` as consuming the NEXT token, so `npx -y @workweave/router`
  # loses the package name and resolves whatever follows as the command.
  local cmd="npx --package @workweave/router -y -- weave-router --uninstall"
  case "$target" in
    codex)    cmd="$cmd --codex" ;;
    opencode) cmd="$cmd --opencode" ;;
    pi)       cmd="$cmd --pi" ;;
  esac
  [ "$scope" = "project" ] && cmd="$cmd --scope project"
  [ -n "$install_dir" ] && cmd="$cmd --dir $(printf '%q' "$install_dir")"
  printf '%s' "$cmd"
}

# print_uninstall_hint prints the reverse command on its own labeled line so
# every successful install ends by telling the user exactly how to undo it.
print_uninstall_hint() {
  [ "$quiet" = "true" ] && return 0
  printf "%sTo uninstall:%s %s%s%s\n" \
    "$C_BOLD" "$C_RESET" "$C_CYAN" "$(uninstall_cmd)" "$C_RESET"
}

# ---------- banner ----------
#
# Print the WEAVE wordmark in brand orange. Skipped under --quiet or when
# stdout isn't a TTY so log captures don't get junk box-drawing chars.
print_banner() {
  [ "$quiet" = "true" ] && return 0
  [ "$tty_out" = "true" ] || return 0
  local target_label
  case "$target" in
    codex)    target_label="Codex installer" ;;
    opencode) target_label="opencode installer" ;;
    pi)       target_label="pi installer" ;;
    *)        target_label="Claude Code installer" ;;
  esac
  printf '\n'
  printf '%s  ╦ ╦╔═╗╔═╗╦  ╦╔═╗%s\n' "$C_BRAND" "$C_RESET"
  printf '%s  ║║║║╣ ╠═╣╚╗╔╝║╣ %s\n' "$C_BRAND" "$C_RESET"
  printf '%s  ╚╩╝╚═╝╩ ╩ ╚╝ ╚═╝%s\n' "$C_BRAND" "$C_RESET"
  printf '  %sWeave Router · %s%s\n\n' "$C_DIM" "$target_label" "$C_RESET"
}

# ---------- spinner ----------
#
# Pure-bash spinner. `spin "label" cmd args...` runs cmd in the background,
# cycles dots frames in place while it runs, then replaces the line with
# ✓ or ✗ depending on exit status. Skipped (synchronous fallback) when
# stdout is not a TTY — pipes and CI logs would otherwise eat the carriage
# returns and leave a blob of frames. The command's own stdout/stderr is
# captured to $spin_log so we can echo it on failure for debugging.
#
# Frame set is `dots` from sindresorhus/cli-spinners.
SPIN_FRAMES='⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏'
SPIN_INTERVAL=0.08
spin_pid=""
spin_log=""

_spin_cleanup() {
  # Kill any active spinner child and restore the cursor. Called from the
  # global EXIT/INT/TERM/HUP trap so Ctrl-C never leaves a dangling spinner
  # process or a hidden cursor behind.
  if [ -n "$spin_pid" ] && kill -0 "$spin_pid" 2>/dev/null; then
    kill "$spin_pid" 2>/dev/null || true
    wait "$spin_pid" 2>/dev/null || true
  fi
  spin_pid=""
  if [ "$tty_out" = "true" ]; then
    printf '\033[?25h' # show cursor
  fi
  [ -n "$spin_log" ] && rm -f "$spin_log" 2>/dev/null || true
  # Also restore stty echo in case we died mid-keypaste prompt. macOS
  # `[ -r /dev/tty ]` returns true even when the underlying device errors
  # on open (ENXIO "Device not configured") under `curl | sh` and CI, so
  # we gate on stdin being an actual tty before touching it.
  if [ -t 0 ]; then
    stty echo 2>/dev/null || true
  fi
}
trap _spin_cleanup EXIT INT TERM HUP

spin() {
  local label="$1"; shift
  if [ "$tty_out" != "true" ] || [ "$quiet" = "true" ]; then
    # No spinner — just run the command and emit a single check line after.
    if "$@" >/dev/null 2>&1; then
      ok "$label"
      return 0
    else
      local rc=$?
      printf "%s✗%s %s\n" "$C_RED" "$C_RESET" "$label" >&2
      return $rc
    fi
  fi

  spin_log="$(mktemp -t weave-install.XXXXXX)"
  ( "$@" >"$spin_log" 2>&1 ) &
  spin_pid=$!

  printf '\033[?25l' # hide cursor
  local i=0
  # shellcheck disable=SC2206
  local frames=($SPIN_FRAMES)
  local n=${#frames[@]}
  while kill -0 "$spin_pid" 2>/dev/null; do
    printf '\r%s%s%s %s' "$C_BRAND" "${frames[i]}" "$C_RESET" "$label"
    i=$(( (i + 1) % n ))
    sleep "$SPIN_INTERVAL"
  done

  wait "$spin_pid"
  local rc=$?
  spin_pid=""
  printf '\033[?25h' # show cursor
  printf '\r\033[2K' # clear line

  if [ $rc -eq 0 ]; then
    printf '%s✓%s %s\n' "$C_GREEN" "$C_RESET" "$label"
    rm -f "$spin_log"
    spin_log=""
    return 0
  else
    printf '%s✗%s %s\n' "$C_RED" "$C_RESET" "$label" >&2
    if [ -s "$spin_log" ]; then
      printf '%s' "$C_DIM" >&2
      sed 's/^/    /' "$spin_log" >&2
      printf '%s' "$C_RESET" >&2
    fi
    rm -f "$spin_log"
    spin_log=""
    return $rc
  fi
}

usage() {
  # Print the leading comment block (lines 2..just-before `set -euo`), stripping
  # the leading `# `. awk avoids GNU `head -n -<N>`, which BSD head on macOS
  # rejects with "illegal line count -- -N". Banner sits above so `--help`
  # gets the same wordmark as a fresh install run.
  print_banner
  awk 'NR<2 { next } /^set -euo/ { exit } { sub(/^# ?/, ""); print }' "$0"
  exit "${1:-0}"
}

require_cmd() {
  local cmd="$1" hint="$2"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    err "$cmd is required but not installed."
    printf "  install: %s\n" "$hint" >&2
    exit 1
  fi
}

# Refuse to write through a symlink. Project scope reads the install path from
# the user's git repo; a malicious checkout could ship `.claude/settings.json`
# (or `.claude/` itself) as a symlink to e.g. `~/.ssh/authorized_keys`, and
# the installer's mkdir/chmod/cp/jq>file would silently follow that link.
refuse_if_symlink() {
  local target="$1"
  if [ -L "$target" ]; then
    err "$target is a symlink (-> $(readlink "$target")). Refusing to write through it."
    exit 1
  fi
}

# Markers that delimit the block this installer manages inside Codex's
# config.toml. Kept on disk verbatim so a re-install (or uninstall.sh
# --codex) can find and replace the block instead of duplicating it.
WEAVE_CODEX_BEGIN_MARKER="# >>> weave-router managed (do not edit between markers) >>>"
WEAVE_CODEX_END_MARKER="# <<< weave-router managed <<<"

# ---------- identity helpers ----------
#
# The router parses X-Weave-User-Email and X-Weave-User-Name on every protocol
# (Anthropic, OpenAI, Gemini) and persists them onto router.model_router_users
# so customers can attribute traffic to a person even when many people share
# one API key. Claude Code's metadata.user_id carries only account_uuid (no
# email), so without these headers the router only ever sees anonymous UUIDs.

# normalize_email mirrors the router's proxy.NormalizeEmail: trim, lowercase,
# enforce a single '@' with non-empty local + domain parts, and cap at 254
# chars (RFC 5321). Returns the cleaned address on stdout, or empty string if
# the input is missing or malformed. We validate locally so the installer
# never plants a header value the router would silently drop, and so a
# typo'd git config doesn't end up as a per-request identity claim.
normalize_email() {
  local raw="${1:-}"
  # Trim whitespace then lowercase. tr is POSIX; the [:upper:]/[:lower:] form
  # works on both macOS (BSD) and Linux (GNU) without needing LANG tweaks.
  local trimmed="${raw#"${raw%%[![:space:]]*}"}"
  trimmed="${trimmed%"${trimmed##*[![:space:]]}"}"
  local lowered
  lowered="$(printf '%s' "$trimmed" | tr '[:upper:]' '[:lower:]')"
  if [ -z "$lowered" ] || [ "${#lowered}" -gt 254 ]; then
    printf ''
    return
  fi
  # Reject any interior whitespace or control character so the value can't
  # smuggle a second header into the newline-delimited ANTHROPIC_CUSTOM_HEADERS
  # var. A valid email has none, so this is shape-only — not a deliverability
  # check.
  if printf '%s' "$lowered" | LC_ALL=C grep -q '[[:space:][:cntrl:]]'; then
    printf ''
    return
  fi
  case "$lowered" in
    *@*@*) printf ''; return ;;
    @*|*@) printf ''; return ;;
    *@*)   printf '%s' "$lowered" ;;
    *)     printf '' ;;
  esac
}

# normalize_name trims whitespace, rejects empty/oversized, and strips control
# chars + the colon/CR/LF chars HTTP forbids in header values. Display names
# are free-form so we don't case-fold; we just keep the header well-formed.
normalize_name() {
  local raw="${1:-}"
  local trimmed="${raw#"${raw%%[![:space:]]*}"}"
  trimmed="${trimmed%"${trimmed##*[![:space:]]}"}"
  # Drop CR/LF/colon (header smuggling) and other control chars. tr's -d
  # with a character class is portable across BSD/GNU.
  local cleaned
  cleaned="$(printf '%s' "$trimmed" | tr -d '\r\n:' | tr -d '[:cntrl:]')"
  if [ -z "$cleaned" ] || [ "${#cleaned}" -gt 128 ]; then
    printf ''
    return
  fi
  printf '%s' "$cleaned"
}

# resolve_user_email picks the email to plant in router request headers so the
# router can attribute traffic to a person even on shared API keys. Priority:
# WEAVE_USER_EMAIL env override → git config user.email → interactive prompt
# (pre-filled with whatever we found). In --non-interactive mode we never
# prompt, so unset/invalid means we ship no header (router treats that as
# account_uuid-only, same as today). Echoes the validated email on stdout.
resolve_user_email() {
  local candidate=""
  if [ -n "${WEAVE_USER_EMAIL:-}" ]; then
    candidate="$(normalize_email "$WEAVE_USER_EMAIL")"
    if [ -z "$candidate" ]; then
      warn "WEAVE_USER_EMAIL=\"$WEAVE_USER_EMAIL\" is not a valid email; ignoring."
    fi
  fi
  if [ -z "$candidate" ]; then
    local git_email
    git_email="$(git config --global user.email 2>/dev/null || true)"
    candidate="$(normalize_email "$git_email")"
  fi
  if [ "$non_interactive" = "true" ] || [ ! -r /dev/tty ]; then
    printf '%s' "$candidate"
    return
  fi
  # Interactive: confirm/edit. Empty input keeps the suggested default; a
  # literal `-` lets the user opt out (ship no header). This stays out of
  # --quiet runs because --quiet implies the caller doesn't want prompts;
  # they can still use WEAVE_USER_EMAIL to provide one explicitly.
  if [ "$quiet" = "true" ]; then
    printf '%s' "$candidate"
    return
  fi
  local prompt_default="$candidate"
  local response=""
  if [ -n "$prompt_default" ]; then
    printf "%sIdentify router traffic as %s[%s]%s (Enter to accept, '-' to skip): " \
      "$C_DIM" "$C_BOLD" "$prompt_default" "$C_RESET" >/dev/tty
  else
    printf "%sEmail to identify your router traffic (blank to skip): %s" \
      "$C_DIM" "$C_RESET" >/dev/tty
  fi
  read -r response </dev/tty || response=""
  case "$response" in
    "")   printf '%s' "$prompt_default" ;;
    "-")  printf '' ;;
    *)
      local cleaned
      cleaned="$(normalize_email "$response")"
      if [ -z "$cleaned" ]; then
        warn "\"$response\" is not a valid email; skipping identity header."
      fi
      printf '%s' "$cleaned"
      ;;
  esac
}

# write_codex_config writes a managed [model_providers.weave] block to the
# Codex CLI's config.toml. Sets `model_provider = "weave"` at the top level so
# Codex picks the routed provider by default. The provider requires OpenAI
# authentication, preserving the user's ChatGPT plan credential while the
# router key is sent independently. The router applies OAuth only to its
# native gpt-5.6 Sol/Terra/Luna family; all other routed models use WorkWeave
# deployment or BYOK credentials. Both settings live inside the managed-block
# markers so uninstall removes them cleanly. We strip any
# top-level `model_provider = ...` declaration OUTSIDE the markers before
# appending so the file doesn't end up with a duplicate key (TOML rejects
# that). Inline `model_provider` keys inside `[profiles.*]` sections stay
# untouched.
#
# Usage: write_codex_config <config_file_path> <base_url> <api_key> [user_email] [user_name]
write_codex_config() {
  local config_file="$1"
  local block_url="$2"
  local block_key="$3"
  local block_email="${4:-}"
  local block_name="${5:-}"

  # Escape `\` and `"` for TOML basic strings. Order matters: replace
  # backslashes first so the quotes we add next aren't double-escaped. A
  # display name like `John "J" Doe` would otherwise produce invalid TOML and
  # Codex would silently fail to parse config.toml — the installer's success
  # message wouldn't help diagnose. Router keys are alnum+`_` from the API so
  # safe as-is, but we escape uniformly for defense-in-depth.
  toml_escape() {
    local s="${1//\\/\\\\}"
    printf '%s' "${s//\"/\\\"}"
  }

  local esc_key esc_email esc_name esc_url esc_status
  esc_key="$(toml_escape "$block_key")"
  esc_email="$(toml_escape "$block_email")"
  esc_name="$(toml_escape "$block_name")"
  esc_url="$(toml_escape "$block_url")"
  esc_status="$(toml_escape "$codex_status_file")"

  # Plant whichever identity values we have alongside the router key so the
  # router can attribute Codex traffic to a person on shared keys. Build the
  # entries piecewise so an empty email/name is omitted entirely — the router
  # never sees a header with no value (and TOML rejects empty unquoted vals).
  local headers_parts="\"X-Weave-Router-Key\" = \"${esc_key}\""
  if [ -n "$block_email" ]; then
    headers_parts="${headers_parts}, \"X-Weave-User-Email\" = \"${esc_email}\""
  fi
  if [ -n "$block_name" ]; then
    headers_parts="${headers_parts}, \"X-Weave-User-Name\" = \"${esc_name}\""
  fi
  # Tag the client so telemetry can attribute traffic to Codex vs other CLIs
  # that share the same router key. The router otherwise has to guess from
  # User-Agent.
  headers_parts="${headers_parts}, \"X-App\" = \"codex\""
  # No strategy header: pinning one here freezes installed clients on whatever
  # policy was current at install time, so a deployment-default change never
  # reaches them. Every endpoint, hosted or self-hosted, uses its own default.
  local headers_line="http_headers = { ${headers_parts} }"

  local hook_feature_line="features.hooks = true"
  local hook_block=""
  local codex_hooks_enabled="true"
  if [ -f "$config_file" ]; then
    if awk '
      !in_section && /^[[:space:]]*hooks[[:space:]]*=/ { conflict=1 }
      $0 == "[hooks]" { conflict=1 }
      /^[[:space:]]*\[/ { in_section=1 }
      END { exit(conflict ? 0 : 1) }
    ' "$config_file"; then
      codex_hooks_enabled="false"
      hook_feature_line=""
    fi
  fi
  if [ -f "$config_file" ] && grep -q '^\[features\]$' "$config_file"; then
    hook_feature_line=""
  fi
  if [ "$codex_hooks_enabled" = "true" ]; then
    hook_block="$(cat <<TOML

[[hooks.SessionStart]]
[[hooks.SessionStart.hooks]]
type = "command"
command = "${esc_status}"

[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "command"
command = "${esc_status}"
TOML
)"
  fi
  local block
  block="$(cat <<TOML
${WEAVE_CODEX_BEGIN_MARKER}
# Managed by the Weave Router installer. Re-running the installer rewrites
# this block; \`./uninstall.sh --codex\` removes it. To opt out without
# uninstalling, change the model_provider value below.
${hook_feature_line}
model_provider = "weave"

[model_providers.weave]
name = "Weave Router"
base_url = "${esc_url}/v1"
wire_api = "responses"
requires_openai_auth = true
${headers_line}
${hook_block}
${WEAVE_CODEX_END_MARKER}
TOML
)"

  if [ -f "$config_file" ]; then
    local tmp; tmp="$(mktemp -t weave-codex.XXXXXX)"
    # Strip the managed block (between markers) plus any top-level
    # `model_provider =` outside it. We define "top-level" as everything
    # before the first `[section]` header. The awk handles both passes in
    # one sweep so we never emit a duplicate.
    awk -v begin="$WEAVE_CODEX_BEGIN_MARKER" -v end="$WEAVE_CODEX_END_MARKER" '
      $0 == begin { skip = 1; next }
      $0 == end   { skip = 0; next }
      skip        { next }
      /^[[:space:]]*\[/ { in_section = 1 }
      !in_section && /^[[:space:]]*model_provider[[:space:]]*=/ { next }
      { print }
    ' "$config_file" >"$tmp"

    # Insert the managed block at TOML top-level scope, NOT end-of-file. In
    # TOML, every bare key after a `[section]` header belongs to that
    # section, so appending `model_provider = "weave"` after a user's
    # existing `[profiles.foo]` would silently scope it as
    # `profiles.foo.model_provider` — Codex would never see the top-level
    # default and routing would silently fail to activate. We splice the
    # block in just before the first user section header so:
    #   <user's top-level keys>           ← still top-level
    #   <our managed block>               ← model_provider stays top-level
    #     [model_providers.weave]         ← scoped section, OK anywhere
    #   <user's sections>                 ← re-scope, unaffected
    local first_section
    first_section="$(awk '/^[[:space:]]*\[/ { print NR; exit }' "$tmp")"
    if [ -n "$first_section" ]; then
      # BSD `head -n 0` (macOS default) errors with "illegal line count"
      # and trips `set -euo pipefail`, leaving an empty config. Skip the
      # head call entirely when the file starts with a section header.
      {
        if [ "$first_section" -gt 1 ]; then
          head -n "$((first_section - 1))" "$tmp"
        fi
        printf "%s\n" "$block"
        tail -n "+${first_section}" "$tmp"
      } >"$config_file"
    else
      # No section headers in the existing file — every prior user key was
      # already at top-level. Our block ends with its own [section], so
      # appending is safe (no bare keys follow).
      cp "$tmp" "$config_file"
      printf "\n%s\n" "$block" >>"$config_file"
    fi
    rm -f "$tmp"
  else
    printf "%s\n" "$block" >"$config_file"
  fi

  # If the user already has a [features] table, place our managed hook
  # setting in that table instead of using a duplicate dotted key. Preserve an
  # explicit user setting and mark only the line this installer owns.
  if grep -q '^\[features\]$' "$config_file"; then
    if ! awk '
      /^\[features\]$/ { in_features=1; next }
      /^\[/ { in_features=0 }
      in_features && /^[[:space:]]*hooks[[:space:]]*=/ { found=1 }
      END { exit(found ? 0 : 1) }
    ' "$config_file"; then
      tmp="$(mktemp -t weave-codex-features.XXXXXX)"
      awk '/^\[features\]$/ && !inserted { print; print "hooks = true # weave-router managed codex hooks"; inserted=1; next } { print }' "$config_file" >"$tmp"
      mv "$tmp" "$config_file"
    fi
  fi
  if [ "$codex_hooks_enabled" != "true" ]; then
    warn "Existing Codex hooks configuration is not an inline array; preserving it and skipping the Weave status hooks. Routing is still enabled."
  fi
  # 0600: the file holds a router key. Even at user scope, mode 644 would
  # leak the key to any local user on a shared box.
  chmod 600 "$config_file"
}

# write_opencode_config merges the managed Weave provider(s) into opencode's
# opencode.json plus the bundled subscription plugin:
#   - provider.weave        : OpenAI/Responses-shaped (@ai-sdk/openai → /v1/responses).
#                             The single request provider. The router routes every
#                             turn across all models the caller's subscriptions +
#                             Weave key can pay for, and bills the plan matching the
#                             model it served. The default.
#   - provider.weave-claude : login-only storage for a Claude (Pro/Max) subscription
#                             (no models, never serves requests). Written only when
#                             the bundled plugin is present, since the Claude login
#                             method lives in the plugin. The `weave` loader reads
#                             this slot and attaches the Claude sub.
# The plugin (src bundled at $script_dir/opencode-weave/src/index.ts) is dropped
# into $opencode_dir/.weave/ and registered via opencode.json's `plugin` array
# by absolute path — scope-independent (no reliance on an auto-load dir). It owns
# both the ChatGPT (on `weave`) and Claude (on `weave-claude`) logins and attaches
# whichever subscriptions are connected to every request via the router's
# dedicated X-Weave-*-Subscription headers. Re-running rewrites the blocks
# in-place via jq (and strips the legacy `weave-codex` provider); uninstall
# strips them.
#
# Usage: write_opencode_config <config_file_path> <base_url> <api_key> [user_email] [user_name]
write_opencode_config() {
  local config_file="$1"
  local block_url="$2"
  local block_key="$3"
  local block_email="${4:-}"
  local block_name="${5:-}"

  # Build the headers object piecewise so empty email/name vanish from the
  # final JSON. opencode forwards the `headers` map verbatim to the upstream
  # provider, so the router sees the same X-Weave-* triplet here that it
  # would from Claude Code or Codex. The X-App tag lets router telemetry
  # attribute traffic to opencode specifically.
  local headers_json
  headers_json="$(jq -n \
    --arg key   "$block_key" \
    --arg email "$block_email" \
    --arg name  "$block_name" '
    {"X-Weave-Router-Key": $key, "X-App": "opencode"}
    | (if $email != "" then . + {"X-Weave-User-Email": $email} else . end)
    | (if $name  != "" then . + {"X-Weave-User-Name":  $name } else . end)
  ')"

  # Surface one Auto choice in opencode's picker. The router chooses the
  # upstream model for every request, so presenting pinned model names would
  # imply a choice that the router intentionally does not honor. Whichever
  # model serves a turn uses its matching subscription when one is connected.
  #
  # npm is @ai-sdk/openai and baseURL KEEPS its /v1 here: opencode's
  # @ai-sdk/openai provider appends /responses, yielding the router's
  # /v1/responses surface (the canonical inbound — the router translates to
  # Anthropic/OSS as it routes, and ships verbatim to the Codex backend for GPT
  # turns). apiKey is the router key as a parse-time placeholder; the router
  # authenticates off X-Weave-Router-Key (planted in headers) and the plugin's
  # loader attaches subscriptions via the dedicated X-Weave-*-Subscription
  # headers, so the apiKey value is never used upstream.
  local block
  block="$(jq -n \
    --arg url "$block_url/v1" \
    --arg key "$block_key" \
    --argjson headers "$headers_json" '
    {
      npm: "@ai-sdk/openai",
      name: "Weave Router",
      options: { apiKey: $key, baseURL: $url, headers: $headers },
      models: {
        auto: { name: "Auto" }
      }
    }
  ')"

  # Login-only storage provider for a Claude (Pro/Max) subscription. It serves
  # no requests (no models), so its npm/baseURL are inert — opencode just needs
  # it registered so the plugin's Claude login method has a home and `opencode
  # auth login` lists it. The `weave` loader reads this slot off disk and
  # attaches the Claude sub. Reuses the same router key + identity headers.
  local claude_block
  claude_block="$(jq -n \
    --arg url "$block_url/v1" \
    --arg key "$block_key" \
    --argjson headers "$headers_json" '
    {
      npm: "@ai-sdk/openai",
      name: "Weave Router — Claude plan",
      options: { apiKey: $key, baseURL: $url, headers: $headers },
      models: {}
    }
  ')"

  # Drop the Codex-subscription plugin next to the config and capture the
  # absolute path we'll register in opencode.json's `plugin` array (so it loads
  # regardless of scope). $config_file's dir is the already-created
  # opencode_dir, so the `cd … && pwd` canonicalization is safe — uninstall.sh
  # must canonicalize identically so the array entry matches on removal.
  #
  # Only register the path when the bundled source is actually present and
  # copied: registering a path with no file on disk makes opencode fail to load
  # a missing plugin. The plugin holds no secrets (router key lives in the
  # config; ChatGPT tokens live in opencode's own auth store), so 644 is fine.
  # Source is bundled alongside install.sh by the npm prepack
  # (scripts/copy-installer.js), same as commands/ + pi-router/.
  local plugin_dir plugin_spec plugin_src plugin_arg=""
  plugin_dir="$(cd "$(dirname "$config_file")" && pwd)/.weave"
  plugin_spec="$plugin_dir/opencode-weave.ts"
  plugin_src="$script_dir/opencode-weave/src/index.ts"
  if [ -f "$plugin_src" ]; then
    mkdir -p "$plugin_dir"
    cp "$plugin_src" "$plugin_spec"
    chmod 644 "$plugin_spec"
    plugin_arg="$plugin_spec"
  else
    warn "opencode subscription plugin source not found at $plugin_src — skipping the Claude login + subscription routing. (Use a packaged 'npx @workweave/router' install.)"
  fi

  # Merge into any existing opencode.json. We always overwrite provider.weave
  # so re-install reflects the latest key/identity, but we leave the rest of the
  # file (other providers, mcp, agent settings) untouched. A previously
  # installed `weave/*` model is migrated to `weave/auto`; unrelated provider
  # choices stay untouched.
  #
  # The weave-claude login provider AND the plugin entry are written together
  # only when the bundled plugin was present and copied ($plugin non-empty): the
  # Claude login method lives in the plugin, so registering the provider without
  # it (e.g. the `curl | sh` path, which carries no plugin source) would leave a
  # non-working login — instead we omit it (and strip any stale one). The legacy
  # `weave-codex` provider is always stripped: the single Responses `weave`
  # provider supersedes it.
  local merged
  if [ -f "$config_file" ]; then
    merged="$(jq \
      --argjson block "$block" \
      --argjson claude "$claude_block" \
      --arg plugin "$plugin_arg" \
      --arg pluginspec "$plugin_spec" '
      .provider = ((.provider // {}) | .weave = $block)
      | (.provider |= del(."weave-codex"))
      | (if $plugin != "" then .provider["weave-claude"] = $claude else .provider |= del(."weave-claude") end)
      # Register the managed plugin path when we installed it; otherwise strip a
      # stale entry left by a prior install (the provider was just removed, so a
      # lingering plugin reference would be dead weight).
      | (if $plugin != ""
           then .plugin = ((.plugin // []) | if index($plugin) then . else . + [$plugin] end)
           else (if (.plugin | type) == "array"
                   then (.plugin -= [$pluginspec]) | (if (.plugin | length) == 0 then del(.plugin) else . end)
                   else . end)
         end)
      | (if (.model // "") == "" then .model = "weave/auto" else . end)
      # Replace any legacy Weave model choice with the single auto-routing
      # choice. Models from unrelated providers remain unchanged.
      | (if (.model // "" | tostring) as $model
           | ($model | startswith("weave/") or startswith("weave-codex/") or startswith("weave-claude/"))
           then .model = "weave/auto" else . end)
      | (.["$schema"] //= "https://opencode.ai/config.json")
    ' "$config_file")"
  else
    merged="$(jq -n \
      --argjson block "$block" \
      --argjson claude "$claude_block" \
      --arg plugin "$plugin_arg" '
      {
        "$schema": "https://opencode.ai/config.json",
        model: "weave/auto",
        provider: { weave: $block }
      }
      | (if $plugin != "" then .provider["weave-claude"] = $claude | .plugin = [$plugin] else . end)
    ')"
  fi
  printf '%s\n' "$merged" >"$config_file"
  # 0600: the file holds a router key. Even at user scope, mode 644 would
  # leak the key to any local user on a shared box.
  chmod 600 "$config_file"
}

# write_pi_models_config merges a managed `weave` provider into pi's
# models.json (anthropic-compatible — the router speaks Anthropic Messages
# natively). The header set carries identity plus the main-loop routing knobs
# (quality bias); the @workweave/router extension re-registers the provider
# per process to flip those knobs for subagents/compaction. apiKey is the
# router key as well as a header — pi treats apiKey as required to consider auth
# configured, but the router authenticates off X-Weave-Router-Key
# (authHeader:false keeps Authorization free for BYOK). Re-running rewrites
# `.providers.weave` in place; uninstall strips it. chmod 600 — holds the key.
#
# Usage: write_pi_models_config <models_file> <base_url> <api_key> [user_email] [user_name]
write_pi_models_config() {
  local config_file="$1"
  local block_url="$2"
  local block_key="$3"
  local block_email="${4:-}"
  local block_name="${5:-}"

  # Identity + the main-loop (quality) routing knobs. Built piecewise so an
  # empty email/name vanishes from the JSON entirely.
  local headers_json
  headers_json="$(jq -n \
    --arg key   "$block_key" \
    --arg email "$block_email" \
    --arg name  "$block_name" '
    {
      "X-Weave-Router-Key": $key,
      "X-App": "pi",
      "x-weave-routing-marker": "off",
      "x-weave-routing-alpha": "0.8",
      "x-weave-routing-speed-weight": "0.05",
      "x-weave-routing-output-cost-ratio": "0.5",
      "x-weave-routing-expected-output-tokens": "3000"
    }
    | (if $email != "" then . + {"X-Weave-User-Email": $email} else . end)
    | (if $name  != "" then . + {"X-Weave-User-Name":  $name } else . end)
  ')"

  # Headline models surfaced in pi's /model picker. The router re-routes every
  # request regardless, so this list is UX; keep it Anthropic-shaped and in
  # sync with @workweave/router's WEAVE_MODELS constant.
  #
  # baseUrl is the router ROOT (no /v1): pi's anthropic-messages provider uses
  # @anthropic-ai/sdk, which appends /v1/messages itself. Unlike the codex block
  # above (OpenAI-style, base ends in /v1), a /v1 suffix here would produce
  # /v1/v1/messages and 404.
  local block
  block="$(jq -n \
    --arg url "$block_url" \
    --arg key "$block_key" \
    --argjson headers "$headers_json" '
    {
      baseUrl: $url,
      api: "anthropic-messages",
      apiKey: $key,
      authHeader: false,
      headers: $headers,
      models: [
        { id: "claude-fable-5",    name: "Claude Fable 5 (via Weave Router)",    reasoning: true, input: ["text","image"], contextWindow: 1000000, maxTokens: 128000 },
        { id: "claude-opus-5",     name: "Claude Opus 5 (via Weave Router)",     reasoning: true, input: ["text","image"], contextWindow: 1000000, maxTokens: 128000 },
        { id: "claude-opus-4-7",   name: "Claude Opus 4.7 (via Weave Router)",   reasoning: true, input: ["text","image"], contextWindow: 1000000, maxTokens: 64000 },
        { id: "claude-sonnet-4-6", name: "Claude Sonnet 4.6 (via Weave Router)", reasoning: true, input: ["text","image"], contextWindow: 1000000, maxTokens: 64000 },
        { id: "claude-haiku-4-5",  name: "Claude Haiku 4.5 (via Weave Router)",  reasoning: true, input: ["text","image"], contextWindow: 200000, maxTokens: 32000 },
        { id: "gpt-5.6-sol",       name: "GPT-5.6 Sol (via Weave Router)",       reasoning: true, input: ["text","image"], contextWindow: 1050000, maxTokens: 128000 },
        { id: "grok-4.5",          name: "Grok 4.5 (via Weave Router)",          reasoning: true, input: ["text","image"], contextWindow: 500000, maxTokens: 131072 },
        { id: "grok-4.6",          name: "Grok 4.6 (via Weave Router)",          reasoning: true, input: ["text","image"], contextWindow: 500000, maxTokens: 131072 }
      ]
    }
  ')"

  # Overwrite provider.weave only; leave any other providers/models the user
  # added untouched.
  local merged
  if [ -f "$config_file" ]; then
    merged="$(jq --argjson block "$block" '.providers = ((.providers // {}) | .weave = $block)' "$config_file")"
  else
    merged="$(jq -n --argjson block "$block" '{ providers: { weave: $block } }')"
  fi
  printf '%s\n' "$merged" >"$config_file"
  # 0600: the headers + apiKey hold the router key.
  chmod 600 "$config_file"
}

# write_pi_settings_config makes the `weave` provider pi's default and loads the
# @workweave/router extension. defaultProvider is always set to "weave" — the
# installer's job is to route via Weave; uninstall reverts it. defaultModel is set
# only when unset (don't clobber a user's model pick). The npm package source is
# appended to `packages` idempotently — pi auto-installs missing packages on
# startup — and the legacy `npm:@workweave/pi-router` id (from before the
# extension was folded into @workweave/router) is dropped so a config from the
# old layout can't keep a dangling/duplicate entry. No secret lives here, so no
# chmod 600.
#
# Usage: write_pi_settings_config <settings_file>
write_pi_settings_config() {
  local settings_file="$1"
  local pkg="npm:@workweave/router"
  local merged
  if [ -f "$settings_file" ]; then
    merged="$(jq --arg pkg "$pkg" '
      (.packages //= [])
      | (.packages -= ["npm:@workweave/pi-router"])
      | (if (.packages | index($pkg)) then . else .packages += [$pkg] end)
      | .defaultProvider = "weave"
      | (if (.defaultModel // "") == "" then .defaultModel = "claude-sonnet-4-6" else . end)
    ' "$settings_file")"
  else
    merged="$(jq -n --arg pkg "$pkg" '{
      defaultProvider: "weave",
      defaultModel: "claude-sonnet-4-6",
      packages: [$pkg]
    }')"
  fi
  printf '%s\n' "$merged" >"$settings_file"
}

# Language-server installs for the pi extension's `lsp` tool (--lsp go,ts,...).
# The alias -> server -> toolchain matrix mirrors LSP_SERVERS in
# install/pi-router/src/lsp-servers.ts — keep the two in lockstep. Every
# failure is a warn-and-continue: a missing toolchain must not fail the router
# install, and the extension re-offers conversationally at session start.
install_lsp_servers() {
  local langs="$1" lang
  local seen=" "
  for lang in $(printf '%s' "$langs" | tr ',' ' '); do
    local id="" bin="" alt_bin="" toolchain="" fallback_dir="" gopath=""
    local cmd=""
    case "$(printf '%s' "$lang" | tr '[:upper:]' '[:lower:]')" in
      go|golang)
        id="go"; bin="gopls"; toolchain="go"
        # go install honors $GOBIN, else <first GOPATH element>/bin (default ~/go/bin).
        gopath="${GOPATH:-$HOME/go}"
        fallback_dir="${GOBIN:-${gopath%%:*}/bin}"
        cmd="go install golang.org/x/tools/gopls@latest"
        ;;
      ts|typescript|js|javascript)
        id="typescript"; bin="typescript-language-server"; toolchain="npm"
        cmd="npm i -g typescript-language-server typescript"
        ;;
      py|python)
        id="python"; bin="pyright-langserver"; alt_bin="basedpyright-langserver"; toolchain="npm"
        cmd="npm i -g pyright"
        ;;
      rs|rust)
        id="rust"; bin="rust-analyzer"; toolchain="rustup"
        # rustup honors $CARGO_HOME (default ~/.cargo).
        fallback_dir="${CARGO_HOME:-$HOME/.cargo}/bin"
        cmd="rustup component add rust-analyzer"
        ;;
      *)
        warn "--lsp: unknown language '$lang' (valid: go, typescript, python, rust). Skipping."
        continue
        ;;
    esac
    case "$seen" in *" $id "*) continue ;; esac
    seen="$seen$id "

    if command -v "$bin" >/dev/null 2>&1 \
      || { [ -n "$alt_bin" ] && command -v "$alt_bin" >/dev/null 2>&1; } \
      || { [ -n "$fallback_dir" ] && [ -x "$fallback_dir/$bin" ]; }; then
      ok "$id language server already installed ($bin)"
      continue
    fi
    if ! command -v "$toolchain" >/dev/null 2>&1; then
      warn "--lsp $id: needs '$toolchain' on PATH (install command: $cmd). Skipping — re-run after installing the $toolchain toolchain."
      continue
    fi
    # shellcheck disable=SC2086 — $cmd is a fixed argv from the case above, never user input.
    if spin "Installing $id language server" $cmd; then
      ok "$id language server installed ($cmd)"
      if [ -n "$fallback_dir" ] && ! command -v "$bin" >/dev/null 2>&1; then
        info "$bin landed in $fallback_dir (not on PATH). The pi lsp tool finds it there; other tools may need a PATH entry."
      fi
    else
      warn "--lsp $id: '$cmd' failed. Run it manually, or ask pi to enable $id LSP support later."
    fi
  done
}

# resolve_user_name mirrors resolve_user_email but for display name. Priority:
# WEAVE_USER_NAME env override → git config user.name → empty. We don't
# prompt for name independently: if email prompting yielded nothing, name
# almost certainly will too, and a second prompt is noise. Echoes the
# validated name on stdout.
resolve_user_name() {
  local candidate=""
  if [ -n "${WEAVE_USER_NAME:-}" ]; then
    candidate="$(normalize_name "$WEAVE_USER_NAME")"
    if [ -z "$candidate" ]; then
      warn "WEAVE_USER_NAME=\"$WEAVE_USER_NAME\" is not a usable name; ignoring."
    fi
  fi
  if [ -z "$candidate" ]; then
    local git_name
    git_name="$(git config --global user.name 2>/dev/null || true)"
    candidate="$(normalize_name "$git_name")"
  fi
  printf '%s' "$candidate"
}

# ---------- uninstall delegation ----------
#
# `--uninstall` flips this script into a thin shim for uninstall.sh: the
# canonical uninstall logic lives in a sibling file, and we want both
# direct invocations (`./install.sh --uninstall`) and curl-piped ones
# (`curl ... | sh -s -- --uninstall`) to behave the same as
# `npx @workweave/router --uninstall` (which bin.js routes to uninstall.sh on
# its own).
#
# Scan every arg, not just $1, so flag order doesn't matter; build a clean
# list with --uninstall stripped and exec uninstall.sh with the remainder.
#
# Resolution order for the uninstall script:
#   1. Sibling file next to install.sh on disk (npm tarball / git checkout).
#   2. WEAVE_UNINSTALL_URL override (self-hosters who fork).
#   3. Default: raw.githubusercontent.com canonical copy (curl|sh path).
for arg in "$@"; do
  if [ "$arg" = "--uninstall" ]; then
    cleaned_args=()
    for a in "$@"; do
      [ "$a" = "--uninstall" ] || cleaned_args+=("$a")
    done

    script_path="${BASH_SOURCE[0]:-$0}"
    if [ -f "$script_path" ]; then
      sibling_dir="$(cd "$(dirname "$script_path")" 2>/dev/null && pwd)"
      if [ -n "$sibling_dir" ] && [ -f "$sibling_dir/uninstall.sh" ]; then
        exec bash "$sibling_dir/uninstall.sh" "${cleaned_args[@]+"${cleaned_args[@]}"}"
      fi
    fi

    require_cmd curl "https://curl.se"
    url="${WEAVE_UNINSTALL_URL:-https://raw.githubusercontent.com/workweave/router/main/install/uninstall.sh}"
    # Pull the body into memory and exec via `bash -c` so we never touch
    # disk: `exec` replaces this process, so any temp file we wrote would
    # outlive the EXIT trap and leak indefinitely. Loading into a variable
    # also gives us a chance to fail closed on 404 HTML pages before
    # handing the content to bash.
    if ! uninstall_body="$(curl -fsSL --max-time 30 "$url" 2>/dev/null)"; then
      err "Failed to fetch uninstall.sh from $url."
      exit 1
    fi
    if [ -z "$uninstall_body" ] || [ "${uninstall_body:0:2}" != "#!" ]; then
      err "Fetched content from $url doesn't look like a bash script."
      exit 1
    fi
    exec bash -c "$uninstall_body" weave-uninstall "${cleaned_args[@]+"${cleaned_args[@]}"}"
  fi
done

# ---------- arg parsing ----------

while [ $# -gt 0 ]; do
  case "$1" in
    --scope)
      scope="${2:-}"; shift 2
      [ "$scope" = "user" ] || [ "$scope" = "project" ] || { err "--scope must be 'user' or 'project'."; exit 2; }
      scope_explicit="true"
      ;;
    --base-url)
      base_url="${2:-}"; shift 2
      [ -n "$base_url" ] || { err "--base-url requires a value."; exit 2; }
      base_url_explicit="true"
      ;;
    --local)
      # Shorthand for local dev: localhost:8080 (matches `wv mr` / `make dev` default PORT).
      base_url="http://localhost:8080"
      base_url_explicit="true"
      shift
      ;;
    --non-interactive)
      non_interactive="true"; shift
      ;;
    --quiet)
      quiet="true"; shift
      ;;
    --rotate-key)
      rotate_key="true"; shift
      ;;
    --dir)
      install_dir="${2:-}"; shift 2
      [ -n "$install_dir" ] || { err "--dir requires a path."; exit 2; }
      ;;
    --codex)
      target="codex"; target_explicit="true"; shift
      ;;
    --opencode)
      target="opencode"; target_explicit="true"; shift
      ;;
    --pi)
      target="pi"; target_explicit="true"; shift
      ;;
    --lsp)
      lsp_langs="${2:-}"; shift 2
      [ -n "$lsp_langs" ] || { err "--lsp requires a comma-separated language list (go,typescript,python,rust)."; exit 2; }
      ;;
    --claude)
      # No-op selector for symmetry with --codex / --opencode. Useful in
      # pipelines that want to skip the interactive picker without depending
      # on the default.
      target="claude"; target_explicit="true"; shift
      ;;
    off|--off|on|--on|status|--status)
      # Toggle/report verbs. Bare (off) or dashed (--off) both accepted; the
      # npm wrapper forwards argv verbatim so either form reaches us.
      mode="${1#--}"; shift
      ;;
    update|--update)
      # Non-interactive refresh of an existing install. Takes the same target
      # and scope flags as install; resolves the key from env or disk only.
      mode="update"; shift
      ;;
    disable-routing|--disable-routing)
      # Convenience alias for the Codex-specific off toggle. Unlike generic
      # `off`, it deliberately resolves the target after all flags are parsed,
      # so `disable-routing --claude` cannot silently change Claude settings.
      mode="off"; disable_routing_alias="true"; shift
      ;;
    models|--models)
      # Model selection. Sub-verb and operand collection happens in the
      # catch-all arm below (guarded on mode="models"), not here — that lets
      # `--claude`/`--json`/etc. appear before, after, or between operands
      # instead of only after every operand has been consumed.
      mode="models"; shift
      ;;
    --json)
      models_json="true"; shift
      ;;
    -h|--help)
      usage 0
      ;;
    *)
      # In `models` mode, a bare (non-dashed) word is a sub-verb or operand —
      # collect it and keep parsing, so a flag can appear before, after, or
      # between them (`models --claude enable x` and `models enable x --claude`
      # both work). A dashed token nothing above matched is still an error,
      # models mode or not.
      if [ "$mode" = "models" ] && [ "${1#-}" = "$1" ]; then
        models_args="${models_args}${models_args:+$'\n'}$1"; shift
      else
        err "Unknown flag: $1."; usage 2
      fi
      ;;
  esac
done

# --lsp is a pi-extension feature, so it implies the pi target; combining it
# with another explicit target is a contradiction, not a preference.
if [ -n "$lsp_langs" ]; then
  if [ "$target_explicit" = "true" ] && [ "$target" != "pi" ]; then
    err "--lsp only applies to the pi target (the lsp tool ships in the pi extension). Drop --lsp or use --pi."
    exit 2
  fi
  target="pi"; target_explicit="true"
fi

if [ "$disable_routing_alias" = "true" ]; then
  if [ "$target_explicit" = "true" ] && [ "$target" != "codex" ]; then
    err "disable-routing only applies to Codex; omit the client flag or use --codex."
    exit 2
  fi
  target="codex"
  target_explicit="true"
fi

# Toggle verbs only flip config install.sh already wrote: no key, no identity,
# no prompts. Require an explicit client so we never guess which config to
# touch, and suppress every interactive prompt downstream. `models` edits no
# local config, but it still reads the endpoint and key out of one install, so
# it needs the same explicit choice.
if [ "$mode" != "install" ] && [ "$mode" != "update" ]; then
  non_interactive="true"
  if [ "$target_explicit" != "true" ]; then
    if [ "$mode" = "models" ]; then
      err "'models' requires an explicit client: --claude or --codex."
    else
      err "'$mode' requires an explicit client: --claude, --codex, or --opencode."
    fi
    exit 2
  fi
fi

# `models` needs to resolve one install's endpoint + key. Claude Code and Codex
# both expose readers for that (resolve_installed_endpoint / read_installed_key);
# opencode and pi do not have the endpoint-trust story worked out yet, so they
# still fail fast here rather than falling through to the toggle dispatch, which
# would flip config nobody asked to change.
if [ "$mode" = "models" ] && [ "$target" != "claude" ] && [ "$target" != "codex" ]; then
  err "'models' supports --claude and --codex only (it resolves the router endpoint from that client's config)."
  exit 2
fi

# --json prints the API payload verbatim, so nothing else may reach stdout.
if [ "$models_json" = "true" ]; then
  if [ "$mode" != "models" ]; then
    err "--json only applies to 'models'."
    exit 2
  fi
  quiet="true"
fi

# `update` is an install that never prompts: it refreshes the managed config
# and assets using the key already on disk (or in the environment). Suppressing
# prompts here also settles the target/scope pickers below on their defaults,
# which is what a cron/script caller wants.
if [ "$mode" = "update" ]; then
  non_interactive="true"
  if [ "$rotate_key" = "true" ]; then
    err "--rotate-key needs a prompt, which 'update' never issues. Re-run the installer without 'update'."
    exit 2
  fi
fi

# Toggle verbs (off/on/status) aren't implemented for pi — its config is a
# structural models.json/settings.json merge, reversed by the uninstaller
# rather than a single env/key line we can park and restore. `update` is not a
# toggle: it rewrites that same structural config in place, exactly as install
# does, so it belongs with install here.
if [ "$mode" != "install" ] && [ "$mode" != "update" ] && [ "$target" = "pi" ]; then
  err "Toggle verbs (off/on/status) aren't supported for --pi. Use 'npx @workweave/router --uninstall --pi' to remove, or re-run the installer to refresh."
  exit 2
fi

if [ -z "$base_url" ]; then
  base_url="$DEFAULT_BASE_URL"
fi
# trim trailing slash for cleanliness
base_url="${base_url%/}"

# ---------- interactive target prompt ----------

# If neither --claude nor --codex was passed and we have a controlling
# terminal, ask which tool to install for. Non-interactive runs (CI,
# `curl | sh --non-interactive`) silently use the "claude" default — same
# behavior the script had before --codex existed, so existing pipelines
# don't change semantics. We prompt BEFORE print_banner so the banner's
# target label (Claude Code installer / Codex installer) reflects the choice.
if [ "$target_explicit" = "false" ] && [ "$non_interactive" = "false" ] && [ -r /dev/tty ]; then
  printf "%sInstall target:%s\n" "$C_BOLD" "$C_RESET"
  printf "  %s1)%s Claude Code  %s— patches ~/.claude/settings.json (or <repo>/.claude/)%s\n" "$C_BRAND" "$C_RESET" "$C_DIM" "$C_RESET"
  printf "  %s2)%s Codex        %s— patches ~/.codex/config.toml (or <repo>/.codex/)%s\n" "$C_BRAND" "$C_RESET" "$C_DIM" "$C_RESET"
  printf "  %s3)%s opencode     %s— patches ~/.config/opencode/opencode.json (or <repo>/opencode.json)%s\n" "$C_BRAND" "$C_RESET" "$C_DIM" "$C_RESET"
  printf "  %s4)%s pi           %s— patches ~/.pi/agent/models.json + settings.json (or <repo>/.pi/)%s\n" "$C_BRAND" "$C_RESET" "$C_DIM" "$C_RESET"
  printf "Choose %s[1/2/3/4]%s (default %s1%s): " "$C_BOLD" "$C_RESET" "$C_BOLD" "$C_RESET"
  read -r target_choice </dev/tty || target_choice=""
  case "${target_choice:-1}" in
    1|""|claude|c|C)  target="claude" ;;
    2|codex|x|X)      target="codex" ;;
    3|opencode|o|O)   target="opencode" ;;
    4|pi|p|P)         target="pi" ;;
    *) err "Invalid choice: $target_choice."; exit 2 ;;
  esac
fi

# Banner runs before the interactive scope prompt so the very first thing
# users see when `make full-setup` hands off to install.sh is the wordmark,
# not a bare "Install scope:" line. Target prompt above already finalized
# $target, so the banner's per-target label reflects the user's choice.
# Toggle verbs stay terse — skip the banner so `status` prints one clean line.
[ "$mode" = "install" ] && print_banner

# ---------- interactive scope prompt ----------

# If the user didn't pass --scope and we have a controlling terminal, ask which
# scope to install into. Non-interactive runs (CI, `curl | sh --non-interactive`)
# silently use the "user" default.
if [ -z "$install_dir" ] && [ "$scope_explicit" = "false" ] && [ "$non_interactive" = "false" ] && [ -r /dev/tty ]; then
  # Per-target paths so the prompt text matches what actually gets written.
  case "$target" in
    codex)
      scope_user_path="~/.codex/"
      scope_project_path="<repo>/.codex/"
      scope_cli_label="codex"
      ;;
    opencode)
      # Match the actual install path, which honors XDG_CONFIG_HOME. Showing a
      # hardcoded "~/.config/opencode/" here lied to users with a custom
      # $XDG_CONFIG_HOME — they'd see one path in the prompt and the installer
      # would write to another.
      if [ -n "${XDG_CONFIG_HOME:-}" ]; then
        scope_user_path="$XDG_CONFIG_HOME/opencode/"
      else
        scope_user_path="~/.config/opencode/"
      fi
      scope_project_path="<repo>/opencode.json"
      scope_cli_label="opencode"
      ;;
    pi)
      scope_user_path="~/.pi/agent/"
      scope_project_path="<repo>/.pi/"
      scope_cli_label="pi"
      ;;
    *)
      scope_user_path="~/.claude/"
      scope_project_path="<repo>/.claude/"
      scope_cli_label="claude"
      ;;
  esac
  printf "%sInstall scope:%s\n" "$C_BOLD" "$C_RESET"
  printf "  %s1)%s user     %s— write to %s (applies everywhere you run %s)%s\n" "$C_BRAND" "$C_RESET" "$C_DIM" "$scope_user_path" "$scope_cli_label" "$C_RESET"
  printf "  %s2)%s project  %s— write to %s (applies only inside this repo)%s\n" "$C_BRAND" "$C_RESET" "$C_DIM" "$scope_project_path" "$C_RESET"
  printf "Choose %s[1/2]%s (default %s1%s): " "$C_BOLD" "$C_RESET" "$C_BOLD" "$C_RESET"
  read -r scope_choice </dev/tty || scope_choice=""
  case "${scope_choice:-1}" in
    1|""|user|u|U)    scope="user" ;;
    2|project|p|P)    scope="project" ;;
    *) err "Invalid choice: $scope_choice."; exit 2 ;;
  esac

  # For project scope, ask which directory rather than silently assuming CWD.
  # A user running this from a shell that happens to be in $HOME or some
  # unrelated repo would otherwise scribble .claude/ into the wrong place.
  if [ "$scope" = "project" ]; then
    default_project_dir="$(pwd)"
    printf "Project directory [default: %s]: " "$default_project_dir"
    read -r project_dir_choice </dev/tty || project_dir_choice=""
    project_dir="${project_dir_choice:-$default_project_dir}"
    # Expand a leading ~ since `read` doesn't.
    case "$project_dir" in
      "~")    project_dir="$HOME" ;;
      "~/"*)  project_dir="$HOME/${project_dir#~/}" ;;
    esac
    if [ ! -d "$project_dir" ]; then
      err "Directory does not exist: $project_dir."
      exit 1
    fi
    project_dir="$(cd "$project_dir" && pwd)"
  fi
fi

# ---------- pre-flight ----------

# `models` skips this line: it prints its own header carrying the endpoint it
# resolved, which is the part a reader actually needs.
if [ "$mode" != "install" ] && [ "$mode" != "update" ] && [ "$mode" != "models" ]; then
  [ "$quiet" = "true" ] || info "mode=${C_BOLD}${mode}${C_RESET}  scope=${C_BOLD}${scope}${C_RESET}  target=${C_BOLD}${target}${C_RESET}"
fi

# Codex install only writes a TOML file (managed via awk) so jq isn't needed.
# Claude Code's settings.json and opencode's opencode.json patching both use
# jq to deep-merge / structurally rewrite JSON. Toggling those clients reads
# and rewrites the same JSON, so jq is required there too.
#
# `models` is the exception for Codex: it renders and edits the router's JSON
# payloads with jq regardless of which client's config supplied the endpoint,
# so require it there too rather than dying mid-render on `jq: command not
# found`. Installing Codex itself still needs no jq.
if [ "$target" = "claude" ] || [ "$target" = "opencode" ] || [ "$target" = "pi" ] \
   || { [ "$mode" = "models" ] && [ "$target" = "codex" ]; }; then
  require_cmd jq    "macOS: 'brew install jq' · Debian/Ubuntu: 'sudo apt install jq'"
fi
# curl is used by the install/update paths' health/validate probes and by every
# `models` call; the on/off/status toggles never hit the network.
if [ "$mode" = "install" ] || [ "$mode" = "update" ] || [ "$mode" = "models" ]; then
  require_cmd curl  "macOS/Linux: usually preinstalled — check your package manager"
fi

# Every warning below says the client's config will be written anyway, which is
# true for install/update/toggles and false for `models` — it only reads that
# config to find the router. Whether the client itself is installed has no
# bearing on a model-selection call, so skip the check there entirely.
if [ "$mode" != "models" ]; then
case "$target" in
  claude)
    if ! command -v claude >/dev/null 2>&1; then
      warn "'claude' not found on PATH. Install Claude Code from https://claude.com/code, then re-run this script."
      warn "Continuing — settings.json will be written and will take effect once Claude Code is installed."
    fi
    ;;
  codex)
    if ! command -v codex >/dev/null 2>&1; then
      warn "'codex' not found on PATH. Install via 'npm install -g @openai/codex' (or brew install codex), then re-run this script."
      warn "Continuing — config.toml will be written and will take effect once Codex is installed."
    fi
    ;;
  opencode)
    if ! command -v opencode >/dev/null 2>&1; then
      warn "'opencode' not found on PATH. Install from https://opencode.ai (or 'npm install -g opencode-ai'), then re-run this script."
      warn "Continuing — opencode.json will be written and will take effect once opencode is installed."
    fi
    ;;
  pi)
    if ! command -v pi >/dev/null 2>&1; then
      warn "'pi' not found on PATH. Install with 'npm install -g @mariozechner/pi-coding-agent', then re-run this script."
      warn "Continuing — models.json/settings.json will be written and take effect once pi is installed."
    fi
    ;;
esac
fi

script_dir="$(cd "$(dirname "$0")" 2>/dev/null && pwd || true)"

# ---------- directive registry (embedded) ----------
#
# install.sh is served standalone (WorkWeave serves it for `curl | sh`), so it
# cannot source install/registry.sh or read install/directives.tsv. Both are
# embedded here verbatim; install/tests/registry_test.sh asserts they never
# drift from the canonical files.
WEAVE_REGISTRY_DATA=$(cat <<'WEAVE_REGISTRY_EOF'
# Weave Router directive registry
# canonical|aliases|capability|claude|codex|opencode|pi|cursor|adapter
# aliases are comma-separated; client columns are yes/no. adapter is the native asset kind.
force-model|fm|prompt|yes|yes|yes|yes|manual|command,skill
unforce-model|ufm|prompt|yes|yes|yes|yes|manual|command,skill
router-feedback|rf|prompt|yes|yes|yes|no|manual|command,skill
router-off||local-toggle|yes|yes|no|no|manual|command,skill
router-on||local-toggle|yes|yes|no|no|manual|command,skill
router-status||local-toggle|yes|yes|no|no|manual|command,skill
router-session||prompt|yes|no|no|no|manual|command
router-models|models|local-toggle|yes|yes|no|no|manual|command,skill
disable-routing||local-toggle|no|yes|no|no|manual|skill
beta||prompt|yes|no|no|yes|manual|command
WEAVE_REGISTRY_EOF
)

# >>> weave-router registry lib >>>
weave_registry_rows() {
  if [ -n "${WEAVE_REGISTRY_DATA:-}" ]; then
    printf '%s\n' "$WEAVE_REGISTRY_DATA" | awk -F '|' '!/^([[:space:]]*#|[[:space:]]*$)/ { print }'
    return 0
  fi
  local registry="${WEAVE_REGISTRY_FILE:-}"
  if [ -z "$registry" ]; then
    local dir
    dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd -P)" || return 1
    registry="$dir/directives.tsv"
  fi
  [ -f "$registry" ] || return 1
  awk -F '|' '!/^([[:space:]]*#|[[:space:]]*$)/ { print }' "$registry"
}

# weave_registry_names lists every canonical name and alias a client installs.
weave_registry_names() {
  local target="$1" canonical aliases capability claude codex opencode pi cursor adapter
  while IFS='|' read -r canonical aliases capability claude codex opencode pi cursor adapter; do
    case "$target" in
      claude) [ "$claude" = yes ] || continue ;; codex) [ "$codex" = yes ] || continue ;;
      opencode) [ "$opencode" = yes ] || continue ;; pi) [ "$pi" = yes ] || continue ;;
      cursor) [ "$cursor" = yes ] || continue ;;
    esac
    printf '%s\n' "$canonical"
    [ -n "$aliases" ] && printf '%s\n' "$aliases" | tr ',' '\n'
  done <<EOF
$(weave_registry_rows)
EOF
}

# weave_registry_skill_names lists the prompt directives a client exposes as a
# native skill. Local-config toggles are excluded: they mutate config this
# installer owns and are handled per target, not as a generic prompt.
weave_registry_skill_names() {
  local target="$1" canonical aliases capability claude codex opencode pi cursor adapter
  while IFS='|' read -r canonical aliases capability claude codex opencode pi cursor adapter; do
    [ "$capability" = prompt ] || continue
    case "$target" in
      claude) [ "$claude" = yes ] || continue ;; codex) [ "$codex" = yes ] || continue ;;
      opencode) [ "$opencode" = yes ] || continue ;; pi) [ "$pi" = yes ] || continue ;;
      cursor) [ "$cursor" = yes ] || continue ;;
    esac
    printf '%s\n' "$canonical"
    [ -n "$aliases" ] && printf '%s\n' "$aliases" | tr ',' '\n'
  done <<EOF
$(weave_registry_rows)
EOF
}

# weave_registry_skill_assets lists every directive a client ships as a skill
# file, including local-config toggles such as Codex's disable-routing. Install
# and uninstall use this for file management; weave_registry_skill_names is the
# narrower prompt-only set used when generating prompt adapters.
weave_registry_skill_assets() {
  local target="$1" canonical aliases capability claude codex opencode pi cursor adapter
  while IFS='|' read -r canonical aliases capability claude codex opencode pi cursor adapter; do
    case ",$adapter," in *,skill,*) ;; *) continue ;; esac
    case "$target" in
      claude) [ "$claude" = yes ] || continue ;; codex) [ "$codex" = yes ] || continue ;;
      opencode) [ "$opencode" = yes ] || continue ;; pi) [ "$pi" = yes ] || continue ;;
      cursor) [ "$cursor" = yes ] || continue ;;
    esac
    printf '%s\n' "$canonical"
    [ -n "$aliases" ] && printf '%s\n' "$aliases" | tr ',' '\n'
  done <<EOF
$(weave_registry_rows)
EOF
}

# weave_registry_canonical_for resolves a name or alias to its canonical
# directive, and fails when the name is not in the registry at all.
weave_registry_canonical_for() {
  local wanted="$1" canonical aliases capability claude codex opencode pi cursor adapter alias
  while IFS='|' read -r canonical aliases capability claude codex opencode pi cursor adapter; do
    [ "$wanted" = "$canonical" ] && { printf '%s' "$canonical"; return 0; }
    IFS=',' read -ra _aliases <<< "$aliases"
    for alias in ${_aliases[@]+"${_aliases[@]}"}; do
      [ "$wanted" = "$alias" ] && { printf '%s' "$canonical"; return 0; }
    done
  done <<EOF
$(weave_registry_rows)
EOF
  return 1
}
# <<< weave-router registry lib <<<

# Resolve the base directory. User scope always uses $HOME. Project scope uses
# --dir if given, otherwise the CWD's git root. --dir alone (no --scope) is a
# throwaway user-style install.
if [ -n "$install_dir" ]; then
  install_dir="$(cd "$install_dir" 2>/dev/null && pwd || echo "$install_dir")"
  settings_base="$install_dir"
else
  case "$scope" in
    user)
      settings_base="$HOME"
      ;;
    project)
      # If the interactive prompt collected a project directory, use it.
      # Otherwise fall back to the git root of CWD (the original behavior,
      # preserved for --scope project passed on the command line).
      if [ -n "${project_dir:-}" ]; then
        settings_base="$project_dir"
        git_root="$(cd "$project_dir" && git rev-parse --show-toplevel 2>/dev/null || true)"
      else
        if ! git_root="$(git rev-parse --show-toplevel 2>/dev/null)"; then
          err "--scope project must be run inside a git repo, or pass --dir <path>. cd into your project first, or use --dir."
          exit 1
        fi
        settings_base="$git_root"
      fi
      ;;
  esac
fi

if [ "$target" = "claude" ]; then
  case "$scope" in
    user)
      settings_dir="$settings_base/.claude"
      settings_file="$settings_dir/settings.json"
      local_settings_file=""
      statusline_dir="${settings_base}/.weave"
      statusline_file="$statusline_dir/cc-statusline.sh"
      statusline_path_for_settings="$statusline_file"
      ;;
    project)
      settings_dir="$settings_base/.claude"
      settings_file="$settings_dir/settings.json"
      local_settings_file="$settings_dir/settings.local.json"
      statusline_dir="$settings_base/.claude"
      statusline_file="$statusline_dir/cc-statusline.sh"
      # Portable relative path for real repos (teammates can clone anywhere).
      # Absolute path when --dir overrides (no meaningful $CLAUDE_PROJECT_DIR).
      if [ -z "$install_dir" ]; then
        statusline_path_for_settings="\${CLAUDE_PROJECT_DIR}/.claude/cc-statusline.sh"
      else
        statusline_path_for_settings="$statusline_file"
      fi
      ;;
  esac

  # Symlink containment: refuse if any target path is a symlink. User-scope
  # paths under $HOME are trusted; project-scope and --dir paths come from a
  # git repo or user-supplied directory that may be hostile, so we check those.
  if [ "$scope" = "project" ] || [ -n "$install_dir" ]; then
    refuse_if_symlink "$settings_dir"
    refuse_if_symlink "$settings_file"
    refuse_if_symlink "$local_settings_file"
    refuse_if_symlink "$statusline_file"
  fi

  mkdir -p "$settings_dir" "$statusline_dir"
elif [ "$target" = "codex" ]; then
  # Codex CLI reads config from ~/.codex/config.toml by default. For project
  # scope we write to <repo>/.codex/config.toml; the user invokes Codex with
  # `CODEX_HOME=<repo>/.codex codex` (or runs from the repo if Codex auto-
  # discovers). The router key is embedded in the file so it stays per-
  # teammate — .codex/config.toml goes in .gitignore in project scope.
  codex_dir="$settings_base/.codex"
  codex_config_file="$codex_dir/config.toml"
  if [ "$scope" = "user" ] && [ -z "$install_dir" ]; then
    codex_status_file="$settings_base/.weave/codex-status.sh"
  else
    codex_status_file="$codex_dir/weave-status.sh"
  fi
  codex_status_disabled_marker="$(dirname "$codex_status_file")/.weave-router-disabled"

  if [ "$scope" = "project" ] || [ -n "$install_dir" ]; then
    refuse_if_symlink "$codex_dir"
    refuse_if_symlink "$codex_config_file"
    refuse_if_symlink "$codex_status_file"
    refuse_if_symlink "$codex_status_disabled_marker"
  fi

  mkdir -p "$codex_dir"
elif [ "$target" = "pi" ]; then
  # pi reads ~/.pi/agent/ by default, so a plain `pi` picks up a user-scope
  # install with no env var. models.json is global-only in pi (there is no
  # project-level models file), so for project/--dir scope we point
  # PI_CODING_AGENT_DIR at a repo-local .pi that holds the whole config
  # (models.json + settings.json + key) — the same shape as Codex's CODEX_HOME.
  # The router key is embedded, so .pi goes in .gitignore for project scope.
  case "$scope" in
    user)    pi_dir="$settings_base/.pi/agent" ;;
    project) pi_dir="$settings_base/.pi" ;;
  esac
  # --dir is a self-contained sandbox: flat .pi, launched via PI_CODING_AGENT_DIR.
  [ -n "$install_dir" ] && pi_dir="$install_dir/.pi"
  pi_models_file="$pi_dir/models.json"
  pi_settings_file="$pi_dir/settings.json"
  pi_key_file="$pi_dir/.weave_router_key"

  if [ "$scope" = "project" ] || [ -n "$install_dir" ]; then
    refuse_if_symlink "$pi_dir"
    refuse_if_symlink "$pi_models_file"
    refuse_if_symlink "$pi_settings_file"
    refuse_if_symlink "$pi_key_file"
  fi

  mkdir -p "$pi_dir"
else
  # opencode discovers config in this order: $XDG_CONFIG_HOME/opencode/opencode.json
  # (or ~/.config/opencode/opencode.json) for user scope, and opencode.json /
  # opencode.jsonc walked up from CWD for project scope. We standardize on
  # opencode.json at the repo root for project scope (the option teammates can
  # commit) and the XDG path for user scope. The router key is embedded so
  # opencode.json goes in .gitignore for project scope, same as Codex.
  case "$scope" in
    user)
      opencode_dir="${XDG_CONFIG_HOME:-$settings_base/.config}/opencode"
      ;;
    project)
      opencode_dir="$settings_base"
      ;;
  esac
  # --dir overrides both scopes: drop opencode.json straight into <dir>/ so
  # the sandbox is self-contained (mirrors how --dir behaves for Codex).
  if [ -n "$install_dir" ]; then
    opencode_dir="$install_dir"
  fi
  opencode_config_file="$opencode_dir/opencode.json"

  if [ "$scope" = "project" ] || [ -n "$install_dir" ]; then
    refuse_if_symlink "$opencode_dir"
    refuse_if_symlink "$opencode_config_file"
  fi

  mkdir -p "$opencode_dir"
fi

# ---------- off / on / status (toggle an existing install) ----------
#
# Flip a client between the Weave Router and talking to its provider directly,
# WITHOUT discarding the router config — so switching back is one command. We
# only run this for the `off`/`on`/`status` verbs; `install` falls straight
# through to the write path below. An explicit client was already required
# during arg parsing, so exactly one of the toggle_* helpers fires.
#
# Per-client "off" mechanics (each leaves the router config in place so "on"
# restores it byte-for-byte):
#   Claude Code — moves ANTHROPIC_BASE_URL + the key header out of settings
#                 into a parked sidecar; CC falls back to its own login.
#   Codex       — comments the `model_provider = "weave"` line inside the
#                 managed block; the [model_providers.weave] section stays.
#   opencode    — parks the top-level `model` and removes it; opencode reverts
#                 to its own default. The provider.weave block stays.
# Claude Code reads env at launch, so its toggle lands on the next `claude`
# start; Codex/opencode re-read config every run.

# router_shaped_url returns 0 when a base URL points at the router — i.e. it's
# neither empty nor Anthropic's own endpoint (what "off" falls back to).
router_shaped_url() {
  case "$1" in
    ""|https://api.anthropic.com*|http://api.anthropic.com*) return 1 ;;
    *) return 0 ;;
  esac
}

# json_get prints a jq scalar from a file, or empty when the file/key is
# absent. Never trips set -e.
json_get() {
  [ -f "$1" ] || return 0
  jq -r "${2} // empty" "$1" 2>/dev/null || true
}

# read_claude_key prints the router key already installed in the given settings
# file, or nothing when the file is absent / carries no key header. Claude Code
# packs several headers into one newline-delimited ANTHROPIC_CUSTOM_HEADERS
# value, so we pick the router-key line out of it and trim the header name.
# Read-only and tolerant of every missing-file case — never trips set -e.
read_claude_key() {
  [ -n "${1:-}" ] || return 0
  json_get "$1" '.env.ANTHROPIC_CUSTOM_HEADERS' \
    | sed -n "s/^[[:space:]]*${router_key_header}:[[:space:]]*//p" \
    | head -n 1 \
    | tr -d '[:space:]'
}

# claude_key_present returns 0 when the given settings file's
# env.ANTHROPIC_CUSTOM_HEADERS carries the router key header. "On" is only valid
# when this is true: in project scope the committed settings.json holds the
# router URL but the key header lives only in the per-teammate settings.local.json
# (or the parked sidecar), so a router URL alone doesn't mean requests can
# authenticate.
claude_key_present() {
  [ -n "$(read_claude_key "$1")" ]
}

# key_source_is_own returns 0 when a config file is safe to read a router key
# back out of. Codex, opencode, and pi all embed the key in a file that lives
# INSIDE the project in project scope, and the installer gitignores each one —
# so a file git tracks is by definition not this user's own install. A hostile
# checkout could commit one carrying an attacker's rk_ key, and adopting it
# would bill the developer's traffic to whoever wrote the repo. Same tracked-file
# test models_endpoint_is_trusted applies to a planted endpoint.
#
# Claude Code deliberately does NOT go through this: read_installed_key is
# documented to pick up a committed header from an older install, which is a
# supported case there.
key_source_is_own() {
  local f="$1"
  [ -f "$f" ] || return 1
  [ -L "$f" ] && return 1
  # Only project-scoped installs put the file somewhere a repo can reach.
  [ "$scope" = "project" ] && [ -z "$install_dir" ] || return 0
  command -v git >/dev/null 2>&1 || return 1
  git -C "$(dirname "$f")" ls-files --error-unmatch -- "$f" >/dev/null 2>&1 && return 1
  return 0
}

# read_codex_key prints the router key out of Codex's config.toml, or nothing.
# Scoped to the managed block so a key-shaped string the user wrote elsewhere in
# the file is never adopted. awk rather than jq on purpose: the Codex path is the
# one target that doesn't require jq (see the require_cmd block above).
read_codex_key() {
  local f="$1"
  key_source_is_own "$f" || return 0
  awk -v begin="$WEAVE_CODEX_BEGIN_MARKER" -v end="$WEAVE_CODEX_END_MARKER" '
    $0 == begin { inblk = 1; next }
    $0 == end   { inblk = 0; next }
    inblk && match($0, /"X-Weave-Router-Key"[[:space:]]*=[[:space:]]*"[^"]*"/) {
      hdr = substr($0, RSTART, RLENGTH)
      sub(/^.*=[[:space:]]*"/, "", hdr)
      sub(/"$/, "", hdr)
      print hdr
      exit
    }
  ' "$f" 2>/dev/null || true
}

# read_opencode_key prints the router key out of opencode.json's managed
# provider, or nothing. The header is the authoritative copy — options.apiKey is
# only a parse-time placeholder for the @ai-sdk/openai provider.
read_opencode_key() {
  local f="$1"
  key_source_is_own "$f" || return 0
  json_get "$f" '.provider.weave.options.headers["X-Weave-Router-Key"]'
}

# read_pi_key prints the router key this pi install already has. The dedicated
# key file comes first — it is what the @workweave/router extension itself reads
# at runtime — and models.json is the fallback for an install whose key file was
# removed by hand.
read_pi_key() {
  local key=""
  if key_source_is_own "$pi_key_file"; then
    key="$(tr -d '[:space:]' <"$pi_key_file" 2>/dev/null || true)"
  fi
  if [ -z "$key" ] && key_source_is_own "$pi_models_file"; then
    key="$(json_get "$pi_models_file" '.providers.weave.headers["X-Weave-Router-Key"]')"
  fi
  printf '%s' "$key"
}

# read_installed_key prints the router key this install already has on disk, or
# nothing, for whichever client is being installed. Reading it back is what makes
# a re-run painless — see the token-handling section below. Defined up here
# rather than with the rest of token handling because `models` dispatches (and
# needs a key) before that section runs.
#
# models_key_file_order echoes the precedence the Claude reader uses, so a caller
# that needs to know *which* file the key came from can walk the same list (a
# command substitution around this function would discard any global it set).
# models_config_file_for_target names the single managed config that holds both
# the endpoint and the key for a non-Claude client. Endpoint and credential from
# the same file are self-consistent, which is exactly the case
# models_endpoint_is_trusted already treats as always-safe.
models_config_file_for_target() {
  case "$target" in
    codex)    printf '%s' "$codex_config_file" ;;
    opencode) printf '%s' "$opencode_config_file" ;;
    pi)       printf '%s' "$pi_models_file" ;;
  esac
}

models_key_file_order() {
  if [ "$scope" = "project" ] && [ -z "$install_dir" ]; then
    printf '%s\n%s\n%s\n' "$local_settings_file" "$settings_file" "$settings_dir/.weave-parked.json"
  else
    printf '%s\n%s\n%s\n' "$settings_file" "$local_settings_file" "$settings_dir/.weave-parked.json"
  fi
}

read_claude_installed_key() {
  local key="" candidate
  # Mirror where the install path writes the key: project scope (no --dir) puts
  # it in the gitignored settings.local.json, everything else inlines it into
  # settings.json. Check the other file too — a scope was possibly changed, or a
  # project checkout may carry a committed header from an older install.
  # The parked sidecar is last: `off` moves the key header out of the settings
  # files and into it, so a run while toggled off finds nothing above even
  # though the key is still on disk. Same {"env":{…}} shape, so read_claude_key
  # reads it directly.
  while IFS= read -r candidate; do
    [ -n "$candidate" ] || continue
    key="$(read_claude_key "$candidate")"
    [ -n "$key" ] && break
  done <<<"$(models_key_file_order)"
  printf '%s' "$key"
}

read_installed_key() {
  case "$target" in
    claude)   read_claude_installed_key ;;
    codex)    read_codex_key "$codex_config_file" ;;
    opencode) read_opencode_key "$opencode_config_file" ;;
    pi)       read_pi_key ;;
  esac
}

# resolve_installed_base_url prints the router endpoint this Claude Code install
# already points at, or nothing when none of its files carry a router-shaped
# one. The parked sidecar comes first: `off` moves the router URL there and
# leaves api.anthropic.com in the live file, so reading the live file while
# toggled off would report Anthropic as the router.
#
# models_base_file_order echoes that same precedence for callers that need to
# know which file supplied the endpoint (see models_key_file_order).
models_base_file_order() {
  printf '%s\n%s\n%s\n' "$settings_dir/.weave-parked.json" "$settings_file" "$local_settings_file"
}

resolve_installed_base_url() {
  local candidate found
  while IFS= read -r candidate; do
    [ -n "$candidate" ] || continue
    found="$(json_get "$candidate" '.env.ANTHROPIC_BASE_URL')"
    if router_shaped_url "$found"; then
      printf '%s' "${found%/}"
      return 0
    fi
  done <<<"$(models_base_file_order)"
  printf ''
}

# resolve_installed_endpoint prints the router endpoint this install already
# points at, for whichever client is being installed, or nothing. `update` uses
# it so a refresh never silently retargets a self-hosted install at the hosted
# default.
#
# The stored shapes differ by client and are normalized back to the root the
# installer takes on the command line: Codex and opencode hold an OpenAI-style
# base ending in /v1, while pi holds the bare root (its Anthropic SDK appends
# /v1/messages itself). Same tracked-file gate as the key readers — a config the
# repo supplied is not this user's install, whichever field is being read.
resolve_installed_endpoint() {
  local found=""
  case "$target" in
    claude)
      resolve_installed_base_url
      return 0
      ;;
    codex)
      if key_source_is_own "$codex_config_file"; then
        found="$(awk -v begin="$WEAVE_CODEX_BEGIN_MARKER" -v end="$WEAVE_CODEX_END_MARKER" '
          $0 == begin { inblk = 1; next }
          $0 == end   { inblk = 0; next }
          inblk && match($0, /^[[:space:]]*base_url[[:space:]]*=[[:space:]]*"[^"]*"/) {
            line = substr($0, RSTART, RLENGTH)
            sub(/^.*=[[:space:]]*"/, "", line)
            sub(/"$/, "", line)
            print line
            exit
          }
        ' "$codex_config_file" 2>/dev/null || true)"
      fi
      found="${found%/v1}"
      ;;
    opencode)
      if key_source_is_own "$opencode_config_file"; then
        found="$(json_get "$opencode_config_file" '.provider.weave.options.baseURL')"
      fi
      found="${found%/v1}"
      ;;
    pi)
      if key_source_is_own "$pi_models_file"; then
        found="$(json_get "$pi_models_file" '.providers.weave.baseUrl')"
      fi
      ;;
  esac
  printf '%s' "${found%/}"
}

# models_endpoint_is_trusted returns 0 when it is safe to send the router key
# resolved from $2 to the endpoint resolved from $1.
#
# Project scope deliberately splits the two: the endpoint lives in the
# committed settings.json (teammates share it) while each teammate's key lives
# in the gitignored settings.local.json. That split is fine on its own, but it
# means a *hostile* repo can commit a settings.json naming an endpoint it
# controls and have this command mail the developer's key to it. Endpoint and
# credential from the same file are self-consistent and always fine. When they
# differ, the endpoint must be one the user vouched for out-of-band — the
# hosted default, or an explicit --base-url — rather than whatever the checkout
# happened to contain.
models_endpoint_is_trusted() {
  local url="$1" base_src="$2" key_src="$3"
  [ "$base_url_explicit" = "true" ] && return 0
  [ "$url" = "$HOSTED_BASE_URL" ] && return 0
  [ -n "$base_src" ] && [ "$base_src" = "$key_src" ] && return 0
  # Project-scoped self-hosted installs intentionally split the endpoint and
  # key. The installer writes a gitignored marker beside the key; require that
  # marker to match before trusting the split. A tracked/symlinked local file is
  # not a teammate's private configuration and cannot vouch for an endpoint.
  if [ "$key_src" = "$local_settings_file" ] && [ ! -L "$key_src" ]; then
    local marked_url
    marked_url="$(json_get "$key_src" '.env.WEAVE_ROUTER_BASE_URL')"
    if [ "${marked_url%/}" = "${url%/}" ]; then
      command -v git >/dev/null 2>&1 || return 1
      git -C "$(dirname "$key_src")" ls-files --error-unmatch -- "$key_src" >/dev/null 2>&1 && return 1
      return 0
    fi
  fi
  # A repo can only pre-plant a file git tracks; an untracked local file is the
  # user's own. If the endpoint's file isn't tracked, it wasn't planted. Checked
  # inline rather than via weave_command_tracked_by_git, which is defined further
  # down (inside the statusline heredoc's neighborhood) and isn't in scope here.
  command -v git >/dev/null 2>&1 || return 1
  git -C "$(dirname "$base_src")" ls-files --error-unmatch -- "$base_src" >/dev/null 2>&1 || return 0
  return 1
}

# gitignore_add appends an entry to the repo .gitignore in project scope so a
# parked sidecar (which may carry the router key header) never gets committed.
# No-op for user scope and --dir, matching how install handles its own ignores.
gitignore_add() {
  [ "$scope" = "project" ] && [ -z "$install_dir" ] && [ -n "${git_root:-}" ] || return 0
  local gi="$git_root/.gitignore" entry="$1"
  refuse_if_symlink "$gi"
  if [ ! -f "$gi" ] || ! grep -qxF "$entry" "$gi"; then
    printf '%s\n' "$entry" >>"$gi"
  fi
}

toggle_claude() {
  local parked="$settings_dir/.weave-parked.json"
  local proj="false" active committed_base local_base parked_env merged
  if [ "$scope" = "project" ] && [ -z "$install_dir" ]; then
    proj="true"
    active="$local_settings_file"
  else
    active="$settings_file"
  fi
  # Symlink containment for the parked sidecar: project/--dir paths come from a
  # possibly-hostile repo, and `off` writes $parked via shell redirection (which
  # follows symlinks). A repo could pre-place it as a symlink to clobber an
  # arbitrary file or siphon the router-key-bearing parked data. The config
  # files themselves are already guarded during path resolution; this covers the
  # sidecar. User scope ($HOME) is trusted, matching the installer.
  if [ "$scope" = "project" ] || [ -n "$install_dir" ]; then
    refuse_if_symlink "$parked"
  fi
  local parked_present="false"
  if [ -f "$parked" ]; then parked_present="true"; fi
  committed_base="$(json_get "$settings_file" '.env.ANTHROPIC_BASE_URL')"

  case "$mode" in
    status)
      local on_hint="on --claude"
      [ "$proj" = "true" ] && on_hint="on --claude --scope project"
      if [ "$parked_present" = "true" ]; then
        ok "Claude Code: ${C_BOLD}off${C_RESET} — routing directly to Anthropic. Run '$on_hint' to re-enable."
      elif [ "$proj" = "true" ]; then
        local_base="$(json_get "$local_settings_file" '.env.ANTHROPIC_BASE_URL')"
        if [ -n "$local_base" ] && ! router_shaped_url "$local_base"; then
          ok "Claude Code (project): ${C_BOLD}off${C_RESET} — routing directly to Anthropic. Run '$on_hint' to re-enable."
        elif router_shaped_url "$committed_base"; then
          # Router URL is committed, but it only authenticates if this teammate's
          # settings.local.json carries the key header. A fresh clone (shared
          # settings.json, no personal local file) has the URL but no key.
          if claude_key_present "$local_settings_file"; then
            ok "Claude Code (project): ${C_BOLD}on${C_RESET} — routing through $committed_base."
          else
            warn "Claude Code (project): router URL is set but your personal router key is missing (no settings.local.json) — requests won't authenticate. Run the installer to add your key."
          fi
        else
          info "Claude Code (project): not configured for the router. Run the installer first."
        fi
      elif router_shaped_url "$committed_base"; then
        if claude_key_present "$settings_file"; then
          ok "Claude Code: ${C_BOLD}on${C_RESET} — routing through $committed_base."
        else
          warn "Claude Code: router URL is set but the router key header is missing — requests won't authenticate. Run the installer to restore it."
        fi
      else
        info "Claude Code: not configured for the router. Run the installer first."
      fi
      ;;
    off)
      if [ "$parked_present" = "true" ]; then
        ok "Claude Code is already off — nothing to do."
        return 0
      fi
      if ! router_shaped_url "$committed_base"; then
        info "Claude Code isn't configured for the router. Run the installer first."
        return 0
      fi
      if [ "$proj" = "true" ]; then
        # Park the whole local env (carries the key header), then override the
        # base URL to Anthropic in the local file only — committed settings.json
        # is never touched, so this stays out of `git diff`.
        if [ -f "$local_settings_file" ]; then
          jq '{env: (.env // {})}' "$local_settings_file" >"$parked"
          merged="$(jq '.env = ((.env // {} | del(.ANTHROPIC_CUSTOM_HEADERS)) + {ANTHROPIC_BASE_URL: "https://api.anthropic.com"})' "$local_settings_file")"
        else
          printf '{"env":{}}\n' >"$parked"
          merged='{"env":{"ANTHROPIC_BASE_URL":"https://api.anthropic.com"}}'
        fi
        printf '%s\n' "$merged" >"$local_settings_file"
        chmod 600 "$local_settings_file" "$parked"
        gitignore_add ".claude/.weave-parked.json"
      else
        # Park just the router-owned env keys, then strip them so Claude Code
        # falls back to its own Anthropic login.
        jq '{env: ((.env // {}) | {ANTHROPIC_BASE_URL, ANTHROPIC_CUSTOM_HEADERS, ENABLE_TOOL_SEARCH} | with_entries(select(.value != null)))}' "$settings_file" >"$parked"
        chmod 600 "$parked"
        merged="$(jq '(.env // {}) |= del(.ANTHROPIC_BASE_URL, .ANTHROPIC_CUSTOM_HEADERS, .ENABLE_TOOL_SEARCH)
                      | (if (.env // {} | length) == 0 then del(.env) else . end)' "$settings_file")"
        printf '%s\n' "$merged" >"$settings_file"
      fi
      ok "Claude Code is ${C_BOLD}off${C_RESET} (direct to Anthropic). Restart Claude Code for it to take effect."
      ;;
    on)
      if [ "$parked_present" != "true" ]; then
        # Project scope can still carry a direct-override in settings.local.json
        # even with no sidecar (e.g. the parked file was deleted by hand). That
        # override wins over the committed router URL, so traffic is really
        # going direct — drop it so "on" matches what "status" reports, instead
        # of falsely claiming we're already on.
        if [ "$proj" = "true" ]; then
          local_base="$(json_get "$local_settings_file" '.env.ANTHROPIC_BASE_URL')"
          if [ -n "$local_base" ] && ! router_shaped_url "$local_base"; then
            # We're off, but the parked sidecar is gone. The router key header
            # lives only in the local file / sidecar in project scope — never in
            # committed settings.json — so we can only re-enable cleanly if the
            # header survived in the local file. If it didn't, clearing the
            # override would point Claude Code at the router with no auth
            # (401s); leave the working direct setup in place and tell the user
            # to reinstall instead of faking success.
            if claude_key_present "$local_settings_file"; then
              merged="$(jq '(.env // {}) |= del(.ANTHROPIC_BASE_URL)
                            | (if (.env // {} | length) == 0 then del(.env) else . end)' "$local_settings_file")"
              printf '%s\n' "$merged" >"$local_settings_file"
              chmod 600 "$local_settings_file"
              ok "Claude Code is ${C_BOLD}on${C_RESET} (routing through the Weave Router). Restart Claude Code for it to take effect."
            else
              warn "Claude Code is off and the parked router key is missing (its sidecar was deleted). Re-run the installer to restore the router key — leaving the current direct-to-Anthropic setup in place so requests don't fail auth."
            fi
            return 0
          fi
        fi
        # No direct override (or user scope). "On" requires both the router URL
        # and the key header — a committed router URL with no local key (e.g. a
        # fresh clone) can't authenticate, so don't claim it's already on.
        if ! router_shaped_url "$committed_base"; then
          warn "No parked router config found. Run the installer to set up Claude Code."
        elif claude_key_present "$active"; then
          ok "Claude Code is already on — nothing to do."
        else
          # $active is settings.local.json in project scope, settings.json for
          # user/--dir — name the right file so the hint isn't misleading.
          warn "Router URL is set but the router key is missing — run the installer to add your key (written to $(basename "$active"))."
        fi
        return 0
      fi
      # Sidecar present: restore it — but only if the result actually carries
      # the router key. An off that ran with an empty/absent settings.local.json
      # parks {"env":{}}; blindly restoring that would drop the direct override
      # and leave the committed router URL unauthenticated while printing
      # success. Refuse that and tell the user to reinstall.
      parked_env="$(jq -c '.env // {}' "$parked")"
      parked_has_key="false"
      if printf '%s' "$parked_env" | jq -e '(.ANTHROPIC_CUSTOM_HEADERS // "") | test("X-Weave-Router-Key")' >/dev/null 2>&1; then
        parked_has_key="true"
      fi
      if [ "$parked_has_key" != "true" ] && ! claude_key_present "$active"; then
        warn "Can't re-enable: the parked config has no router key (it was created without one). Re-run the installer to set up your router key — leaving the current direct-to-Anthropic setup in place."
        return 0
      fi
      merged="$(jq --argjson p "$parked_env" '.env = (((.env // {}) | del(.ANTHROPIC_BASE_URL)) + $p)' "$active")"
      printf '%s\n' "$merged" >"$active"
      [ "$proj" = "true" ] && chmod 600 "$active"
      rm -f "$parked"
      ok "Claude Code is ${C_BOLD}on${C_RESET} (routing through the Weave Router). Restart Claude Code for it to take effect."
      ;;
  esac
}

toggle_codex() {
  local f="$codex_config_file" state="absent" tmp
  if [ -f "$f" ]; then
    state="$(awk -v b="$WEAVE_CODEX_BEGIN_MARKER" -v e="$WEAVE_CODEX_END_MARKER" '
      $0==b{inblk=1; next}
      $0==e{inblk=0; next}
      inblk && /^[[:space:]]*model_provider[[:space:]]*=[[:space:]]*"weave"/ {st="on"}
      inblk && /^[[:space:]]*#[[:space:]]*model_provider[[:space:]]*=[[:space:]]*"weave"/ {if(st=="")st="off"}
      END{print (st==""?"absent":st)}
    ' "$f")"
  fi

  case "$mode" in
    status)
      case "$state" in
        on)  ok "Codex: ${C_BOLD}on${C_RESET} — routing through the Weave Router." ;;
        off) ok "Codex: ${C_BOLD}off${C_RESET} — using Codex's default provider. Run 'on --codex' to re-enable." ;;
        *)   info "Codex: not configured for the router. Run the installer first." ;;
      esac
      ;;
    off)
      if [ "$state" = "absent" ]; then info "Codex isn't configured for the router. Run the installer first."; return 0; fi
      if [ "$state" = "off" ]; then ok "Codex is already off — nothing to do."; return 0; fi
      tmp="$(mktemp -t weave-codex-toggle.XXXXXX)"
      awk -v b="$WEAVE_CODEX_BEGIN_MARKER" -v e="$WEAVE_CODEX_END_MARKER" '
        $0==b{inblk=1; print; next}
        $0==e{inblk=0; print; next}
        inblk && /^[[:space:]]*model_provider[[:space:]]*=[[:space:]]*"weave"[[:space:]]*$/ {
          print "# " $0 "  # weave-router: off (run on to re-enable)"; next
        }
        {print}
      ' "$f" >"$tmp" && mv "$tmp" "$f"
      chmod 600 "$f"
      if [ -f "$codex_status_file" ] && grep -Fq '<!-- weave-router managed codex status -->' "$codex_status_file"; then
        "$codex_status_file" --off >/dev/null 2>&1 || true
      fi
      ok "Codex is ${C_BOLD}off${C_RESET} (default provider). Takes effect on your next 'codex' run."
      ;;
    on)
      if [ "$state" = "absent" ]; then warn "No managed Weave block in $f. Run the installer to set up Codex."; return 0; fi
      if [ "$state" = "on" ]; then ok "Codex is already on — nothing to do."; return 0; fi
      tmp="$(mktemp -t weave-codex-toggle.XXXXXX)"
      awk -v b="$WEAVE_CODEX_BEGIN_MARKER" -v e="$WEAVE_CODEX_END_MARKER" '
        $0==b{inblk=1; print; next}
        $0==e{inblk=0; print; next}
        inblk && /^[[:space:]]*#[[:space:]]*model_provider[[:space:]]*=[[:space:]]*"weave"/ {
          print "model_provider = \"weave\""; next
        }
        {print}
      ' "$f" >"$tmp" && mv "$tmp" "$f"
      chmod 600 "$f"
      if [ -f "$codex_status_file" ] && grep -Fq '<!-- weave-router managed codex status -->' "$codex_status_file"; then
        "$codex_status_file" --on >/dev/null 2>&1 || true
      fi
      ok "Codex is ${C_BOLD}on${C_RESET} (routing through the Weave Router). Takes effect on your next 'codex' run."
      ;;
  esac
}

toggle_opencode() {
  local f="$opencode_config_file" parked="$opencode_dir/.weave-parked.json"
  local model="" has_weave="false" parked_present="false" on="false" restore_model merged
  # Symlink containment for the parked sidecar — `off` writes it via shell
  # redirection; a hostile project repo could pre-place it as a symlink. The
  # opencode.json itself is already guarded during path resolution.
  if [ "$scope" = "project" ] || [ -n "$install_dir" ]; then
    refuse_if_symlink "$parked"
  fi
  if [ -f "$parked" ]; then parked_present="true"; fi
  if [ -f "$f" ]; then
    model="$(jq -r '.model // empty' "$f" 2>/dev/null || true)"
    if [ "$(jq -r '((.provider // {}) | has("weave"))' "$f" 2>/dev/null || true)" = "true" ]; then has_weave="true"; fi
  fi
  # A managed model counts as router-on: the Responses-shaped `weave/…` default,
  # or a legacy `weave-codex/…` model parked by a pre-upgrade install.
  case "$model" in weave/* | weave-codex/*) on="true" ;; esac

  case "$mode" in
    status)
      if [ "$on" = "true" ]; then
        ok "opencode: ${C_BOLD}on${C_RESET} — default model is $model (via the Weave Router)."
      elif [ "$has_weave" = "true" ] || [ "$parked_present" = "true" ]; then
        ok "opencode: ${C_BOLD}off${C_RESET} — not using the Weave Router model. Run 'on --opencode' to re-enable."
      else
        info "opencode: not configured for the router. Run the installer first."
      fi
      ;;
    off)
      if [ "$on" != "true" ]; then
        if [ "$has_weave" = "true" ]; then ok "opencode is already off — nothing to do."; else info "opencode isn't configured for the router. Run the installer first."; fi
        return 0
      fi
      jq '{model: .model}' "$f" >"$parked"
      chmod 600 "$parked"
      merged="$(jq 'del(.model)' "$f")"
      printf '%s\n' "$merged" >"$f"
      chmod 600 "$f"
      gitignore_add ".weave-parked.json"
      ok "opencode is ${C_BOLD}off${C_RESET} — pick a non-Weave model with /models. Takes effect on your next opencode run."
      ;;
    on)
      if [ "$on" = "true" ]; then ok "opencode is already on — nothing to do."; return 0; fi
      restore_model="weave/auto"
      if [ "$parked_present" = "true" ]; then
        restore_model="$(jq -r '.model // "weave/auto"' "$parked")"
      elif [ "$has_weave" != "true" ]; then
        warn "opencode isn't configured for the router. Run the installer first."; return 0
      else
        # No parked model (sidecar deleted by hand). Derive the default from the
        # installed provider.weave.models block rather than a hardcoded literal
        # that silently diverges when the installer's default changes — prefer
        # the auto-routing entry, else the first model the installer registered.
        restore_model="$(jq -r '
          (.provider.weave.models // {} | keys) as $k
          | (([$k[] | select(. == "auto")] | first) // $k[0] // "auto")
          | "weave/" + .
        ' "$f" 2>/dev/null || echo "weave/auto")"
      fi
      # Restore only a registered `weave` model. Legacy installations may have
      # parked a pinned model, but a current install exposes only `weave/auto`.
      case "$restore_model" in
        weave/*)
          local model_id="${restore_model#weave/}"
          if ! jq -e --arg id "$model_id" '(.provider.weave.models // {}) | has($id)' "$f" >/dev/null 2>&1; then
            restore_model="weave/auto"
          fi
          ;;
        weave-codex/*)
          if [ "$(jq -r '((.provider // {}) | has("weave-codex"))' "$f" 2>/dev/null || true)" != "true" ]; then
            restore_model="weave/auto"
          fi
          ;;
      esac
      merged="$(jq --arg m "$restore_model" '.model = $m' "$f")"
      printf '%s\n' "$merged" >"$f"
      chmod 600 "$f"
      rm -f "$parked"
      ok "opencode is ${C_BOLD}on${C_RESET} (default model $restore_model via the Weave Router). Takes effect on your next opencode run."
      ;;
  esac
}

# ---------- model selection ----------
#
# `models` reads and edits the installation's model/provider selection through
# the router's /admin/v1 API — the same lists the router dashboard's settings
# page renders, backed by the same columns. Nothing local is written: the
# endpoint and router key are read out of the install already on disk so a
# self-hosted install talks to its own router with its own key.
#
# The Weave-hosted router runs in `managed` mode and mounts no /admin/v1 at
# all — there model selection belongs to the organization and is edited in the
# Weave dashboard. That surfaces as a 404, which reads (for a list) as "fall
# back to the public catalog and say where to edit" and (for an edit) as a
# refusal naming the dashboard.

# Public, unauthed catalog of everything the router can route to. Mounted in
# both deployment modes, so it is the one listing that always works.
MODELS_CATALOG_PATH="/v1/router/models"
MODELS_DASHBOARD_URL="https://router.workweave.ai/dashboard/settings"

# Set by models_api on every call.
models_http_status=""
models_http_body=""

# models_api METHOD PATH [JSON_BODY] — call the router and capture status+body.
# The key rides in on stdin (curl --header @-) rather than a -H argument so it
# never appears in the process arg list, which any other local user can read.
models_api() {
  local method="$1" path="$2" body="${3:-}"
  local out status=""
  out="$(mktemp)" || { err "Could not create a temp file."; exit 1; }
  if [ -n "$body" ]; then
    status="$(printf '%s: %s\n' "$router_key_header" "$api_key" \
      | curl -sS --max-time 20 -X "$method" \
             -H 'Content-Type: application/json' --data-binary "$body" \
             --header @- -o "$out" -w '%{http_code}' "$base_url$path" 2>/dev/null)" || status=""
  else
    status="$(printf '%s: %s\n' "$router_key_header" "$api_key" \
      | curl -sS --max-time 20 -X "$method" \
             --header @- -o "$out" -w '%{http_code}' "$base_url$path" 2>/dev/null)" || status=""
  fi
  models_http_status="$status"
  models_http_body="$(cat "$out" 2>/dev/null || true)"
  rm -f "$out"
  case "$models_http_status" in
    2*) return 0 ;;
    *)  return 1 ;;
  esac
}

# models_api_error prints the router's own `error` field, when it sent one.
models_api_error() {
  printf '%s' "$models_http_body" | jq -r '.error // empty' 2>/dev/null || true
}

# models_fail turns the status left by models_api into a message that says what
# to do next, then exits. $1 names the attempted operation.
models_fail() {
  local what="$1" detail
  detail="$(models_api_error)"
  case "$models_http_status" in
    ""|000)
      err "Could not reach the router at $base_url. Is it running?"
      ;;
    401|403)
      # 403 with a message is the ROUTER_EXCLUDED_MODELS/PROVIDERS env pin,
      # which is actionable on its own; a bare 401/403 is a key problem.
      if [ -n "$detail" ]; then
        err "$detail"
      else
        err "The router rejected this installation's key. Re-run 'npx @workweave/router --$target --rotate-key' to install a current one."
      fi
      ;;
    404)
      err "This router does not expose the model-selection API, so $what is not available here."
      printf "  %sWeave-hosted routers keep model selection with the organization — edit it at %s%s\n" \
        "$C_DIM" "$MODELS_DASHBOARD_URL" "$C_RESET" >&2
      printf "  %sSelf-hosted? Update the router to a build that serves /admin/v1/models.%s\n" \
        "$C_DIM" "$C_RESET" >&2
      ;;
    *)
      if [ -n "$detail" ]; then
        err "$what failed (HTTP $models_http_status): $detail"
      else
        err "$what failed (HTTP $models_http_status)."
      fi
      ;;
  esac
  exit 1
}

# models_render_list prints the [{model,provider,enabled}] payload as a
# provider-grouped checklist. The API sorts by provider then model, which is
# what group_by needs, and what keeps two runs comparable.
models_render_list() {
  local payload="$1" total enabled
  total="$(printf '%s' "$payload" | jq 'length')"
  enabled="$(printf '%s' "$payload" | jq '[.[] | select(.enabled)] | length')"
  printf "%s%sWeave Router models%s %s· %s%s\n" "$C_BOLD" "$C_BRAND" "$C_RESET" "$C_DIM" "$base_url" "$C_RESET"
  printf "%s%s of %s enabled%s\n\n" "$C_DIM" "$enabled" "$total" "$C_RESET"
  printf '%s' "$payload" | jq -r --arg on "$C_GREEN" --arg off "$C_DIM" --arg reset "$C_RESET" --arg bold "$C_BOLD" '
    group_by(.provider)[]
    | ($bold + .[0].provider + $reset),
      (.[] | if .enabled
             then "  " + $on + "[x]" + $reset + " " + .model
             else "  " + $off + "[ ] " + .model + $reset
             end)
  '
}

# models_render_providers prints the [{provider,enabled}] payload.
models_render_providers() {
  local payload="$1"
  printf "%s%sWeave Router providers%s %s· %s%s\n\n" "$C_BOLD" "$C_BRAND" "$C_RESET" "$C_DIM" "$base_url" "$C_RESET"
  printf '%s' "$payload" | jq -r --arg on "$C_GREEN" --arg off "$C_DIM" --arg reset "$C_RESET" '
    .[] | if .enabled
          then "  " + $on + "[x]" + $reset + " " + .provider
          else "  " + $off + "[ ] " + .provider + $reset
          end
  '
}

# models_print_preferred appends the priority ranking when one is set. Absent
# is the norm (the router picks per turn), so silence is the right output.
models_print_preferred() {
  models_api GET "/admin/v1/preferred-models" || return 0
  local list
  list="$(printf '%s' "$models_http_body" | jq -r '(.preferred // []) | join(" > ")' 2>/dev/null || true)"
  [ -n "$list" ] || return 0
  printf "\n%sPreferred order:%s %s\n" "$C_DIM" "$C_RESET" "$list"
}

# models_list_catalog is the read-only fallback for a router with no
# model-selection API: list what it can route to and say where to change it.
# Deliberately not rendered as a checklist — this endpoint reports the deployed
# catalog, not the installation's selection, so marking every row [x] would
# claim models are enabled that the dashboard may well have excluded.
#
# $1 selects what to print from the same catalog payload: "models" (the
# default) or "providers" — `models providers` hits this same 404 and must
# degrade the same way `models list` does rather than hard-failing, since both
# are read-only listing commands.
models_list_catalog() {
  local what="${1:-models}"
  models_api GET "$MODELS_CATALOG_PATH" || models_fail "listing $what"
  if [ "$models_json" = "true" ]; then
    if [ "$what" = "providers" ]; then
      printf '%s' "$models_http_body" | jq -c '[.models[].provider] | unique'
    else
      printf '%s\n' "$models_http_body"
    fi
    return 0
  fi
  printf "%s%sWeave Router %s%s %s· %s%s\n" "$C_BOLD" "$C_BRAND" "$what" "$C_RESET" "$C_DIM" "$base_url" "$C_RESET"
  printf "%severything this router can route to%s\n\n" "$C_DIM" "$C_RESET"
  if [ "$what" = "providers" ]; then
    printf '%s' "$models_http_body" | jq -r '[.models[].provider] | unique[] | "  " + .'
  else
    printf '%s' "$models_http_body" | jq -r --arg reset "$C_RESET" --arg bold "$C_BOLD" '
      .models | group_by(.provider)[]
      | ($bold + .[0].provider + $reset),
        (.[] | "  " + .model)
    '
  fi
  printf "\n%sThis router does not report which of them your installation has enabled:%s\n" "$C_DIM" "$C_RESET"
  printf "%sselection is an organization-wide setting here. See it, and change it, at%s\n" "$C_DIM" "$C_RESET"
  printf "%s%s%s\n" "$C_DIM" "$MODELS_DASHBOARD_URL" "$C_RESET"
}

models_list() {
  if ! models_api GET "/admin/v1/models"; then
    # Only a missing API is worth degrading for; anything else is a real error.
    [ "$models_http_status" = "404" ] || models_fail "listing models"
    models_list_catalog models
    return 0
  fi
  if [ "$models_json" = "true" ]; then
    printf '%s\n' "$models_http_body"
    return 0
  fi
  models_render_list "$models_http_body"
  models_print_preferred
  printf "\n%sEnable a model:%s  npx @workweave/router models enable <id> --%s\n" "$C_DIM" "$C_RESET" "$target"
  printf "%sDisable a model:%s npx @workweave/router models disable <id> --%s\n" "$C_DIM" "$C_RESET" "$target"
}

models_providers_list() {
  if ! models_api GET "/admin/v1/providers"; then
    # Same 404 degrade as models_list: a router with no model-selection API
    # still answers the unauthed catalog, and this is a read-only listing
    # command same as `models list` — no reason to hard-fail one and not
    # the other.
    [ "$models_http_status" = "404" ] || models_fail "listing providers"
    models_list_catalog providers
    return 0
  fi
  if [ "$models_json" = "true" ]; then
    printf '%s\n' "$models_http_body"
    return 0
  fi
  models_render_providers "$models_http_body"
}

# models_toggle KIND ACTION IDS — flip one or more models/providers on or off.
# Each id is its own request so a typo in the third id can't roll back the two
# that already applied, and so the report names exactly what changed.
models_toggle() {
  local kind="$1" action="$2" ids="$3"
  local path body id label doing
  case "$kind" in
    model)    path="/admin/v1/excluded-models"    ; label="model"    ;;
    provider) path="/admin/v1/excluded-providers" ; label="provider" ;;
  esac
  # Enabling means dropping the id from the exclusion list, disabling means
  # adding it — the API's remove/add endpoints, inverted here so the CLI reads
  # the way the dashboard's checkboxes do.
  doing="disabling"
  if [ "$action" = "enable" ]; then
    doing="enabling"
    path="$path/remove"
  fi

  # Fed by a here-string, not a pipe: a pipe would run the loop in a subshell
  # where models_fail's exit only kills that subshell.
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    body="$(jq -nc --arg v "$id" --arg k "$label" '{($k): $v}')"
    if ! models_api POST "$path" "$body"; then
      models_fail "$doing $label '$id'"
    fi
    # Under --json the router's own response body is the output: a scripted
    # caller parses stdout, and printing prose there makes it report a parse
    # failure for a mutation that already succeeded.
    if [ "$models_json" = "true" ]; then
      printf '%s\n' "$models_http_body"
    elif [ "$action" = "enable" ]; then
      printf "%s✓%s %s %s%s%s is enabled\n" "$C_GREEN" "$C_RESET" "$label" "$C_BOLD" "$id" "$C_RESET"
    else
      printf "%s✓%s %s %s%s%s is disabled — the router will not pick it\n" "$C_GREEN" "$C_RESET" "$label" "$C_BOLD" "$id" "$C_RESET"
    fi
  done <<<"$ids"
}

# models_prefer replaces the priority ranking outright (the API's PUT), because
# a ranking is an ordering: appending one id at a time can't express "this one
# first". `clear` drops it and returns the installation to plain routing.
models_prefer() {
  local ids="$1" body stored
  if [ "$ids" = "clear" ] || [ "$ids" = "none" ]; then
    body='{"preferred":[]}'
  else
    body="$(printf '%s' "$ids" | jq -c -R -s '{preferred: (split("\n") | map(select(length > 0)))}')"
  fi
  models_api PUT "/admin/v1/preferred-models" "$body" || models_fail "setting the preferred-model ranking"
  if [ "$models_json" = "true" ]; then
    printf '%s\n' "$models_http_body"
    return 0
  fi
  stored="$(printf '%s' "$models_http_body" | jq -r '(.preferred // []) | join(" > ")')"
  if [ -n "$stored" ]; then
    printf "%s✓%s Preferred order: %s\n" "$C_GREEN" "$C_RESET" "$stored"
  else
    printf "%s✓%s Preferred-model ranking cleared.\n" "$C_GREEN" "$C_RESET"
  fi
}

models_usage() {
  # Echo back the client the caller actually named, so a Codex user is never
  # told to run a command that would target (or create) a Claude Code install.
  local c="--${target}"
  err "$1"
  printf '%s\n' \
    "  npx @workweave/router models $c                          # list models" \
    "  npx @workweave/router models enable  <id> [<id>…] $c" \
    "  npx @workweave/router models disable <id> [<id>…] $c" \
    "  npx @workweave/router models providers $c                # list providers" \
    "  npx @workweave/router models providers disable <name> $c" \
    "  npx @workweave/router models prefer <id> [<id>…] $c      # ranking ('clear' to drop)" >&2
  exit 2
}

run_models() {
  local verb operands
  verb="$(printf '%s' "$models_args" | head -n 1)"
  operands="$(printf '%s' "$models_args" | tail -n +2)"

  # `models providers …` shifts one more word: the sub-verb is the second word.
  case "$verb" in
    ""|list)
      [ -z "$operands" ] || models_usage "'models $verb' takes no arguments."
      models_list
      ;;
    enable|disable)
      [ -n "$operands" ] || models_usage "'models $verb' needs at least one model id."
      models_toggle model "$verb" "$operands"
      ;;
    prefer)
      [ -n "$operands" ] || models_usage "'models prefer' needs model ids, or 'clear'."
      models_prefer "$operands"
      ;;
    providers)
      local sub rest
      sub="$(printf '%s' "$operands" | head -n 1)"
      rest="$(printf '%s' "$operands" | tail -n +2)"
      case "$sub" in
        ""|list)
          models_providers_list
          ;;
        enable|disable)
          [ -n "$rest" ] || models_usage "'models providers $sub' needs at least one provider name."
          models_toggle provider "$sub" "$rest"
          ;;
        *)
          models_usage "Unknown providers sub-command: '$sub'."
          ;;
      esac
      ;;
    *)
      models_usage "Unknown models sub-command: '$verb'."
      ;;
  esac
}

if [ "$mode" = "models" ]; then
  # Which file supplied the endpoint / the key. Empty means "not from a settings
  # file" — an explicit --base-url, or WEAVE_ROUTER_KEY. Initialized before the
  # branches below so `set -u` holds on every path.
  models_base_source=""
  models_key_source=""
  # Editing model selection needs the endpoint and key of one specific install,
  # never the hosted defaults: a self-hosted user pointing at their own router
  # would otherwise silently edit the hosted one's installation.
  if [ "$base_url_explicit" != "true" ]; then
    models_base="$(resolve_installed_endpoint)"
    if [ -z "$models_base" ]; then
      err "No Weave Router install found for $target in this scope. Run 'npx @workweave/router --$target' first, or pass --base-url."
      exit 1
    fi
    base_url="$models_base"
    # resolve_installed_endpoint ran in a command substitution, so any global it
    # set died with that subshell. Recover the source by walking the same
    # precedence to find which file holds the endpoint we just adopted. Only
    # Claude Code spreads its endpoint across several files; every other client
    # keeps endpoint and key in one managed config, which is self-consistent by
    # construction (see models_endpoint_is_trusted's same-file rule).
    if [ "$target" = "claude" ]; then
      while IFS= read -r models_src_candidate; do
        [ -n "$models_src_candidate" ] || continue
        models_src_url="$(json_get "$models_src_candidate" '.env.ANTHROPIC_BASE_URL')"
        if [ "${models_src_url%/}" = "$models_base" ]; then
          models_base_source="$models_src_candidate"
          break
        fi
      done <<<"$(models_base_file_order)"
    else
      models_base_source="$(models_config_file_for_target)"
    fi
  fi
  # WEAVE_ROUTER_KEY is the user's own choice, so it pairs with any endpoint.
  if [ -n "${WEAVE_ROUTER_KEY:-}" ]; then
    api_key="$WEAVE_ROUTER_KEY"
    models_key_source="env:WEAVE_ROUTER_KEY"
  else
    api_key="$(read_installed_key)"
    # Same subshell caveat as the endpoint above: recover which file the key
    # came from by re-reading them in read_installed_key's own precedence.
    models_key_source=""
    if [ "$target" = "claude" ]; then
      while IFS= read -r models_src_candidate; do
        [ -n "$models_src_candidate" ] || continue
        if [ -n "$(read_claude_key "$models_src_candidate")" ]; then
          models_key_source="$models_src_candidate"
          break
        fi
      done <<<"$(models_key_file_order)"
    else
      models_key_source="$(models_config_file_for_target)"
    fi
  fi
  if [ -z "$api_key" ]; then
    err "No router key found for $target in this scope. Re-run 'npx @workweave/router --$target', or export WEAVE_ROUTER_KEY."
    exit 1
  fi
  # Never send a key to an endpoint the checkout supplied. See
  # models_endpoint_is_trusted: a hostile repo can commit a settings.json naming
  # its own router, and pairing that with the teammate key from the gitignored
  # settings.local.json would hand the key to whoever wrote the repo.
  if [ "$models_key_source" != "env:WEAVE_ROUTER_KEY" ] \
     && ! models_endpoint_is_trusted "$base_url" "$models_base_source" "$models_key_source"; then
    err "Refusing to send this installation's router key to $base_url."
    printf "  %sThat endpoint comes from %s, which git tracks — a checked-out repo can set it —%s\n" \
      "$C_DIM" "${models_base_source##*/}" "$C_RESET" >&2
    printf "  %swhile the key comes from %s. Pass --base-url <url> to confirm the endpoint,%s\n" \
      "$C_DIM" "${models_key_source##*/}" "$C_RESET" >&2
    printf "  %sor re-run 'npx @workweave/router --claude' to install against the one you want.%s\n" \
      "$C_DIM" "$C_RESET" >&2
    exit 1
  fi
  run_models
  exit 0
fi

if [ "$mode" != "install" ] && [ "$mode" != "update" ]; then
  case "$target" in
    claude)   toggle_claude ;;
    codex)    toggle_codex ;;
    opencode) toggle_opencode ;;
  esac
  exit 0
fi

# ---------- endpoint carry-over for update ----------
#
# `update` refreshes an install in place, so it must not silently retarget a
# self-hosted or custom endpoint at the hosted default just because --base-url
# wasn't repeated. Read the endpoint already configured and keep it; an explicit
# --base-url / --local still wins, and install mode is untouched (it is how you
# deliberately move an install to a new endpoint).
#
# `off` parks the router URL and points Claude Code at Anthropic, so the parked
# sidecar is the authority while toggled off — reading the live file there would
# pin the install to api.anthropic.com.
if [ "$mode" = "update" ] && [ "$base_url_explicit" != "true" ]; then
  installed_base="$(resolve_installed_endpoint)"
  if [ -n "$installed_base" ]; then
    base_url="${installed_base%/}"
  fi
fi

if [ "$mode" = "install" ]; then
  [ "$quiet" = "true" ] || info "scope=${C_BOLD}${scope}${C_RESET}  target=${C_BOLD}${target}${C_RESET}  base_url=${C_BOLD}${base_url}${C_RESET}"
else
  [ "$quiet" = "true" ] || info "mode=${C_BOLD}update${C_RESET}  scope=${C_BOLD}${scope}${C_RESET}  target=${C_BOLD}${target}${C_RESET}  base_url=${C_BOLD}${base_url}${C_RESET}"
fi

# ---------- token handling ----------
#
# Resolution order: WEAVE_ROUTER_KEY, then the key this install already wrote to
# disk, then an interactive prompt. Reading the installed key back is what makes
# a re-run painless — the installer refreshes assets and config shape often
# enough that users re-run it routinely, and re-pasting a key every time is pure
# friction. --rotate-key skips the read-back so a new key can replace the old.
# read_installed_key itself is defined above, next to the other settings readers.

# prompt_for_key reads a key from the controlling terminal into $api_key. Only
# ever called on an interactive install; callers check first.
prompt_for_key() {
  # Read from /dev/tty explicitly so the prompt works under `curl -fsSL ... | sh`,
  # where stdin is the curl pipe (already at EOF by the time we get here, and
  # `set -e` would abort on read returning 1).
  # _spin_cleanup (installed globally above) already restores stty echo on
  # any exit path, so we don't need a separate trap here — that would
  # overwrite the spinner cleanup and leak the cursor / child PID on Ctrl-C.
  printf "%sGet your Weave Router API key at %s%s\n" "$C_BRAND" "$base_url" "$C_RESET"
  printf "%sPaste your key here (rk_…):%s " "$C_DIM" "$C_RESET"
  stty -echo </dev/tty 2>/dev/null || true
  read -r api_key </dev/tty
  stty echo </dev/tty 2>/dev/null || true
  printf "\n"
  api_key_source="prompt"
  [ -n "$api_key" ] || { err "No key provided."; exit 1; }
}

api_key=""
installed_key=""
[ "$rotate_key" = "true" ] || installed_key="$(read_installed_key)"

if [ -n "${WEAVE_ROUTER_KEY:-}" ]; then
  api_key="$WEAVE_ROUTER_KEY"
  api_key_source="env"
  info "Using WEAVE_ROUTER_KEY from environment."
elif [ -n "$installed_key" ]; then
  api_key="$installed_key"
  api_key_source="disk"
  [ "$quiet" = "true" ] || info "Reusing the router key already installed (pass --rotate-key to replace it)."
elif [ "$mode" = "update" ]; then
  err "No router key found for $target. Export WEAVE_ROUTER_KEY, or run the installer once to set one up."
  exit 1
elif [ "$non_interactive" = "true" ]; then
  if [ "$rotate_key" = "true" ]; then
    err "--rotate-key needs either a prompt or WEAVE_ROUTER_KEY, and neither is available. Export the new key and re-run."
  else
    err "--non-interactive set but WEAVE_ROUTER_KEY is empty and no installed key was found. Export it and re-run."
  fi
  exit 1
else
  # If /dev/tty isn't available (e.g. CI without a controlling terminal) the
  # user must use --non-interactive.
  if [ ! -r /dev/tty ]; then
    err "No controlling terminal — set WEAVE_ROUTER_KEY and re-run with --non-interactive."
    exit 1
  fi
  prompt_for_key
fi

# ---------- identity (user email + name) ----------
#
# The router parses X-Weave-User-Email and X-Weave-User-Name on every protocol
# (Anthropic, OpenAI/Codex, Gemini) and persists them onto
# router.model_router_users so customers can attribute traffic to a person even
# when many people share one API key. We plant the headers at install time
# because Claude Code's metadata.user_id payload carries only account_uuid (no
# email), and Codex carries no identity at all — without this step the router
# only ever sees anonymous UUIDs for non-OTLP customers.
#
# Gate name on email: when the user explicitly opts out of email identity (via
# '-' at the prompt or by clearing git config), don't auto-plant a name from
# git config either. Opt-out should be all-or-nothing so the router
# consistently sees zero identity headers when the user wants to stay
# anonymous.
user_email="$(resolve_user_email)"
if [ -n "$user_email" ]; then
  user_name="$(resolve_user_name)"
else
  user_name=""
fi
if [ -n "$user_email" ] && [ -n "$user_name" ]; then
  ok "Will identify router traffic as $user_name <$user_email>"
elif [ -n "$user_email" ]; then
  ok "Will identify router traffic as $user_email"
else
  info "No identity set — router traffic will be attributed by account UUID only."
fi

# ---------- slash command wrappers (Claude Code and opencode) ----------
#
# Claude Code and opencode expand command Markdown locally rather than sending
# the literal invocation to the model, so they need wrappers that expand to a
# router directive.
#
# Layout:
#   Claude:  <settings_dir>/commands/{force-model,unforce-model}.md  → /force-model
#   opencode: <commands_dir>/{force-model,unforce-model}.md          → /force-model
#
# Codex is intentionally not listed here: its slash menu only exposes built-in
# commands and does not load these Markdown wrappers. Codex instead gets native
# `$name` skills (see install_codex_prompt_skills), each of which sends the
# leading-space prompt form (for example, ` /force-model gpt-5.6-terra`) that
# the router parses. Which client gets which directive comes from the embedded
# registry, not from a list here.
#
# Files come from install/commands/ in the repo (or the colocated commands/
# directory the npm package ships alongside install.sh).
install_slash_commands() {
  dst_dir="$1"
  commands_src_dir=""
  for candidate in \
    "$script_dir/commands" \
    "$script_dir/../commands"
  do
    if [ -d "$candidate" ]; then
      commands_src_dir="$candidate"
      break
    fi
  done
  [ -n "$commands_src_dir" ] || return 0

  if [ "$scope" = "project" ] || [ -n "$install_dir" ]; then
    refuse_if_symlink "$dst_dir"
  fi
  mkdir -p "$dst_dir"

  # force-model/unforce-model are router-intercepted prompt expansions for the
  # command-capable clients. The router-off/on/status wrappers shell out to
  # this installer to flip the *local* config, so they're Claude Code-only and
  # need the install scope baked into the command (the .md can't discover it
  # at invocation time). {{SCOPE}} is substituted accordingly. router-models
  # (alias models) is in the same boat: it shells out to read this install's
  # endpoint and key.
  cmds=""
  while IFS= read -r cmd; do
    [ -f "$commands_src_dir/$cmd.md" ] || continue
    case ",$cmd," in
      *,router-off,*|*,router-on,*|*,router-status,*|*,router-session,*|*,router-models,*|*,models,*)
        [ "$target" = "claude" ] || continue ;;
    esac
    [ -n "$cmds" ] && cmds="$cmds "
    cmds="$cmds$cmd"
  done <<EOF
$(weave_registry_names "$target")
EOF
  installed="$(printf '%s' "$cmds" | tr ' ' ',' | sed 's/,/, /g')"

  # Bake the same scope selector the toggle needs to find this install: --dir
  # overrides scope (mirrors install/uninstall path resolution), so a sandbox
  # install gets `--dir <path>` and the slash commands flip that dir rather
  # than the default user-scope paths. printf %q quotes paths with spaces.
  local scope_args=""
  if [ -n "$install_dir" ]; then
    scope_args=" --dir $(printf '%q' "$install_dir")"
  elif [ "$scope" = "project" ]; then
    scope_args=" --scope project"
  fi

  for cmd in $cmds; do
    src="$commands_src_dir/$cmd.md"
    dst="$dst_dir/$cmd.md"
    [ -f "$src" ] || continue
    if [ "$scope" = "project" ] || [ -n "$install_dir" ]; then
      refuse_if_symlink "$dst"
    fi
    # Substitute the {{SCOPE}} placeholder (only the router-* wrappers carry it;
    # cp-equivalent for the others since the token is absent).
    local body; body="$(cat "$src")"
    body="${body//\{\{SCOPE\}\}/$scope_args}"
    # Wrappers installed before ownership markers existed carry no marker. A
    # file whose body still matches what this installer would write is one of
    # ours from an older version, so adopt it rather than treating it as
    # user-owned — otherwise every pre-marker install is stranded: never
    # refreshed, never uninstalled, and skipped by the statusline refresh.
    # Anything that does not match byte-for-byte is left alone.
    if [ -e "$dst" ] && { [ ! -f "$dst" ] || ! grep -Fq "<!-- weave-router managed command: $cmd -->" "$dst"; }; then
      if [ -f "$dst" ] && [ "$(cat "$dst")" = "$body" ]; then
        : # an unmarked copy of our own wrapper; fall through and adopt it
      else
        warn "A user-owned $target command already exists at $dst; leaving it untouched."
        continue
      fi
    fi
    printf '%s\n<!-- weave-router managed command: %s -->' "$body" "$cmd" >"$dst"
  done
  seed_command_baseline "$commands_src_dir" "$cmds"
  ok "Slash commands written to $dst_dir ($installed)"
}

# seed_command_baseline records the canonical (unrendered) wrapper bodies this
# run installed, in the cache dir the statusline's background refresh reads. The
# refresh replaces an installed wrapper only when its bytes still match that
# baseline, which is how it tells "stale copy" from "user edited it" — without a
# seed it has to spend one whole interval establishing one. Claude Code only:
# the statusline is the refresh point and it only ever runs there.
#
# Keyed off the statusline's own location, exactly as the refresh derives it, so
# both sides land on the same cache path.
seed_command_baseline() {
  local src_dir="$1" names="$2"
  [ "$target" = "claude" ] || return 0
  local sl_dir cmd_dir
  sl_dir="$(cd "$(dirname "$statusline_file")" 2>/dev/null && pwd -P)" || return 0
  case "${sl_dir##*/}" in
    .weave)  cmd_dir="${sl_dir%/*}/.claude/commands" ;;
    .claude) cmd_dir="$sl_dir/commands" ;;
    *)       return 0 ;;
  esac
  local dir_slug
  dir_slug="$(printf '%s' "$cmd_dir" | tr -c 'A-Za-z0-9._-' '_')"
  local baseline_dir="${XDG_CACHE_HOME:-$HOME/.cache}/weave-router/commands${dir_slug}"
  mkdir -p "$baseline_dir" 2>/dev/null || return 0
  local name
  for name in $names; do
    [ -f "$src_dir/$name.md" ] || continue
    cp "$src_dir/$name.md" "$baseline_dir/$name.md" 2>/dev/null || true
  done
}

# Codex builds before custom prompt discovery existed were given wrapper files
# under ~/.codex/prompts. They were never invocable as the advertised aliases.
# Remove only byte-for-byte copies of our old wrappers; retain any prompt the
# user has changed or created independently.
remove_obsolete_codex_prompt_wrappers() {
  local dst_dir="$1"
  local commands_src_dir=""
  local candidate cmd src dst

  [ -d "$dst_dir" ] || return 0
  for candidate in \
    "$script_dir/commands" \
    "$script_dir/../commands"
  do
    if [ -d "$candidate" ]; then
      commands_src_dir="$candidate"
      break
    fi
  done
  [ -n "$commands_src_dir" ] || return 0

  if [ "$scope" = "project" ] || [ -n "$install_dir" ]; then
    refuse_if_symlink "$dst_dir"
  fi

  while IFS= read -r cmd; do
    src="$commands_src_dir/$cmd.md"
    dst="$dst_dir/$cmd.md"
    [ -f "$src" ] && [ -f "$dst" ] || continue
    [ -L "$dst" ] && continue
    if [ "$scope" = "project" ] || [ -n "$install_dir" ]; then
      refuse_if_symlink "$dst"
    fi
    cmp -s "$src" "$dst" && rm -f "$dst"
  done <<EOF
$(weave_registry_names codex)
EOF
  rmdir "$dst_dir" 2>/dev/null || true
}


# Install every Codex-native skill the registry declares for this client:
# prompt directives, their aliases, and local-config toggles like $router-off
# that shell out to this installer's own verbs. weave_registry_skill_assets is
# the union, canonical names plus aliases — Codex discovers skills by directory
# name, so an advertised $fm needs its own installed skill rather than a
# pointer to $force-model.
#
# A name with no SKILL.md is skipped: an alias may exist for the Claude command
# surface without a Codex skill behind it (registry_test.sh pins which ones do).
install_codex_prompt_skills() {
  local canonical candidate skill_src dst_dir dst_file emit_src emit_dst scope_args body
  while IFS= read -r canonical; do
    skill_src=""
    for candidate in \
      "$script_dir/codex-skills/$canonical/SKILL.md" \
      "$script_dir/../codex-skills/$canonical/SKILL.md"
    do
      [ -f "$candidate" ] || continue
      skill_src="$candidate"
      break
    done
    [ -n "$skill_src" ] || continue
    grep -Fq "<!-- weave-router managed $canonical skill -->" "$skill_src" || continue
    dst_dir="$codex_dir/skills/$canonical"
    dst_file="$dst_dir/SKILL.md"
    if [ "$scope" = "project" ] || [ -n "$install_dir" ]; then
      refuse_if_symlink "$codex_dir/skills"
      refuse_if_symlink "$dst_dir"
      refuse_if_symlink "$dst_file"
    elif [ -L "$codex_dir/skills" ] || [ -L "$dst_dir" ] || [ -L "$dst_file" ]; then
      warn "Codex skill path contains a symlink; leaving $canonical untouched."
      continue
    fi
    if [ -e "$dst_file" ] && { [ ! -f "$dst_file" ] || ! grep -Fq "<!-- weave-router managed $canonical skill -->" "$dst_file"; }; then
      warn "A user-owned Codex $canonical skill already exists at $dst_file; leaving it untouched."
      continue
    fi
    mkdir -p "$dst_dir"
    scope_args=""
    if [ -n "$install_dir" ]; then scope_args=" --dir $(printf '%q' "$install_dir")"
    elif [ "$scope" = project ]; then scope_args=" --scope project"; fi
    body="$(<"$skill_src")"
    body="${body//\{\{SCOPE\}\}/$scope_args}"
    printf '%s\n' "$body" >"$dst_file"
    # Prompt skills emit their directive through a script Codex execs; toggles
    # shell out to the installer's own verbs and ship none.
    emit_src="${skill_src%/SKILL.md}/scripts/emit.sh"
    if [ -f "$emit_src" ]; then
      mkdir -p "$dst_dir/scripts"
      emit_dst="$dst_dir/scripts/emit.sh"
      if [ "$scope" = "project" ] || [ -n "$install_dir" ]; then
        refuse_if_symlink "$dst_dir/scripts"
        refuse_if_symlink "$emit_dst"
      elif [ -L "$dst_dir/scripts" ] || [ -L "$emit_dst" ]; then
        warn "Codex skill script path contains a symlink; leaving $canonical emit.sh untouched."
      else
        cp "$emit_src" "$emit_dst"
        chmod +x "$emit_dst"
      fi
    fi
    ok "Codex skill installed: \$$canonical"
  done <<EOF
$(weave_registry_skill_assets codex)
EOF
}

# ---------- post-install verification (shared by every target) ----------
#
# Every client gets the same two probes — reach the router, then prove the key
# it was just handed actually authenticates — so a working install produces the
# same green checks regardless of which one was installed.

# validate_key asks the router whether $api_key is live. The key goes over stdin
# (`@-`) rather than a -H argument so it never appears in the process arg list,
# where `ps` / /proc would expose it to other local users on a shared machine.
# Wrapped in a function so the spinner's exec form sees a single command argv.
validate_key() {
  printf '%s: %s\n' "$router_key_header" "$api_key" \
    | curl -fsS --max-time 5 --header @- "$base_url/validate"
}

# rewrite_installed_key rewrites this target's managed config with the key in $1.
# Used by the rejected-key fallback below to swap in a replacement without
# re-running the whole install.
rewrite_installed_key() {
  local key="$1"
  case "$target" in
    claude)   write_claude_settings "$key" ;;
    codex)    write_codex_config "$codex_config_file" "$base_url" "$key" "$user_email" "$user_name" ;;
    opencode) write_opencode_config "$opencode_config_file" "$base_url" "$key" "$user_email" "$user_name" ;;
    pi)
      write_pi_models_config "$pi_models_file" "$base_url" "$key" "$user_email" "$user_name"
      printf '%s\n' "$key" >"$pi_key_file"
      chmod 600 "$pi_key_file"
      ;;
  esac
}

verify_install() {
  if [ "$quiet" != "true" ]; then
    if ! spin "Pinging $base_url/health" curl -fsS --max-time 5 "$base_url/health"; then
      warn "Could not reach $base_url/health within 5s. Settings are written; verify the router is running."
    fi
  fi

  [ -n "$api_key" ] || return 0

  spin "Validating API key" validate_key && return 0

  # A key we read back off disk can have been revoked or rotated since it was
  # installed, and reusing it silently would leave a broken install behind
  # exactly where the old behavior (always prompt) would have fixed it. Ask
  # once, then rewrite the config with whatever the user pastes. Anywhere a
  # prompt isn't available — non-interactive, update, --quiet, no tty, or a
  # key that didn't come from disk — keep the historical warn-and-continue.
  if [ "$api_key_source" = "disk" ] && [ "$non_interactive" != "true" ] \
     && [ "$quiet" != "true" ] && [ -r /dev/tty ]; then
    warn "The router key already installed was rejected (revoked or rotated)."
    prompt_for_key
    rewrite_installed_key "$api_key"
    if ! spin "Validating API key" validate_key; then
      warn "Router rejected the API key (check it matches the dashboard at $base_url)."
    fi
    return 0
  fi

  if [ "$mode" = "update" ]; then
    # update is meant for cron/scripting: a rejected key is a real failure,
    # not a note in the log. --quiet callers opted out of the noise, not out
    # of the nonzero exit — a scheduled run that reports success here would
    # hide a revoked key indefinitely.
    if [ "$quiet" = "true" ]; then
      warn "Router rejected the installed API key. Re-run the installer to set a new one."
    else
      err "Router rejected the installed API key. Re-run the installer to set a new one."
    fi
    exit 1
  fi

  warn "Router rejected the API key (check it matches the dashboard at $base_url)."
}

# announce_done prints the closing line for the client named in $1. `update`
# refreshed an existing install rather than creating one, so it says so and
# skips the uninstall hint — that run was never the user's first contact with
# the installer.
announce_done() {
  printf "\n"
  if [ "$mode" = "update" ]; then
    printf "%s✓%s %s%sWeave Router config refreshed for %s.%s\n" \
      "$C_GREEN" "$C_RESET" "$C_BOLD" "$C_BRAND" "$1" "$C_RESET"
    return 0
  fi
  printf "%s✓%s %s%sWeave Router installed for %s.%s\n" \
    "$C_GREEN" "$C_RESET" "$C_BOLD" "$C_BRAND" "$1" "$C_RESET"
}

# ---------- codex install path (dispatch + exit before the Claude-only writes) ----------

# Install the Codex lifecycle helper that reflects the active router and the
# latest known routed model in the terminal title. The embedded copy keeps the
# standalone curl installer feature-complete; packaged installs use the
# canonical sibling asset.
install_codex_status_script() {
  local candidate status_src=""
  for candidate in \
    "$script_dir/codex-status.sh" \
    "$script_dir/../codex-status.sh"
  do
    if [ -f "$candidate" ]; then
      status_src="$candidate"
      break
    fi
  done
  if [ "$scope" = "user" ] && [ -z "$install_dir" ]; then
    mkdir -p "$(dirname "$codex_status_file")"
  else
    refuse_if_symlink "$codex_status_file"
  fi
  if [ -e "$codex_status_file" ] && { [ ! -f "$codex_status_file" ] || ! grep -Fq '<!-- weave-router managed codex status -->' "$codex_status_file"; }; then
    warn "A user-owned Codex status helper already exists at $codex_status_file; leaving it untouched."
    return 1
  fi
  if [ -n "$status_src" ]; then
    grep -Fq '<!-- weave-router managed codex status -->' "$status_src" || {
      warn "Codex status helper has no ownership marker; leaving it unchanged."
      return 1
    }
    cp "$status_src" "$codex_status_file"
  else
    cat >"$codex_status_file" <<'CODEX_STATUS_EOF'
#!/usr/bin/env bash
# <!-- weave-router managed codex status -->
#
# Codex lifecycle hook for the Weave Router. Codex passes a JSON object on
# stdin; the Stop hook includes the last assistant message, which carries the
# router's routed-model marker when the selected model changes. The helper
# keeps the last known routed model per session and reflects it in the terminal
# title, so the active router remains visible between turns without injecting
# another message into the conversation.
#
# Savings come from the router, not from local arithmetic. Codex records its
# own requested model on every turn and never the served one, so the per-turn
# pricing the Claude Code statusline does cannot be reproduced here — it would
# price both sides of the comparison at the same model and report zero. The
# router already sums the real thing per session, so the hook fetches
# GET <base>/v1/sessions/<id>/cost and renders what it returns. The fetch runs
# in a detached subshell writing a cache the NEXT turn reads, so no turn ever
# blocks on the network, and every failure path leaves the title model-only.

set -euo pipefail

state_root="${XDG_CACHE_HOME:-$HOME/.cache}/weave-router/codex"
helper_dir="$(cd "$(dirname "$0")" 2>/dev/null && pwd -P)"
disabled_marker="$helper_dir/.weave-router-disabled"
router_badge_sentinel=$'⁣⁠⁣⁠'
# Must stay verbatim in sync with install.sh / uninstall.sh: the endpoint read
# below is scoped to this block so a key-shaped string elsewhere in the user's
# config.toml is never adopted.
codex_begin_marker="# >>> weave-router managed (do not edit between markers) >>>"
codex_end_marker="# <<< weave-router managed <<<"

emit_title() {
  local title="$1"
  if [ -n "${WEAVE_CODEX_STATUS_TITLE_FILE:-}" ]; then
    printf '%s\n' "$title" >"$WEAVE_CODEX_STATUS_TITLE_FILE"
  elif [ -w /dev/tty ]; then
    printf '\033]0;%s\007' "$title" >/dev/tty
  fi
}

safe_session_id() {
  local id="$1"
  case "$id" in
    ''|*[!A-Za-z0-9._-]*) return 1 ;;
  esac
  [ "${#id}" -le 128 ] || return 1
  printf '%s' "$id"
}

safe_display_value() {
  printf '%s' "$1" | sed 's/[^A-Za-z0-9._:\/-]//g' | cut -c1-128
}

state_file_for() {
  local id
  id="$(safe_session_id "$1")" || return 1
  printf '%s/%s.state' "$state_root" "$id"
}

read_state() {
  local file="$1" key value
  [ -f "$file" ] || return 0
  while IFS='=' read -r key value; do
    case "$key" in
      routed_model) routed_model="$value" ;;
    esac
  done <"$file"
}

write_state() {
  local file="$1" tmp
  mkdir -p "$state_root"
  chmod 700 "$state_root"
  tmp="$(mktemp "$state_root/.state.XXXXXX")"
  printf 'requested_model=%s\nrouted_model=%s\n' "$requested_model" "$routed_model" >"$tmp"
  chmod 600 "$tmp"
  mv "$tmp" "$file"
}

cost_file_for() {
  local id
  id="$(safe_session_id "$1")" || return 1
  printf '%s/%s.cost' "$state_root" "$id"
}

# Reads the router base URL and key out of the Codex config this install owns.
# Resolved from the helper's own location first so a project-scope install never
# reads (or leaks) the user-scope key: the project helper lives in the same
# .codex directory as its config, while the user-scope helper sits in ~/.weave
# and reads ~/.codex. Values are scoped to the managed block so a key-shaped
# string the user wrote elsewhere in the file is never adopted. awk, not a TOML
# parser, because the Codex target deliberately does not require jq for config
# reads.
read_codex_endpoint() {
  local config=""
  # Project/custom installs name the helper weave-status.sh and keep it next
  # to their config.toml. Falling through to ~/.codex would fetch another
  # installation's session cost with that key. The user-scope helper
  # (codex-status.sh in ~/.weave) has no adjacent config and owns HOME.
  if [ -f "$helper_dir/config.toml" ]; then
    config="$helper_dir/config.toml"
  elif [ "$(basename "$0")" != "weave-status.sh" ] && [ -f "$HOME/.codex/config.toml" ]; then
    config="$HOME/.codex/config.toml"
  fi
  [ -n "$config" ] || return 0
  awk -v begin="$codex_begin_marker" -v end="$codex_end_marker" '
    $0 == begin { inblk = 1; next }
    $0 == end   { inblk = 0; next }
    !inblk { next }
    match($0, /base_url[[:space:]]*=[[:space:]]*"[^"]*"/) {
      v = substr($0, RSTART, RLENGTH)
      sub(/^.*=[[:space:]]*"/, "", v); sub(/"$/, "", v)
      url = v
    }
    match($0, /"X-Weave-Router-Key"[[:space:]]*=[[:space:]]*"[^"]*"/) {
      v = substr($0, RSTART, RLENGTH)
      sub(/^.*=[[:space:]]*"/, "", v); sub(/"$/, "", v)
      key = v
    }
    END { if (url != "" && key != "") printf "%s\n%s\n", url, key }
  ' "$config" 2>/dev/null || true
}

# Kicks off a detached fetch of this session's committed cost. The result lands
# in a cache the next turn reads; this turn renders whatever is already there.
# Fire-and-forget on purpose — a slow or unreachable router must never stall a
# Codex turn, and every failure simply leaves the previous cache in place.
refresh_session_cost() {
  local id="$1" file="$2"
  [ "${WEAVE_CODEX_STATUS_SAVINGS:-1}" = "0" ] && return 0
  command -v curl >/dev/null 2>&1 || return 0

  local endpoint base_url key
  endpoint="$(read_codex_endpoint)" || return 0
  base_url="$(printf '%s' "$endpoint" | sed -n 1p)"
  key="$(printf '%s' "$endpoint" | sed -n 2p)"
  [ -n "$base_url" ] && [ -n "$key" ] || return 0

  mkdir -p "$state_root" 2>/dev/null || return 0
  chmod 700 "$state_root" 2>/dev/null || true

  (
    exec </dev/null
    # mkdir is the portable atomic test-and-set. A crashed holder would block
    # refreshes forever, so a lock older than the fetch timeout is reclaimed.
    lock="$file.lock"
    if ! mkdir "$lock" 2>/dev/null; then
      lock_mtime="$(stat -c %Y "$lock" 2>/dev/null || stat -f %m "$lock" 2>/dev/null)" || lock_mtime=0
      lock_now="$(date +%s 2>/dev/null)" || lock_now=0
      if [ "${lock_mtime:-0}" -le 0 ] || [ $(( lock_now - lock_mtime )) -le 30 ]; then
        exit 0
      fi
      rm -rf "$lock" 2>/dev/null
      mkdir "$lock" 2>/dev/null || exit 0
    fi
    trap 'rmdir "$lock" 2>/dev/null' EXIT

    # A file:// base is the offline/test seam: curl reads it as the response
    # body directly, so the endpoint path is meaningless for it.
    url="${base_url%/}"
    case "$url" in
      file://*) ;;
      *) url="${url%/v1}/v1/sessions/$id/cost" ;;
    esac
    body="$(curl -fsS --max-time 5 -H "X-Weave-Router-Key: $key" "$url" 2>/dev/null)" || exit 0
    # savings_usd is the router's own (requested - actual). A body without it
    # (404, error envelope, older router) writes nothing and leaves the cache.
    savings="$(printf '%s' "$body" | jq -r '.savings_usd // empty' 2>/dev/null)" || exit 0
    case "$savings" in
      ''|*[!0-9.eE+-]*) exit 0 ;;
    esac
    tmp="$file.tmp.$$"
    mkdir -p "$(dirname "$file")" 2>/dev/null
    if printf '%s' "$savings" >"$tmp" 2>/dev/null; then
      chmod 600 "$tmp" 2>/dev/null
      mv "$tmp" "$file" 2>/dev/null
    fi
    rm -f "$tmp" 2>/dev/null
  ) >/dev/null 2>&1 &
  disown 2>/dev/null || true
}

# Renders the cached savings as a display clause, or nothing. Values below a
# cent read as "<$0.01" rather than "$0.00", which would be indistinguishable
# from "the router ran and did not beat your selection". Negative totals are
# omitted entirely: the router picked a pricier model for quality on this
# session and "saved -$0.02" is a worse answer than staying quiet.
savings_clause() {
  local file="$1" raw
  [ "${WEAVE_CODEX_STATUS_SAVINGS:-1}" = "0" ] && return 0
  [ -f "$file" ] || return 0
  raw="$(cat "$file" 2>/dev/null)" || return 0
  case "$raw" in
    ''|*[!0-9.eE+-]*) return 0 ;;
  esac
  awk -v v="$raw" 'BEGIN{
    v = v + 0
    if (v < 0.005) { exit }
    if (v < 0.01)  { printf " · saved <$0.01"; exit }
    printf " · saved $%.2f", v
  }' 2>/dev/null || true
}

set -e

case "${1:-hook}" in
  --direct)
    emit_title "Codex · direct"
    exit 0
    ;;
  --on)
    rm -f "$disabled_marker"
    emit_title "Weave Router · active"
    exit 0
    ;;
  --off)
    [ ! -L "$disabled_marker" ] || exit 0
    : >"$disabled_marker"
    chmod 600 "$disabled_marker"
    emit_title "Codex · direct"
    exit 0
    ;;
esac

payload="$(cat)"
[ -n "$payload" ] || exit 0
command -v jq >/dev/null 2>&1 || exit 0
jq -e . >/dev/null 2>&1 <<<"$payload" || exit 0

hook_event_name="$(jq -r '.hook_event_name // ""' <<<"$payload")"
if [ "$hook_event_name" = "SessionStart" ]; then
  if [ -f "$disabled_marker" ]; then
    emit_title "Codex · direct"
    jq -cn '{systemMessage:"Codex direct · Weave Router is off"}'
  else
    emit_title "Weave Router · active"
    jq -cn '{systemMessage:"Weave Router active · routed model appears in the terminal title"}'
  fi
  exit 0
fi

if [ -f "$disabled_marker" ]; then
  exit 0
fi

requested_model="$(safe_display_value "$(jq -r '.model // ""' <<<"$payload")")"
routed_model=""
session_id="$(jq -r '.session_id // ""' <<<"$payload")"
last_assistant_message="$(jq -r '.last_assistant_message // ""' <<<"$payload")"

file=""
if file="$(state_file_for "$session_id" 2>/dev/null)"; then
  read_state "$file"
fi

# The marker is intentionally matched only at the beginning of the assistant
# message. Do not treat ordinary prose that mentions the heading as metadata.
first_line="${last_assistant_message%%$'\n'*}"
marker_model=""
force_model=""
case "$first_line" in
  "${router_badge_sentinel}✦ **Weave Router** → "*)
    marker_model="${first_line#"${router_badge_sentinel}✦ **Weave Router** → "}"
    marker_model="${marker_model%% ·*}"
    ;;
  "✦ **Weave Router** → "*)
    marker_model="${first_line#"✦ **Weave Router** → "}"
    marker_model="${marker_model%% ·*}"
    ;;
esac
case "$first_line" in
  "Weave Router: force-model applied: "*" ("*)
    force_model="${first_line#"Weave Router: force-model applied: "}"
    force_model="${force_model%% (*}"
    ;;
esac
if [ -n "$marker_model" ]; then
  routed_model="$(safe_display_value "$marker_model")"
elif [ -n "$force_model" ]; then
  routed_model="$(safe_display_value "$force_model")"
else
  routed_model="$(safe_display_value "$routed_model")"
fi

if [ -n "$file" ]; then
  write_state "$file"
fi

# The cache holds the previous turn's fetch; the refresh below serves the next
# one. Router telemetry is written asynchronously anyway, so a just-finished
# turn would not be included even in a blocking read — reading first and
# refreshing after costs a turn of freshness and buys never blocking Codex.
savings=""
cost_file=""
if cost_file="$(cost_file_for "$session_id" 2>/dev/null)"; then
  savings="$(savings_clause "$cost_file")"
  refresh_session_cost "$(safe_session_id "$session_id")" "$cost_file"
fi

if [ -n "$routed_model" ] && [ -n "$requested_model" ] && [ "$routed_model" != "$requested_model" ]; then
  title="Weave Router · $routed_model ← $requested_model$savings"
elif [ -n "$routed_model" ]; then
  title="Weave Router · $routed_model$savings"
elif [ -n "$requested_model" ]; then
  title="Weave Router · active ← $requested_model$savings"
else
  title="Weave Router · active$savings"
fi
emit_title "$title"
if [ -n "$marker_model" ] || [ -n "$force_model" ]; then
  printf '%s' "$title" | jq -Rc '{systemMessage: .}'
fi
CODEX_STATUS_EOF
  fi
  chmod 700 "$codex_status_file"
  "$codex_status_file" --on >/dev/null 2>&1 || true
  ok "Codex status integration installed at $codex_status_file"
}

if [ "$target" = "codex" ]; then
  if ! install_codex_status_script; then
    err "Cannot install the Codex status helper safely; refusing to write hooks that could execute unowned code."
    exit 1
  fi
  write_codex_config "$codex_config_file" "$base_url" "$api_key" "$user_email" "$user_name"
  ok "Codex config written to $codex_config_file"
  remove_obsolete_codex_prompt_wrappers "$codex_dir/prompts"
  install_codex_prompt_skills
  info "Codex router directives: begin the message with one space, e.g. ' /force-model gpt-5.6-terra'."

  # Project scope: ensure the per-teammate config (which holds the router key)
  # is gitignored. The base URL is the same for every teammate, so a
  # committed shared file would still leak the per-key portion. Easier to
  # ignore the whole config and have each teammate run the installer.
  if [ "$scope" = "project" ] && [ -z "$install_dir" ] && [ -n "${git_root:-}" ]; then
    gitignore="$git_root/.gitignore"
    refuse_if_symlink "$gitignore"
    for entry in \
      ".codex/config.toml" \
      ".codex/weave-status.sh" \
      ".codex/.weave-router-disabled"
    do
      if [ ! -f "$gitignore" ] || ! grep -qxF "$entry" "$gitignore"; then
        printf '%s\n' "$entry" >>"$gitignore"
      fi
    done
    ok "Updated $gitignore (ignored Codex router config and status helper)"
  fi

  verify_install

  announce_done "Codex"
  if [ "$scope" = "project" ] || [ -n "$install_dir" ]; then
    # Codex auto-discovers ~/.codex; for non-user installs the caller has to
    # point CODEX_HOME at the directory we wrote so Codex finds our config.
    info "Run Codex with CODEX_HOME=$codex_dir codex so it picks up this config."
  fi
  [ "$mode" = "update" ] || print_uninstall_hint
  exit 0
fi

# ---------- opencode install path (dispatch + exit before the Claude-only writes) ----------

if [ "$target" = "opencode" ]; then
  write_opencode_config "$opencode_config_file" "$base_url" "$api_key" "$user_email" "$user_name"
  ok "opencode config written to $opencode_config_file"

  # Project scope: the per-teammate config carries the router key, so it
  # stays out of git. Same reasoning as the Codex path — base URL is shared,
  # but the key is per-person. The .weave/ plugin dir is per-person too (the
  # config references it by absolute path), so ignore it alongside.
  if [ "$scope" = "project" ] && [ -z "$install_dir" ] && [ -n "${git_root:-}" ]; then
    gitignore="$git_root/.gitignore"
    refuse_if_symlink "$gitignore"
    for entry in \
      "opencode.json" \
      ".weave/"
    do
      if [ ! -f "$gitignore" ] || ! grep -qxF "$entry" "$gitignore"; then
        printf '%s\n' "$entry" >>"$gitignore"
      fi
    done
    ok "Updated $gitignore (ignored opencode.json, .weave/)"
  fi

  # Slash command wrappers: opencode discovers commands in *.md files under
  # ~/.config/opencode/commands/ (user scope) and .opencode/commands/ (project
  # scope). Install command wrappers for /rf (±), /force-model, /unforce-model
  # so typing /rf + in the TUI expands to /router-feedback + and reaches the
  # router's server-side feedback interceptor, same as Claude Code + Codex.
  #
  # Router on/off/status are not installed — they run npx shell commands
  # specific to the Claude Code settings model and don't apply to opencode.
  if [ "$scope" = "project" ]; then
    opencode_commands_dir="$opencode_dir/.opencode/commands"
  else
    # User scope and --dir installs: use the global commands path that opencode
    # discovers regardless of working directory.
    opencode_commands_dir="${XDG_CONFIG_HOME:-$HOME/.config}/opencode/commands"
  fi
  install_slash_commands "$opencode_commands_dir"
  if [ "$scope" = "user" ] || [ -n "$install_dir" ]; then
    info "opencode restart required for commands to take effect."
  fi

  verify_install

  announce_done "opencode"
  # Surface the optional subscription-routing path only when this run actually
  # registered the weave-claude login provider (which is written together with
  # the plugin). Gate on its presence in the written config (authoritative)
  # rather than a plugin file on disk — a leftover plugin from a prior install
  # can outlive a plugin-less re-install that stripped the provider, which would
  # make these instructions misleading.
  if jq -e '(.provider // {}) | has("weave-claude")' "$opencode_config_file" >/dev/null 2>&1; then
    info "Optional: connect your AI plans so they pay for the matching turns. Run ${C_BOLD}opencode auth login${C_RESET} → ${C_BOLD}Weave Router${C_RESET} for ${C_BOLD}ChatGPT Pro/Plus${C_RESET} (GPT/Codex turns) and/or ${C_BOLD}Weave Router — Claude plan${C_RESET} for ${C_BOLD}Claude Pro/Max${C_RESET} (Claude turns). The router still routes every turn; your Weave key pays for the rest."
  fi
  if [ -n "$install_dir" ]; then
    # --dir installs land outside opencode's discovery roots, so the caller
    # has to point opencode at the file explicitly.
    info "Run opencode with OPENCODE_CONFIG=$opencode_config_file opencode."
  fi
  [ "$mode" = "update" ] || print_uninstall_hint
  exit 0
fi

# ---------- pi install path (dispatch + exit before the Claude-only writes) ----------

if [ "$target" = "pi" ]; then
  write_pi_models_config "$pi_models_file" "$base_url" "$api_key" "$user_email" "$user_name"
  ok "pi models config written to $pi_models_file"
  write_pi_settings_config "$pi_settings_file"
  ok "pi settings written to $pi_settings_file (provider weave + @workweave/router)"

  if [ -n "$api_key" ]; then
    printf '%s\n' "$api_key" >"$pi_key_file"
    chmod 600 "$pi_key_file"
    ok "Router key written to $pi_key_file"
  fi

  # Project scope: the repo-local .pi carries the router key, so keep it out of
  # git. Same reasoning as the Codex/opencode paths — base URL is shared, the
  # key is per-person.
  if [ "$scope" = "project" ] && [ -z "$install_dir" ] && [ -n "${git_root:-}" ]; then
    # Write the .gitignore in the directory that CONTAINS .pi (the chosen project
    # dir), not the git root: gitignore entries with a slash are anchored to the
    # .gitignore's own location, so a root-level ".pi/models.json" would NOT match
    # a nested <subdir>/.pi/ — leaking the router key. dirname "$pi_dir" == the
    # project dir (== git root when they're the same).
    gitignore="$(dirname "$pi_dir")/.gitignore"
    refuse_if_symlink "$gitignore"
    for entry in \
      ".pi/models.json" \
      ".pi/settings.json" \
      ".pi/.weave_router_key"
    do
      if [ ! -f "$gitignore" ] || ! grep -qxF "$entry" "$gitignore"; then
        printf '%s\n' "$entry" >>"$gitignore"
      fi
    done
    ok "Updated $gitignore (ignored repo-local .pi router config)"
  fi

  verify_install

  if [ -n "$lsp_langs" ]; then
    install_lsp_servers "$lsp_langs"
  fi

  announce_done "pi"
  # Billing note: pi normally draws on a Claude subscription (OAuth); routing
  # through the router switches to per-token billing on the router deployment
  # key (or BYOK). Surface it at install so the change isn't a surprise.
  info "pi bills per token on the Weave Router key, not your Claude subscription."
  if [ "$scope" = "project" ] || [ -n "$install_dir" ]; then
    info "Run pi with PI_CODING_AGENT_DIR=$pi_dir pi so it picks up this config."
  fi
  [ "$mode" = "update" ] || print_uninstall_hint
  exit 0
fi

# ---------- write the statusline script ----------

cat > "$statusline_file" << 'STATUSLINE_EOF'
#!/usr/bin/env bash
#
# Claude Code statusline for the Weave Router. CC pipes a JSON blob on stdin
# whose `transcript_path` points at the JSONL log of the current session and
# whose `model.display_name` is the user's CC-side model selection. The
# router rewrites each request's `model` field before forwarding, so
# Anthropic/OpenAI/Google return `message.model = <routed>` in the SSE
# stream and CC stores that in the transcript verbatim. Per-turn savings
# come from comparing each turn's routed cost against what the user's
# selection would have cost on the same tokens. Works identically for
# local docker and the managed cloud router — no sidecar, no DB, no auth.
#
# Wire up by adding to ~/.claude/settings.json:
#   { "statusLine": { "type": "command", "command": "/abs/path/to/cc-statusline.sh" } }
#
# Renders:
#   WEAVE ROUTER — claude-sonnet-4-5 ← claude-opus-4-7 · saved $1.23 · 12.4k in / 3.1k out / 45.2k cached
#
# Pricing source of truth: internal/router/catalog. Input/output prices and
# cache-read multipliers are generated by cmd/genprices. Cache creation remains
# at 1.25× input pending TTL-aware pricing.

set -euo pipefail

# ---------- background self-refresh ----------
#
# Once every WEAVE_STATUSLINE_UPDATE_INTERVAL_DAYS (default 7), check
# raw.githubusercontent.com for a newer copy of this script and swap it in
# atomically. Runs in a forked subshell so the current Claude turn never
# blocks; the next turn picks up the new version. Applies to both user-scope
# (~/.weave/cc-statusline.sh) and project-scope (<repo>/.claude/cc-statusline.sh)
# installs — project teammates rate-limit independently because the stamp
# lives in their per-user cache dir, and on no-content-change days we skip
# the mv entirely so the repo working tree stays clean. When upstream does
# change, the first teammate's commit propagates the new version to the rest.
# A pricing-table miss also triggers a refresh off-schedule — see
# weave_refresh_on_price_miss below.
#
# Opt out entirely with `export WEAVE_STATUSLINE_UPDATE=0`. Override the
# source with `WEAVE_STATUSLINE_URL=...`, e.g. for self-hosters who fork.
#
# $1 is an optional stamp suffix, so a caller refreshing for a reason other than
# "the interval elapsed" rate-limits on its own clock rather than sharing the
# periodic check's budget. $2=1 drops the stamp again if the download fails, for
# callers recovering from a known-bad state where waiting out the full interval
# on a transient network error is worse than retrying next turn.
weave_self_refresh() {
  local stamp_suffix="${1:-}"
  local retry_on_fail="${2:-0}"

  [ "${WEAVE_STATUSLINE_UPDATE:-1}" = "0" ] && return 0
  command -v curl >/dev/null 2>&1 || return 0

  local self="${BASH_SOURCE[0]:-$0}"
  [ -f "$self" ] && [ -w "$self" ] || return 0

  local interval_days="${WEAVE_STATUSLINE_UPDATE_INTERVAL_DAYS:-7}"
  local interval_seconds=$(( interval_days * 86400 ))

  # Stamp lives in the per-user cache dir, keyed by absolute script path so
  # multiple repos (and the user-scope copy) rate-limit independently and no
  # stray file ever lands inside a repo working tree.
  local cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/weave-router"
  mkdir -p "$cache_dir" 2>/dev/null || return 0
  local script_slug
  script_slug="$(printf '%s' "$self" | tr -c 'A-Za-z0-9._-' '_')"
  local stamp="$cache_dir/checked-at${script_slug}${stamp_suffix}"

  local now stamp_mtime
  now="$(date +%s 2>/dev/null)" || return 0
  if [ -f "$stamp" ]; then
    # Try GNU `stat -c %Y` first; on macOS (BSD stat) -c isn't recognized
    # and exits non-zero, so we fall through to `stat -f %m`. The reverse
    # order is broken: GNU `stat -f` is `--file-system`, which silently
    # succeeds with multi-line filesystem info instead of failing, leaving
    # $stamp_mtime as garbage and disabling the rate-limit check entirely.
    stamp_mtime="$(stat -c %Y "$stamp" 2>/dev/null || stat -f %m "$stamp" 2>/dev/null)" || stamp_mtime=0
  else
    stamp_mtime=0
  fi
  if [ -n "${stamp_mtime:-}" ] && [ "$stamp_mtime" -gt 0 ] \
     && [ $(( now - stamp_mtime )) -lt "$interval_seconds" ]; then
    return 0
  fi

  # Touch the stamp BEFORE forking so concurrent statusline invocations
  # (Claude calls us on every turn) don't all kick off downloads.
  : > "$stamp" 2>/dev/null || return 0

  local url="${WEAVE_STATUSLINE_URL:-https://raw.githubusercontent.com/workweave/router/main/install/cc-statusline.sh}"
  # $$ alone is not unique: two calls can run in one invocation (the periodic
  # check and a pricing-miss retry both fire on a cold cache) and would then
  # curl -o into the same path and mv over each other, installing a truncated
  # script that can never self-heal. The stamp suffix is the right key — it is
  # what makes two callers mutually exclusive in the first place, so callers
  # that could overlap necessarily have different suffixes.
  local tmp="${self}.tmp.$$${stamp_suffix}"
  (
    # Detach stdin (CC pipes JSON to us) so curl can't accidentally consume
    # it, and silence all output so nothing leaks into the statusline.
    exec </dev/null
    if curl -fsSL --max-time 15 "$url" -o "$tmp" 2>/dev/null \
       && [ -s "$tmp" ] \
       && head -n 1 "$tmp" | grep -q '^#!.*bash' \
       && [ "$(wc -c < "$tmp")" -ge 1024 ]; then
      # No-op when the download matches what's already on disk — keeps git
      # status clean for project-scope teammates during a routine refresh.
      if cmp -s "$tmp" "$self"; then
        rm -f "$tmp"
      else
        chmod +x "$tmp" 2>/dev/null || true
        mv "$tmp" "$self" 2>/dev/null || rm -f "$tmp"
      fi
    else
      rm -f "$tmp"
      # A download that never landed shouldn't spend the caller's whole
      # interval; drop the stamp so the next turn can try again.
      if [ "$retry_on_fail" = "1" ]; then
        rm -f "$stamp"
      fi
    fi
  ) >/dev/null 2>&1 &
  disown 2>/dev/null || true
  return 0
}
weave_self_refresh 2>/dev/null || true

# ---------- background slash-command refresh ----------
#
# The /force-model, /router-* … wrappers under <install>/.claude/commands/ are
# written once at install time and then never touched again, so a user who
# doesn't re-run the installer keeps whatever set shipped that day. This script
# is the only thing Claude Code invokes on every turn, so it doubles as the
# refresh point for them: same rate limit, same detached fork, same
# content-diff no-op as the self-refresh above.
#
# Never clobber a wrapper the user edited. The only way to tell an edit from a
# stale copy is to remember what we last downloaded, so each canonical file is
# cached (unrendered) per install under the user cache dir and a wrapper is
# replaced only when its bytes still match that baseline. With no baseline yet
# we can't prove anything, so the first run only seeds the cache and a swap can
# happen from the next one on. install.sh seeds it too, so fresh installs skip
# that warm-up round.
#
# Never touch a wrapper git tracks either. A project-scope install writes the
# wrappers into the repo's own .claude/commands/, and unlike cc-statusline.sh
# the installer does not gitignore them — so an unattended weekly rewrite would
# surface as unexplained dirty files (and could ride along in someone's commit).
# Only the installer changes tracked files, and only when a human runs it.
#
# Opt out with WEAVE_COMMANDS_UPDATE=0 (WEAVE_STATUSLINE_UPDATE=0 disables this
# too, along with every other network path here). Override the source with
# WEAVE_COMMANDS_URL_BASE=..., e.g. for self-hosters who fork.

# weave_command_tracked_by_git returns 0 when $1 is a file git tracks. Used to
# leave repo-committed wrappers alone; no git (or no repo) means untracked.
weave_command_tracked_by_git() {
  command -v git >/dev/null 2>&1 || return 1
  git -C "$(dirname "$1")" ls-files --error-unmatch -- "$1" >/dev/null 2>&1
}

# weave_installed_command_names lists the wrappers this install may refresh.
# The statusline ships standalone (no registry.sh beside it), so the installed
# set — itself written from the registry — is the only source of truth here.
#
# Files written before ownership markers existed carry none, so matching on the
# marker alone would freeze every pre-marker install out of refreshes forever.
# List every wrapper instead and let the baseline comparison below decide: a
# file is replaced only when its bytes still match the last canonical copy, so
# a user-authored command is never touched whether or not it carries a marker.
weave_installed_command_names() {
  local dir="$1" file
  for file in "$dir"/*.md; do
    [ -f "$file" ] || continue
    printf '%s\n' "$(basename "$file" .md)"
  done
}

# weave_render_command prints $1 with the installer's {{SCOPE}} placeholder
# replaced by $2, matching how install_slash_commands writes the same file.
# Trailing newlines are stripped on both sides of every comparison below.
weave_render_command() {
  local body
  body="$(cat "$1" 2>/dev/null)" || return 1
  printf '%s' "${body//\{\{SCOPE\}\}/$2}"
}

weave_sync_commands() {
  [ "${WEAVE_STATUSLINE_UPDATE:-1}" = "0" ] && return 0
  [ "${WEAVE_COMMANDS_UPDATE:-1}" = "0" ] && return 0
  command -v curl >/dev/null 2>&1 || return 0

  local self="${BASH_SOURCE[0]:-$0}" self_dir cmd_dir
  self_dir="$(cd "$(dirname "$self")" 2>/dev/null && pwd -P)" || return 0
  # User scope installs this script at <base>/.weave/, project and --dir at
  # <base>/.claude/. Claude Code reads commands from <base>/.claude/commands in
  # both layouts.
  case "${self_dir##*/}" in
    .weave)  cmd_dir="${self_dir%/*}/.claude/commands" ;;
    .claude) cmd_dir="$self_dir/commands" ;;
    *)       return 0 ;;
  esac
  [ -d "$cmd_dir" ] && [ -w "$cmd_dir" ] || return 0

  local cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/weave-router"
  local dir_slug
  dir_slug="$(printf '%s' "$cmd_dir" | tr -c 'A-Za-z0-9._-' '_')"
  # Baseline is keyed by install, not shared: two installs refresh on their own
  # clocks, and a shared baseline updated by one would make the other's still-
  # canonical wrappers look user-edited and freeze them forever.
  local baseline_dir="$cache_dir/commands${dir_slug}"
  local stamp="$cache_dir/checked-at${dir_slug}.commands"
  mkdir -p "$baseline_dir" 2>/dev/null || return 0

  local interval_days="${WEAVE_STATUSLINE_UPDATE_INTERVAL_DAYS:-7}"
  local now stamp_mtime
  now="$(date +%s 2>/dev/null)" || return 0
  if [ -f "$stamp" ]; then
    stamp_mtime="$(stat -c %Y "$stamp" 2>/dev/null || stat -f %m "$stamp" 2>/dev/null)" || stamp_mtime=0
  else
    stamp_mtime=0
  fi
  if [ -n "${stamp_mtime:-}" ] && [ "$stamp_mtime" -gt 0 ] \
     && [ $(( now - stamp_mtime )) -lt $(( interval_days * 86400 )) ]; then
    return 0
  fi
  # Stamp before forking, same as the self-refresh: Claude Code calls us on
  # every turn and concurrent invocations must not all start downloading.
  : > "$stamp" 2>/dev/null || return 0

  # router-off/on/status bake this install's scope selector into their npx
  # line (upstream carries {{SCOPE}} there). Recover it from the installed copy
  # so a project or --dir install keeps toggling its own config; when it can't
  # be recovered those three are skipped rather than rewritten to point at the
  # user-scope install.
  local scope_args="" scope_known="false" off="$cmd_dir/router-off.md"
  if [ -f "$off" ] && grep -q '^`npx @workweave/router off --claude.*`$' "$off" 2>/dev/null; then
    scope_known="true"
    scope_args="$(sed -n 's|^`npx @workweave/router off --claude\(.*\)`$|\1|p' "$off" | head -n 1)"
  fi

  local url_base="${WEAVE_COMMANDS_URL_BASE:-https://raw.githubusercontent.com/workweave/router/main/install/commands}"
  local name installed raw prev tmp new_body prev_body installed_body
  (
    # Detach stdin (CC pipes JSON to us) so curl can't consume it, and silence
    # everything so no output leaks into the statusline.
    exec </dev/null
    while IFS= read -r name; do
      installed="$cmd_dir/$name.md"
      # Only ever refresh a wrapper that is already installed: a missing one
      # was uninstalled or deliberately deleted, and resurrecting it would be
      # a surprise. A symlink is user-owned; leave it alone. A git-tracked
      # wrapper belongs to the repo — rewriting it would dirty a working tree
      # nobody asked us to touch.
      [ -f "$installed" ] || continue
      [ -L "$installed" ] && continue
      weave_command_tracked_by_git "$installed" && continue

      raw="$baseline_dir/$name.md.tmp.$$"
      curl -fsSL --max-time 15 "$url_base/$name.md" -o "$raw" 2>/dev/null || { rm -f "$raw"; continue; }
      # Shape check: every wrapper opens with YAML front matter, so a 404 page
      # or a truncated body can never be installed as a command.
      if [ ! -s "$raw" ] || [ "$(head -n 1 "$raw")" != "---" ]; then
        rm -f "$raw"
        continue
      fi

      prev="$baseline_dir/$name.md"
      if grep -q '{{SCOPE}}' "$raw" && [ "$scope_known" != "true" ]; then
        mv "$raw" "$prev" 2>/dev/null || rm -f "$raw"
        continue
      fi

      if [ -f "$prev" ]; then
        new_body="$(weave_render_command "$raw" "$scope_args")"
        prev_body="$(weave_render_command "$prev" "$scope_args")"
        installed_body="$(cat "$installed" 2>/dev/null | sed '/^<!-- weave-router managed command: .* -->$/d')" || installed_body=""
        if [ "$prev_body" = "$installed_body" ] && [ "$new_body" != "$installed_body" ]; then
          tmp="$installed.tmp.$$"
          if printf '%s\n<!-- weave-router managed command: %s -->' "$new_body" "$name" >"$tmp" 2>/dev/null; then
            mv "$tmp" "$installed" 2>/dev/null || rm -f "$tmp"
          else
            rm -f "$tmp"
          fi
        fi
      fi
      mv "$raw" "$prev" 2>/dev/null || rm -f "$raw"
    done <<EOF
$(weave_installed_command_names "$cmd_dir")
EOF
  ) >/dev/null 2>&1 &
  disown 2>/dev/null || true
  return 0
}
weave_sync_commands 2>/dev/null || true

# ---------- org "hide terminal surfaces" gate ----------
#
# When the org has hidden the router's terminal surfaces, the statusline
# renders nothing: this prints no output and exits 0, leaving the slot blank.
# The setting comes from GET /v1/display-settings with the install's own
# router key, but the foreground path NEVER touches the network: it decides
# solely from a per-install cache file (TTL WEAVE_DISPLAY_SETTINGS_TTL_SECONDS,
# default 1h), and a missing or stale cache fails open — the statusline
# renders normally. When the cache is missing or stale a detached background
# refresh re-fetches the setting and rewrites the cache atomically, so the
# next turn picks up the fresh value; a refresh failure simply leaves the
# cache stale, which keeps failing open rather than pinning the gate closed.
#
# Claude Code runs the statusline every turn, so several invocations can see a
# stale cache at once. Letting each fetch independently is not safe: the
# responses can land out of order, and the loser's mv would replace a newer
# setting with an older one AND stamp it fresh, pinning the gate on a stale
# value for a full TTL. Refreshes are therefore serialized per cache key on a
# mkdir mutex (atomic everywhere; flock is absent on macOS), held across both
# the fetch and the write. A refresh that finds the mutex held exits rather
# than queueing — the in-flight one is at least as fresh as anything it would
# fetch, so waiting only to overwrite it is the bug. The one path that is not
# strictly exclusive is reclaiming a lock whose holder died; see below.
weave_hidden_gate() {
  command -v curl >/dev/null 2>&1 || return 1
  command -v jq >/dev/null 2>&1 || return 1

  local cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/weave-router"
  mkdir -p "$cache_dir" 2>/dev/null || return 1
  local self="${BASH_SOURCE[0]:-$0}"
  local script_slug
  script_slug="$(printf '%s' "$self" | tr -c 'A-Za-z0-9._-' '_')"
  local cache="$cache_dir/display-settings${script_slug}"
  local ttl="${WEAVE_DISPLAY_SETTINGS_TTL_SECONDS:-3600}"

  local now mtime fresh="false"
  now="$(date +%s 2>/dev/null)" || now=0
  if [ -f "$cache" ]; then
    mtime="$(stat -c %Y "$cache" 2>/dev/null || stat -f %m "$cache" 2>/dev/null)" || mtime=0
    if [ -n "${mtime:-}" ] && [ "$mtime" -gt 0 ] && [ $(( now - mtime )) -lt "$ttl" ]; then
      fresh="true"
    fi
  fi

  # Foreground decision: only a fresh cache hides the surfaces. Anything else
  # (no cache, stale cache, unreadable cache) renders normally so a slow or
  # unreachable router can never stall a turn or wedge the statusline blank.
  if [ "$fresh" = "true" ]; then
    [ "$(cat "$cache" 2>/dev/null)" = "1" ]
    return
  fi

  # Background refresh for the next invocation. Resolve the router base URL
  # and key inside the subshell from the Claude Code settings the installer
  # wrote: project/--dir installs put both under <base>/.claude alongside
  # this script (key in settings.local.json), while a user-scope install
  # lives under ~/.weave and reads ~/.claude. Resolve relative to the
  # script's own location, falling back to user scope, so a project install
  # never reads (or leaks) the user-scope key. ANTHROPIC_BASE_URL and
  # WEAVE_ROUTER_BASE_URL may also be set in the environment. A file:// base
  # URL is the offline/test seam: curl reads it as the response body
  # directly, so the endpoint path is meaningless for it; real router URLs
  # (https) get /v1/display-settings appended.
  (
    exec </dev/null

    # Take the per-cache-key mutex, or bail. mkdir is the portable atomic
    # test-and-set. A crashed holder would otherwise block refreshes forever,
    # so a lock older than the fetch timeout is treated as abandoned and
    # reclaimed. Releasing from a trap covers every exit path below.
    lock="$cache.lock"
    if ! mkdir "$lock" 2>/dev/null; then
      lock_mtime="$(stat -c %Y "$lock" 2>/dev/null || stat -f %m "$lock" 2>/dev/null)" || lock_mtime=0
      lock_now="$(date +%s 2>/dev/null)" || lock_now=0
      if [ "${lock_mtime:-0}" -le 0 ] || [ $(( lock_now - lock_mtime )) -le 30 ]; then
        exit 0
      fi
      # Reclaiming an abandoned lock must not be delete-then-recreate. With
      # `rm -rf` + `mkdir`, refreshers that all see the same stale lock each
      # delete the next one's freshly created directory, so many end up holding
      # it at once (measured at 50 claimants: 4-11 concurrent holders) and their
      # writes can land out of order. Renaming is atomic, so only one racer can
      # move a given directory aside and the losers exit instead of clobbering
      # the winner (same measurement: 1-2). It is not a perfect mutex — a
      # straggler can still reclaim the new lock a winner just created, since
      # nothing distinguishes it from the stale one — but real refreshes arrive
      # one per turn rather than 50 at once, and the staleness threshold below
      # is what bounds the rest.
      dead="$lock.dead.$$"
      mv "$lock" "$dead" 2>/dev/null || exit 0
      rm -rf "$dead" 2>/dev/null
      mkdir "$lock" 2>/dev/null || exit 0
    fi
    trap 'rmdir "$lock" 2>/dev/null' EXIT

    self_dir="$(cd "$(dirname "$self")" 2>/dev/null && pwd)"
    settings_base="$HOME"
    case "$self_dir" in
      */.claude) settings_base="${self_dir%/.claude}" ;;
    esac
    settings="$settings_base/.claude/settings.json"
    local_settings="$settings_base/.claude/settings.local.json"
    base_url="${WEAVE_ROUTER_BASE_URL:-${ANTHROPIC_BASE_URL:-}}"
    key="${WEAVE_ROUTER_KEY:-}"
    if [ -z "$key" ] && [ -f "$settings" ]; then
      key="$(jq -r '.env.ANTHROPIC_CUSTOM_HEADERS // "" | split("\n")[] | select(startswith("X-Weave-Router-Key:")) | sub("^X-Weave-Router-Key:[[:space:]]*";"")' "$settings" 2>/dev/null | head -n1)"
    fi
    if [ -z "$key" ] && [ -f "$local_settings" ]; then
      key="$(jq -r '.env.ANTHROPIC_CUSTOM_HEADERS // "" | split("\n")[] | select(startswith("X-Weave-Router-Key:")) | sub("^X-Weave-Router-Key:[[:space:]]*";"")' "$local_settings" 2>/dev/null | head -n1)"
    fi
    if [ -z "$base_url" ] && [ -f "$settings" ]; then
      base_url="$(jq -r '.env.ANTHROPIC_BASE_URL // empty' "$settings" 2>/dev/null)"
    fi
    [ -n "$base_url" ] && [ -n "$key" ] || exit 0
    url="${base_url%/}"
    case "$url" in
      file://*) ;;
      *) url="$url/v1/display-settings" ;;
    esac
    body="$(curl -fsS --max-time 5 -H "X-Weave-Router-Key: $key" "$url" 2>/dev/null)" || exit 0
    hidden="$(printf '%s' "$body" | jq -r '.hide_terminal_surfaces // false' 2>/dev/null)"
    tmp="$cache.tmp.$$"
    if [ "$hidden" = "true" ]; then
      printf '1' >"$tmp" 2>/dev/null && mv "$tmp" "$cache" 2>/dev/null
    else
      printf '0' >"$tmp" 2>/dev/null && mv "$tmp" "$cache" 2>/dev/null
    fi
    rm -f "$tmp" 2>/dev/null
  ) >/dev/null 2>&1 &
  disown 2>/dev/null || true
  return 1
}

if weave_hidden_gate </dev/null; then
  exit 0
fi

input="$(cat)"
transcript_path="$(printf '%s' "$input" | jq -r '.transcript_path // empty')"
# Prefer model.id over display_name: pricing keys + the routed model id in
# the transcript are canonical ids (e.g. claude-opus-4-7), while display_name
# is a human label ("Opus 4.7 (1M context)") that won't hit the pricing table,
# zeroing out savings. id passes through normalize_model cleanly.
selected_display="$(printf '%s' "$input" | jq -r '.model.id // .model.display_name // "?"')"

# Normalize a model id to a pricing-table key. CC + the decisions log carry
# two flavors of annotation we don't want in the lookup:
#   * date suffix:    claude-opus-4-7-20260101  → claude-opus-4-7
#   * variant tag:    claude-opus-4-7[1m]       → claude-opus-4-7
# The 1M-context variant prices ~2× base for prompts >200k tokens, but for
# the "saved $X vs your selection" UX the base rate is the right comparison
# — we're measuring the model swap, not the context tier. Used below on the
# routed and requested model ids from the decisions log / transcript.
normalize_model() {
  printf '%s' "$1" | sed -E 's/\[[^]]*\]$//; s/-[0-9]{8}$//'
}

# USD per 1k tokens. Generated from internal/observability/otel/pricing.go
# (USD/1M there, ÷1000 here) by cmd/genprices. Do not hand-edit — run
# `make generate` after updating pricing.go.
# BEGIN_GENERATED_PRICES
prices='{
  "input": {
    "claude-fable-5":                   0.01,
    "claude-haiku-4-5":                 0.001,
    "claude-opus-4-0":                  0.015,
    "claude-opus-4-1":                  0.015,
    "claude-opus-4-5":                  0.005,
    "claude-opus-4-6":                  0.005,
    "claude-opus-4-7":                  0.005,
    "claude-opus-4-8":                  0.005,
    "claude-opus-5":                    0.005,
    "claude-sonnet-4-5":                0.003,
    "claude-sonnet-4-6":                0.003,
    "claude-sonnet-5":                  0.003,
    "deepseek/deepseek-v4-flash":       0.0001134,
    "deepseek/deepseek-v4-pro":         0.00174,
    "deepseek/deepseek-v4-pro-0813":    0.00174,
    "gemini-2.0-flash":                 0.0001,
    "gemini-2.0-flash-lite":            0.000075,
    "gemini-2.5-flash":                 0.0003,
    "gemini-2.5-flash-lite":            0.0001,
    "gemini-2.5-pro":                   0.00125,
    "gemini-3-flash-preview":           0.0005,
    "gemini-3-pro-preview":             0.002,
    "gemini-3.1-flash-lite-preview":    0.0001,
    "gemini-3.1-pro-preview":           0.002,
    "gemini-3.5-flash":                 0.0015,
    "gemini-3.5-flash-lite":            0.0003,
    "gemini-3.6-flash":                 0.0015,
    "gemini-3.7-flash":                 0.0015,
    "google/gemma-4-26b-a4b-it":        0.00015,
    "gpt-4.1":                          0.002,
    "gpt-4.1-mini":                     0.0004,
    "gpt-4.1-nano":                     0.0001,
    "gpt-4o":                           0.0025,
    "gpt-4o-mini":                      0.00015,
    "gpt-5":                            0.0025,
    "gpt-5-chat":                       0.0025,
    "gpt-5-mini":                       0.0005,
    "gpt-5-nano":                       0.0001,
    "gpt-5.4":                          0.0025,
    "gpt-5.4-mini":                     0.00075,
    "gpt-5.4-nano":                     0.0002,
    "gpt-5.4-pro":                      0.03,
    "gpt-5.5":                          0.005,
    "gpt-5.5-mini":                     0.0005,
    "gpt-5.5-nano":                     0.00015,
    "gpt-5.5-pro":                      0.03,
    "gpt-5.6-luna":                     0.001,
    "gpt-5.6-luna-pro":                 0.001,
    "gpt-5.6-sol":                      0.005,
    "gpt-5.6-sol-pro":                  0.005,
    "gpt-5.6-terra":                    0.0025,
    "grok-4.5":                         0.002,
    "grok-4.6":                         0.002,
    "minimax/minimax-m2.7":             0.0003,
    "minimax/minimax-m3":               0.0003,
    "mistralai/mistral-small-2603":     0.0002,
    "moonshotai/kimi-k2.5":             0.0006,
    "moonshotai/kimi-k2.6":             0.00095,
    "moonshotai/kimi-k2.7":             0.00095,
    "moonshotai/kimi-k3":               0.003,
    "qwen/qwen3-235b-a22b-2507":        0.0002266,
    "qwen/qwen3-30b-a3b-instruct-2507": 0.00015,
    "qwen/qwen3-coder":                 0.0009,
    "qwen/qwen3-coder-next":            0.0005,
    "qwen/qwen3-next-80b-a3b-instruct": 0.00015,
    "qwen/qwen3.5-flash-02-23":         0.00005,
    "qwen/qwen3.6-35b-a3b":             0.00015,
    "qwen/qwen3.7-plus":                0.0004,
    "qwen/qwen3.8-max":                 0.002,
    "xiaomi/mimo-v2.5-pro":             0.001,
    "z-ai/glm-5":                       0.001,
    "z-ai/glm-5.1":                     0.0014,
    "z-ai/glm-5.2":                     0.0014,
    "z-ai/glm-5.3":                     0.0014,
    "z-ai/glm-5.3-flash":               0.00015
  },
  "output": {
    "claude-fable-5":                   0.05,
    "claude-haiku-4-5":                 0.005,
    "claude-opus-4-0":                  0.075,
    "claude-opus-4-1":                  0.075,
    "claude-opus-4-5":                  0.025,
    "claude-opus-4-6":                  0.025,
    "claude-opus-4-7":                  0.025,
    "claude-opus-4-8":                  0.025,
    "claude-opus-5":                    0.025,
    "claude-sonnet-4-5":                0.015,
    "claude-sonnet-4-6":                0.015,
    "claude-sonnet-5":                  0.015,
    "deepseek/deepseek-v4-flash":       0.0002791,
    "deepseek/deepseek-v4-pro":         0.00348,
    "deepseek/deepseek-v4-pro-0813":    0.00348,
    "gemini-2.0-flash":                 0.0004,
    "gemini-2.0-flash-lite":            0.0003,
    "gemini-2.5-flash":                 0.0012,
    "gemini-2.5-flash-lite":            0.0004,
    "gemini-2.5-pro":                   0.005,
    "gemini-3-flash-preview":           0.002,
    "gemini-3-pro-preview":             0.008,
    "gemini-3.1-flash-lite-preview":    0.0004,
    "gemini-3.1-pro-preview":           0.008,
    "gemini-3.5-flash":                 0.009,
    "gemini-3.5-flash-lite":            0.0025,
    "gemini-3.6-flash":                 0.0075,
    "gemini-3.7-flash":                 0.0075,
    "google/gemma-4-26b-a4b-it":        0.0006,
    "gpt-4.1":                          0.008,
    "gpt-4.1-mini":                     0.0016,
    "gpt-4.1-nano":                     0.0004,
    "gpt-4o":                           0.01,
    "gpt-4o-mini":                      0.0006,
    "gpt-5":                            0.01,
    "gpt-5-chat":                       0.01,
    "gpt-5-mini":                       0.002,
    "gpt-5-nano":                       0.0004,
    "gpt-5.4":                          0.015,
    "gpt-5.4-mini":                     0.0045,
    "gpt-5.4-nano":                     0.00125,
    "gpt-5.4-pro":                      0.18,
    "gpt-5.5":                          0.03,
    "gpt-5.5-mini":                     0.0025,
    "gpt-5.5-nano":                     0.0006,
    "gpt-5.5-pro":                      0.18,
    "gpt-5.6-luna":                     0.006,
    "gpt-5.6-luna-pro":                 0.006,
    "gpt-5.6-sol":                      0.03,
    "gpt-5.6-sol-pro":                  0.03,
    "gpt-5.6-terra":                    0.015,
    "grok-4.5":                         0.006,
    "grok-4.6":                         0.006,
    "minimax/minimax-m2.7":             0.0012,
    "minimax/minimax-m3":               0.0012,
    "mistralai/mistral-small-2603":     0.0006,
    "moonshotai/kimi-k2.5":             0.003,
    "moonshotai/kimi-k2.6":             0.004,
    "moonshotai/kimi-k2.7":             0.004,
    "moonshotai/kimi-k3":               0.015,
    "qwen/qwen3-235b-a22b-2507":        0.0009064,
    "qwen/qwen3-30b-a3b-instruct-2507": 0.0006,
    "qwen/qwen3-coder":                 0.0027,
    "qwen/qwen3-coder-next":            0.0012,
    "qwen/qwen3-next-80b-a3b-instruct": 0.0012,
    "qwen/qwen3.5-flash-02-23":         0.00015,
    "qwen/qwen3.6-35b-a3b":             0.001,
    "qwen/qwen3.7-plus":                0.0016,
    "qwen/qwen3.8-max":                 0.006,
    "xiaomi/mimo-v2.5-pro":             0.003,
    "z-ai/glm-5":                       0.0032,
    "z-ai/glm-5.1":                     0.0044,
    "z-ai/glm-5.2":                     0.0044,
    "z-ai/glm-5.3":                     0.0044,
    "z-ai/glm-5.3-flash":               0.0005
  },
  "cache_read": {
    "claude-fable-5":                   0.1,
    "claude-haiku-4-5":                 0.1,
    "claude-opus-4-0":                  0.1,
    "claude-opus-4-1":                  0.1,
    "claude-opus-4-5":                  0.1,
    "claude-opus-4-6":                  0.1,
    "claude-opus-4-7":                  0.1,
    "claude-opus-4-8":                  0.1,
    "claude-opus-5":                    0.1,
    "claude-sonnet-4-5":                0.1,
    "claude-sonnet-4-6":                0.1,
    "claude-sonnet-5":                  0.1,
    "deepseek/deepseek-v4-flash":       0.2,
    "deepseek/deepseek-v4-pro":         0.11494252873563218,
    "deepseek/deepseek-v4-pro-0813":    0.11494252873563218,
    "gemini-2.0-flash":                 0.25,
    "gemini-2.0-flash-lite":            0.25,
    "gemini-2.5-flash":                 0.1,
    "gemini-2.5-flash-lite":            0.1,
    "gemini-2.5-pro":                   0.1,
    "gemini-3-flash-preview":           0.1,
    "gemini-3-pro-preview":             0.1,
    "gemini-3.1-flash-lite-preview":    0.1,
    "gemini-3.1-pro-preview":           0.1,
    "gemini-3.5-flash":                 0.1,
    "gemini-3.5-flash-lite":            0.1,
    "gemini-3.6-flash":                 0.1,
    "gemini-3.7-flash":                 0.1,
    "google/gemma-4-26b-a4b-it":        0.1,
    "gpt-4.1":                          0.25,
    "gpt-4.1-mini":                     0.25,
    "gpt-4.1-nano":                     0.25,
    "gpt-4o":                           0.5,
    "gpt-4o-mini":                      0.5,
    "gpt-5":                            0.1,
    "gpt-5-chat":                       0.1,
    "gpt-5-mini":                       0.1,
    "gpt-5-nano":                       0.1,
    "gpt-5.4":                          0.1,
    "gpt-5.4-mini":                     0.1,
    "gpt-5.4-nano":                     0.1,
    "gpt-5.4-pro":                      1,
    "gpt-5.5":                          0.1,
    "gpt-5.5-mini":                     0.1,
    "gpt-5.5-nano":                     0.1,
    "gpt-5.5-pro":                      1,
    "gpt-5.6-luna":                     0.1,
    "gpt-5.6-luna-pro":                 0.1,
    "gpt-5.6-sol":                      0.1,
    "gpt-5.6-sol-pro":                  0.1,
    "gpt-5.6-terra":                    0.1,
    "grok-4.5":                         0.25,
    "grok-4.6":                         0.25,
    "minimax/minimax-m2.7":             0.2,
    "minimax/minimax-m3":               0.2,
    "mistralai/mistral-small-2603":     0.1,
    "moonshotai/kimi-k2.5":             0.5,
    "moonshotai/kimi-k2.6":             0.1684,
    "moonshotai/kimi-k2.7":             0.2,
    "moonshotai/kimi-k3":               0.1,
    "qwen/qwen3-235b-a22b-2507":        0.5,
    "qwen/qwen3-30b-a3b-instruct-2507": 0.1684,
    "qwen/qwen3-coder":                 0.1684,
    "qwen/qwen3-coder-next":            0.5,
    "qwen/qwen3-next-80b-a3b-instruct": 0.5,
    "qwen/qwen3.5-flash-02-23":         0.1,
    "qwen/qwen3.6-35b-a3b":             0.1,
    "qwen/qwen3.7-plus":                0.2,
    "qwen/qwen3.8-max":                 0.125,
    "xiaomi/mimo-v2.5-pro":             0.1,
    "z-ai/glm-5":                       0.2,
    "z-ai/glm-5.1":                     0.18571428571428572,
    "z-ai/glm-5.2":                     0.18571428571428572,
    "z-ai/glm-5.3":                     0.18571428571428572,
    "z-ai/glm-5.3-flash":               0.2
  }
}'
# END_GENERATED_PRICES

routed=""
forced="false"
forced_model=""
session_savings=""
tot_in=0
tot_out=0
tot_cache_read=0
tot_cache_write=0

# Per-turn savings compare each turn's routed cost (priced from
# message.model in the transcript) against what the CC-side model selection
# (selected_display) would have cost on the same tokens. The selection
# isn't strictly the per-turn "requested" model — CC tags some background
# side-calls (compaction probes, title-gen) with a different model id —
# but for those the planner short-circuits to a hard pin and the savings
# math zeroes out anyway. Turns where routed == selection or where either
# model isn't in the pricing table emit 0 savings; the tokens clause
# always renders.

# Normalize the CC-side selection once for use in the jq math below.
requested_norm="$(normalize_model "$selected_display")"

if [[ -n "$transcript_path" && -f "$transcript_path" ]]; then
  # macOS ships `tail -r`, GNU coreutils ships `tac`. Either works to walk the
  # JSONL in reverse so we can grab the latest assistant turn.
  if command -v tac >/dev/null 2>&1; then reverse=(tac); else reverse=(tail -r); fi

  # CC stamps message.model = "<synthetic>" on assistant turns it generated
  # locally (errored requests, cancellations, tool-only stubs) instead of a
  # real model id. Show that as "failure" rather than leaking the internal
  # sentinel into the statusline.
  routed="$("${reverse[@]}" "$transcript_path" 2>/dev/null \
    | jq -r 'select(.type=="assistant") | .message.model // empty' \
    | head -n 1 || true)"
  if [[ "$routed" == "<synthetic>" ]]; then
    routed="failure"
  else
    routed="$(normalize_model "$routed")"
  fi

  # Detect an active /force-model pin from the router's synthetic ack turns —
  # the only turns stamped message.model == "weave-router". The router emits
  # one whenever a pin changes state:
  #   * /force-model              → "force-model applied: <model> (<provider>) …"
  #   * /unforce-model            → "force-model cleared …"
  #   * loop / no-progress break  → "… clearing the session pin …" (expires the
  #                                  pin, including a user-forced one)
  #   * unrecognized model        → "… isn't a recognized model · keeping
  #                                  automatic routing" — a NO-OP: the prior
  #                                  pin, if any, is left untouched
  #   * listing (historical)      → "… pick a model by id …" / "no models are
  #                                  available to pin …" — also NO-OPs. The
  #                                  router no longer emits these, but a
  #                                  transcript written while it did still
  #                                  carries them, and they must not read as
  #                                  a clear or a live pin loses its [forced]
  # These persist on disk (the ingress stripper only scrubs them from upstream
  # requests). Classify each weave-router turn newest-first, skip the no-op
  # "rejected" acks, and let the latest real state change decide: an "applied"
  # marker means the session is pinned (and names the model); anything else
  # (cleared / loop-break / no-progress) means automatic routing has resumed.
  # Restricting to weave-router turns keeps a normal reply that merely quotes
  # these phrases from flipping the tag. (A silent server-side TTL expiry emits
  # no turn and so can't be reflected here — the pin TTL outlives a session.)
  force_state="$("${reverse[@]}" "$transcript_path" 2>/dev/null \
    | jq -r 'select(.type=="assistant" and .message.model=="weave-router")
        | ([.message.content[]? | select(.type? == "text") | .text] | join(" ") | gsub("[\n\r]"; " ")) as $t
        | if ($t | test("force-model applied:")) then "APPLIED " + ($t | capture("force-model applied: (?<m>[^ ]+)").m)
          elif ($t | test("isn.t a recognized model")) then "REJECTED"
          elif ($t | test("pick a model by id|no models are available to pin")) then "REJECTED"
          else "CLEARED" end' 2>/dev/null \
    | grep -m1 -v '^REJECTED$' || true)"
  if [[ "$force_state" == APPLIED\ * ]]; then
    forced="true"
    forced_model="${force_state#APPLIED }"
  fi

  # Compute a session running total: savings across every assistant turn
  # whose marker reports a requested ≠ routed swap, plus cumulative token
  # counts across every assistant turn (rerouted or not — total work the
  # session has done). cache_creation is priced at 1.25× input; cache_read
  # uses each model's generated catalog multiplier. Both are no-ops when the
  # provider does not return those fields.
  #
  # The marker regex tolerates the optional "(<provider>)" segment and a
  # `[1m]` / `-YYYYMMDD` suffix on either model name so transcripts written
  # against context-tiered or dated model ids still parse cleanly.
  #
  # Dedup note: CC writes one JSONL entry per *content block* in an
  # assistant turn (text, text, tool_use → 3 entries), and every entry
  # carries the same `message.usage`. Summing per-entry triple-counts the
  # turn. We dedupe on (message.id, message.usage) before summing:
  #   * For native Anthropic upstreams message.id is unique per turn, so
  #     this collapses the content-block fan-out cleanly.
  #   * For non-Anthropic upstreams that round-trip through the router's
  #     translator, message.id can be a constant placeholder
  #     ("msg_translated"); usage still differs per turn (input_tokens
  #     grows), so the composite key keeps turns distinct. Two turns with
  #     byte-identical id AND usage would still collapse, but that's a
  #     genuine retry/duplicate we want to drop.
  read -r session_savings tot_in tot_out tot_cache_read tot_cache_write < <(
    jq -rs --argjson p "$prices" --arg requested "$requested_norm" '
      [.[] | select(.type=="assistant")] |
      unique_by([.message.id, .message.usage]) |
      .[] |
      .message as $m |
      ($m.model // "" | sub("\\[[^]]*\\]$"; "") | sub("-[0-9]{8}$"; "")) as $rm |
      {
        in:    ($m.usage.input_tokens // 0),
        out:   ($m.usage.output_tokens // 0),
        cwrt:  ($m.usage.cache_creation_input_tokens // 0),
        crd:   ($m.usage.cache_read_input_tokens // 0)
      } as $t |
      (if $requested == "" or $requested == $rm then 0
       else
         ($p.input[$rm] // null)             as $rin  | ($p.output[$rm] // null)             as $rout |
         ($p.cache_read[$rm] // null)        as $rcr  |
         ($p.input[$requested] // null)      as $sin  | ($p.output[$requested] // null)      as $sout |
         ($p.cache_read[$requested] // null) as $scr  |
         if ($rin == null or $rout == null or $rcr == null or $sin == null or $sout == null or $scr == null) then 0
         else
           (($t.in + 1.25 * $t.cwrt + $rcr * $t.crd) / 1000) as $routed_input_units |
           (($t.in + 1.25 * $t.cwrt + $scr * $t.crd) / 1000) as $requested_input_units |
           ($t.out / 1000)                                    as $output_units |
           ($routed_input_units * $rin + $output_units * $rout)       as $routed_cost |
           ($requested_input_units * $sin + $output_units * $sout)    as $requested_cost |
           ($requested_cost - $routed_cost)
         end
       end) as $savings |
      "\($savings) \($t.in) \($t.out) \($t.crd) \($t.cwrt)"
    ' "$transcript_path" 2>/dev/null \
    | awk 'BEGIN{s=0; i=0; o=0; r=0; w=0}
           {s+=$1; i+=$2; o+=$3; r+=$4; w+=$5}
           END{printf "%.4f %d %d %d %d\n", s, i, o, r, w}'
  ) || true
fi

# ---------- refresh when a model isn't in the pricing table ----------
#
# A model that shipped after this copy of the script has no price entry, so the
# jq guard above zeroes savings on every turn and the line reads "saved $0.00"
# — indistinguishable from "the router ran and didn't beat your selection". The
# periodic check heals that eventually, but a week late, and a model launch is
# exactly when the number gets looked at. Refresh off-schedule instead.
#
# Keyed on its own stamp per unpriced id: a model we never price (self-hosted,
# unrecognized) then costs one download per interval, not one per turn, and
# can't starve the periodic check.
weave_refresh_on_price_miss() {
  local candidates="" m
  for m in "$@"; do
    case "$m" in
      "" | "?" | failure | weave-router | "<synthetic>") continue ;;
    esac
    candidates="${candidates}${m}"$'\n'
  done
  [ -n "$candidates" ] || return 0

  # A malformed $prices emits nothing here, which fails closed (no refresh)
  # rather than re-downloading every turn.
  local missing
  missing="$(printf '%s' "$candidates" \
    | jq -rR --argjson p "$prices" \
        'select($p.input[.] == null or $p.output[.] == null)' 2>/dev/null \
    | head -n 1)" || return 0
  [ -n "$missing" ] || return 0

  local model_slug
  model_slug="$(printf '%s' "$missing" | tr -c 'A-Za-z0-9._-' '_')"
  weave_self_refresh ".miss.${model_slug}" 1
}
weave_refresh_on_price_miss "$requested_norm" "$routed" 2>/dev/null || true

# Brand color (#FF6C47) on terminals that grok 24-bit truecolor — that's
# every modern one (iTerm2, Apple Terminal, vscode, ghostty, alacritty,
# wezterm, kitty). Falls back gracefully on any escape-stripping terminal.
brand=$'\033[38;2;255;108;71mWEAVE ROUTER\033[0m'

# Format helpers.
fmt_money() {
  awk -v v="$1" 'BEGIN{
    if (v == "" || v+0 == 0)        { printf "$0.00";        exit }
    if (v+0 < 0.005 && v+0 > -0.005){ printf "<$0.01";       exit }
    if (v+0 < 0)                    { printf "-$%.2f", -v+0; exit }
    printf "$%.2f", v
  }'
}

fmt_tok() {
  awk -v v="$1" 'BEGIN{
    v = v+0
    if (v >= 1000000) { printf "%.1fM", v/1000000; exit }
    if (v >= 1000)    { printf "%.1fk", v/1000;    exit }
    printf "%d", v
  }'
}

# cache_read tokens are the cached portion of every prompt that the
# provider serves at 0.1× input price; cache_write tokens are the bytes
# that get newly cached on this turn at 1.25× input price. They behave
# completely differently both in cost and in what they tell the user
# about session-level efficiency, so we surface them separately rather
# than summing into a single "cached" number that conflates the two.
# Each clause is shown only when nonzero, so quiet sessions stay quiet.
tokens_clause=""
if [[ "$tot_in" -gt 0 || "$tot_out" -gt 0 || "$tot_cache_read" -gt 0 || "$tot_cache_write" -gt 0 ]]; then
  tokens_clause=" · $(fmt_tok "$tot_in") in / $(fmt_tok "$tot_out") out"
  if [[ "$tot_cache_read" -gt 0 ]]; then
    tokens_clause+=" / $(fmt_tok "$tot_cache_read") cache read"
  fi
  if [[ "$tot_cache_write" -gt 0 ]]; then
    tokens_clause+=" / $(fmt_tok "$tot_cache_write") cache write"
  fi
fi

if [[ "$forced" == "true" ]]; then
  # Session is pinned via /force-model. The "← selection · saved $X" clause
  # describes automatic routing and would be misleading on a manual pin, so
  # show the pinned model with a [forced] tag instead. forced_model comes from
  # the marker; fall back to the routed/selected id if parsing came up empty.
  forced_display="${forced_model:-${routed:-$selected_display}}"
  printf '%s — %s [forced]%s' "$brand" "$forced_display" "$tokens_clause"
elif [[ "$routed" == "failure" ]]; then
  # Latest turn was a CC-synthesized error stub — don't claim a routing
  # swap or compute savings against a non-model.
  printf '%s — %s%s' "$brand" "$routed" "$tokens_clause"
elif [[ -n "$routed" ]]; then
  # Always show the savings clause, flooring a non-positive total at $0.00.
  # session_savings is "0.0000" on fresh sessions or sessions where every
  # turn routed back to the selected model, and can go negative when routing
  # upgrades a turn to a pricier model (e.g. a sticky/hard-pinned side-call,
  # or a hard turn escalated to opus). "saved -$X" would mislead, so clamp
  # the display to $0.00 rather than dropping the clause — a $0.00 readout
  # tells the user the router ran and simply didn't beat their selection.
  # When the CC selection is unknown ("?" or empty) there's nothing to
  # compare against, so drop the "← selection" arrow and just show routed.
  display_savings="$session_savings"
  if [[ -z "$display_savings" ]] \
     || awk -v v="$display_savings" 'BEGIN{exit !(v+0 < 0)}'; then
    display_savings="0"
  fi
  if [[ -n "$selected_display" && "$selected_display" != "?" ]]; then
    printf '%s — %s ← %s · saved %s%s' \
      "$brand" "$routed" "$selected_display" "$(fmt_money "$display_savings")" "$tokens_clause"
  else
    printf '%s — %s%s' "$brand" "$routed" "$tokens_clause"
  fi
else
  printf '%s — %s%s' "$brand" "$selected_display" "$tokens_clause"
fi
STATUSLINE_EOF
chmod +x "$statusline_file"
ok "Statusline installed at $statusline_file"

# ---------- patch settings.json ----------

# Build the merge patch. Claude Code keeps its own Anthropic auth in
# Authorization/x-api-key; the router key rides in ANTHROPIC_CUSTOM_HEADERS.
# Project scope (no --dir) writes the key to settings.local.json (gitignored)
# so teammates can share settings.json. --dir and user scope inline the key
# directly into settings.json since there's no team to coordinate with.
tmp_patch="$(mktemp)"
# Compose with the spinner cleanup trap installed above — replacing it would
# leave the cursor hidden if Ctrl-C lands during settings.json patching.
trap '_spin_cleanup; rm -f "$tmp_patch"' EXIT INT TERM HUP

# write_claude_settings merges the router config into settings.json (and, in
# project scope, the key header into settings.local.json) for the key in $1.
# Factored out so the /validate fallback can rewrite both files with a
# replacement key without re-running the whole install.
write_claude_settings() {
  local block_key="$1"

  # Claude Code splits ANTHROPIC_CUSTOM_HEADERS on newlines, so multiple headers
  # ride in the same env var separated by \n. Append identity headers alongside
  # the router key so a single var carries them all. When email/name are empty
  # we keep the bare router-key form so a re-install for a user who opted out
  # cleanly removes the old line.
  local custom_headers="$router_key_header: $block_key"
  if [ -n "$user_email" ]; then
    custom_headers="$custom_headers"$'\n'"X-Weave-User-Email: $user_email"
  fi
  if [ -n "$user_name" ]; then
    custom_headers="$custom_headers"$'\n'"X-Weave-User-Name: $user_name"
  fi
  custom_headers="$custom_headers"$'\n'"X-App: claude-code"

  # Setting ANTHROPIC_BASE_URL makes Claude Code treat us as non-first-party and
  # disable MCP tool-search deferral, inlining every tool schema into every request
  # — a large uncompactable prefix that can push a session into an autocompact
  # thrash loop. ENABLE_TOOL_SEARCH=auto restores on-demand loading (Claude Code's
  # own first-party default).
  if [ "$scope" = "project" ] && [ -z "$install_dir" ]; then
    jq -n --arg url "$base_url" --arg sl "$statusline_path_for_settings" '{
      env: { ANTHROPIC_BASE_URL: $url, ENABLE_TOOL_SEARCH: "auto" },
      statusLine: { type: "command", command: $sl },
      attribution: {
        commit: "Co-Authored-By: Weave Router <router@workweave.ai>",
        pr: "🤖 Generated with [Weave Router](https://router.workweave.ai)"
      }
    }' >"$tmp_patch"
  else
    jq -n --arg url "$base_url" --arg header "$custom_headers" --arg sl "$statusline_path_for_settings" '{
      env: { ANTHROPIC_BASE_URL: $url, ANTHROPIC_CUSTOM_HEADERS: $header, ENABLE_TOOL_SEARCH: "auto" },
      statusLine: { type: "command", command: $sl },
      attribution: {
        commit: "Co-Authored-By: Weave Router <router@workweave.ai>",
        pr: "🤖 Generated with [Weave Router](https://router.workweave.ai)"
      }
    }' >"$tmp_patch"
  fi

  # Merge with existing settings. Deep-merge env and replace statusLine.
  # We strip router-owned auth from the existing settings BEFORE merging —
  # otherwise switching auth mode (key→dev-mode) would leave stale credentials
  # behind. ANTHROPIC_AUTH_TOKEN/apiKeyHelper are also removed to migrate older
  # installs that used them for router auth.
  local merged
  if [ -f "$settings_file" ]; then
    merged="$(jq -s '.[0] as $a | .[1] as $b
      | $a
      | .env = (($a.env // {} | del(.ANTHROPIC_AUTH_TOKEN, .ANTHROPIC_CUSTOM_HEADERS)) + ($b.env // {}))
      | (if (.env | length) == 0 then del(.env) else . end)
      | del(.apiKeyHelper)
      | (if $b.statusLine then .statusLine = $b.statusLine else . end)
      | (if $b.attribution then .attribution = $b.attribution else . end)
    ' "$settings_file" "$tmp_patch")"
    printf '%s\n' "$merged" >"$settings_file"
  else
    cp "$tmp_patch" "$settings_file"
  fi
  ok "Settings written to $settings_file"

  if [ "$scope" = "project" ] && [ -z "$install_dir" ]; then
    jq -n --arg header "$custom_headers" --arg router_url "$base_url" '{
      env: { ANTHROPIC_CUSTOM_HEADERS: $header, WEAVE_ROUTER_BASE_URL: $router_url }
    }' >"$tmp_patch"
    if [ -f "$local_settings_file" ]; then
      # Also strip ANTHROPIC_BASE_URL: `off` in project scope points this file
      # straight at Anthropic (the committed settings.json carries the router
      # URL instead). Leaving that override in place would have every request
      # keep bypassing the router even though this install/update just wrote
      # a fresh key and reports success.
      merged="$(jq -s '.[0] as $a | .[1] as $b
        | $a
        | .env = (($a.env // {} | del(.ANTHROPIC_AUTH_TOKEN, .ANTHROPIC_CUSTOM_HEADERS, .ANTHROPIC_BASE_URL)) + ($b.env // {}))
        | (if (.env | length) == 0 then del(.env) else . end)
        | del(.apiKeyHelper)
      ' "$local_settings_file" "$tmp_patch")"
      printf '%s\n' "$merged" >"$local_settings_file"
    else
      cp "$tmp_patch" "$local_settings_file"
    fi
    chmod 600 "$local_settings_file"
    ok "Router key header written to $local_settings_file"
  fi
}

# write_claude_settings rewrites the full router config live, so a parked
# sidecar left over from `off` is redundant afterward: the router is back on
# and the key/base URL it preserved are in the settings files. Leave it in
# place and `status` would keep reporting off, and `off` would no-op against
# live router config the user can no longer toggle. Removed AFTER the write
# succeeds (not before) — deleting it first would lose the only on-disk copy
# of the key if the write itself failed midway under `set -e`.
write_claude_settings "$api_key"

if [ "$target" = "claude" ] && [ -f "$settings_dir/.weave-parked.json" ]; then
  rm -f "$settings_dir/.weave-parked.json"
fi

# Slash command wrappers — see install_slash_commands() below for the why.
install_slash_commands "$settings_dir/commands"

# ---------- gitignore for project scope ----------

if [ "$scope" = "project" ] && [ -z "$install_dir" ] && [ -n "${git_root:-}" ]; then
  gitignore="$git_root/.gitignore"
  # Same symlink containment as the .claude/ paths above: a hostile repo could
  # commit .gitignore as a symlink so the >> below writes outside the repo.
  refuse_if_symlink "$gitignore"
  # Keep the statusline script and per-teammate local settings out of git. The
  # local settings carry the router key header; each teammate gets their own.
  for entry in \
    ".claude/settings.local.json" \
    ".claude/.credentials.json" \
    ".claude/cc-statusline.sh"
  do
    if [ ! -f "$gitignore" ] || ! grep -qxF "$entry" "$gitignore"; then
      printf '%s\n' "$entry" >>"$gitignore"
    fi
  done
  ok "Updated $gitignore (ignored credentials + local helpers)"
fi

# ---------- post-install verification ----------

verify_install

# ---------- done ----------

announce_done "Claude Code"
[ "$mode" = "update" ] || print_uninstall_hint
