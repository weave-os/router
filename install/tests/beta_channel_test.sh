#!/usr/bin/env bash
#
# Offline regression tests for direct-client beta access: the claude-beta
# launcher and weave-beta.json, `beta on|off|status`, the Codex weave-beta
# profile, the status-line BETA badge, and their removal on uninstall.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
uninstaller="${UNINSTALLER:-$script_dir/../uninstall.sh}"
statusline="$script_dir/../cc-statusline.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin"
printf '%s\n' '#!/usr/bin/env bash' 'exit 22' >"$work/bin/curl"
printf '%s\n' '#!/usr/bin/env bash' 'exit 0' >"$work/bin/claude"
printf '%s\n' '#!/usr/bin/env bash' 'exit 0' >"$work/bin/codex"
chmod +x "$work/bin/curl" "$work/bin/claude" "$work/bin/codex"

hosted="https://router.weaveos.com"
beta_default="https://beta.router.workweave.ai"

fail() { echo "FAIL: $*" >&2; exit 1; }

install_for() { # install_for <home> <target> [args...]
  local home="$1" target="$2"
  shift 2
  HOME="$home" XDG_CACHE_HOME="$home/.cache" PATH="$work/bin:$PATH" ANTHROPIC_MODEL="" \
    NO_COLOR=1 WEAVE_ROUTER_KEY=rk_beta_test \
    bash "$installer" "--$target" --quiet --non-interactive --scope user "$@" </dev/null >"$work/output" 2>&1 \
    || { cat "$work/output" >&2; fail "install --$target $*"; }
}
router() { # router <home> <args...>
  local home="$1"
  shift
  HOME="$home" XDG_CACHE_HOME="$home/.cache" PATH="$work/bin:$PATH" NO_COLOR=1 \
    bash "$installer" "$@" </dev/null >"$work/output" 2>&1
}
uninstall_for() {
  local home="$1" target="$2"
  HOME="$home" PATH="$work/bin:$PATH" NO_COLOR=1 bash "$uninstaller" "--$target" --scope user </dev/null >"$work/output" 2>&1 \
    || { cat "$work/output" >&2; fail "uninstall --$target"; }
}

# --- Claude Code: hosted install writes the beta settings and launcher. ---
h="$work/claude"
mkdir -p "$h"
install_for "$h" claude --base-url "$hosted"
settings="$h/.claude/settings.json"
beta="$h/.claude/weave-beta.json"
launcher="$h/.weave/bin/claude-beta"
[ -f "$beta" ] || fail "weave-beta.json was not written"
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$beta")" = "$beta_default" ] || fail "beta settings do not point at the beta host"
[ "$(jq -r '.env.ANTHROPIC_CUSTOM_HEADERS' "$beta")" = "$(jq -r '.env.ANTHROPIC_CUSTOM_HEADERS' "$settings")" ] \
  || fail "beta settings do not carry the same router key headers"
[ "$(jq -r '.env | has("WEAVE_ROUTER_BETA_URL")' "$beta")" = false ] || fail "default beta host was written as an override"
[ "$(stat -f %Lp "$beta" 2>/dev/null || stat -c %a "$beta")" = 600 ] || fail "weave-beta.json is not mode 600"
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$settings")" = "$hosted" ] || fail "stable settings were changed"
[ -x "$launcher" ] || fail "claude-beta launcher was not installed"
grep -Fq -- "--settings" "$launcher" || fail "claude-beta does not pass --settings"
grep -Fq "$beta" "$launcher" || fail "claude-beta does not point at weave-beta.json"

# The launcher runs claude with the beta settings and forwards arguments.
# shellcheck disable=SC2016 # the stub expands these when it runs
printf '%s\n' '#!/usr/bin/env bash' 'printf "%s\n" "$@" >"$CLAUDE_ARGS_OUT"' >"$work/bin/claude"
CLAUDE_ARGS_OUT="$work/claude-args" PATH="$work/bin:$PATH" "$launcher" --resume abc
[ "$(cat "$work/claude-args")" = "$(printf '%s\n' --settings "$beta" --resume abc)" ] || fail "claude-beta argv: $(cat "$work/claude-args")"
printf '%s\n' '#!/usr/bin/env bash' 'exit 0' >"$work/bin/claude"

# A user-owned file at the launcher path is left alone on reinstall.
u="$work/user-launcher"
mkdir -p "$u/.weave/bin"
printf '%s\n' '#!/usr/bin/env bash' 'echo mine' >"$u/.weave/bin/claude-beta"
install_for "$u" claude --base-url "$hosted"
[ "$(sed -n 2p "$u/.weave/bin/claude-beta")" = "echo mine" ] || fail "installer overwrote a user-owned claude-beta"

# --- beta on / status / off rewrites only the base URL. ---
router "$h" beta status || fail "beta status failed"
grep -q 'stable' "$work/output" || fail "beta status did not report stable: $(cat "$work/output")"
router "$h" beta on || fail "beta on failed"
grep -q 'Restart Claude Code' "$work/output" || fail "beta on did not ask for a restart"
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$settings")" = "$beta_default" ] || fail "beta on did not move the base URL"
[ "$(jq -r '.env.ANTHROPIC_CUSTOM_HEADERS' "$settings")" = "$(jq -r '.env.ANTHROPIC_CUSTOM_HEADERS' "$beta")" ] \
  || fail "beta on changed the router key"
router "$h" beta status || fail "beta status failed"
grep -q 'beta' "$work/output" || fail "beta status did not report beta"
router "$h" beta on || fail "repeat beta on failed"
grep -q 'already on beta' "$work/output" || fail "repeat beta on was not a no-op"
# `update` keeps the beta endpoint and still writes beta settings for it.
# Offline, update cannot verify the key and exits non-zero; the files it
# rewrote before that check are what matter here.
router "$h" update --claude --quiet --scope user || true
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$settings")" = "$beta_default" ] || fail "update dropped the beta switch"
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$beta")" = "$beta_default" ] || fail "update rewrote beta settings off the beta host"
router "$h" beta off || fail "beta off failed"
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$settings")" = "$hosted" ] || fail "beta off did not restore the stable URL"
[ ! -e "$h/.claude/.weave-beta-switch.json" ] || fail "beta off left its sidecar"
if router "$h" beta --codex on; then fail "beta accepted --codex"; fi
if router "$h" beta sideways; then fail "beta accepted an unknown action"; fi

# beta on refuses while the router is off.
router "$h" off --claude || fail "off failed"
router "$h" beta on || true
grep -q "isn't routed" "$work/output" || fail "beta on while off did not refuse"
router "$h" on --claude || fail "on failed"

# --- Self-hosted: no beta unless WEAVE_ROUTER_BETA_URL names one. ---
s="$work/selfhosted"
mkdir -p "$s"
install_for "$s" claude --base-url http://127.0.0.1:9
[ ! -e "$s/.claude/weave-beta.json" ] || fail "self-hosted install wrote beta settings without a beta host"
[ ! -e "$s/.weave/bin/claude-beta" ] || fail "self-hosted install wrote a launcher without a beta host"
router "$s" beta on || true
grep -q 'no beta channel' "$work/output" || fail "beta on without a beta host did not explain"
WEAVE_ROUTER_BETA_URL=http://127.0.0.2:9 install_for "$s" claude --base-url http://127.0.0.1:9
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$s/.claude/weave-beta.json")" = http://127.0.0.2:9 ] || fail "WEAVE_ROUTER_BETA_URL was not used"
[ "$(jq -r '.env.WEAVE_ROUTER_BETA_URL' "$s/.claude/weave-beta.json")" = http://127.0.0.2:9 ] || fail "beta override not carried for the status line"

# Project scope never writes personal beta assets.
p="$work/project"
mkdir -p "$p/repo"
git -C "$p/repo" init -q
( cd "$p/repo" && HOME="$p" XDG_CACHE_HOME="$p/.cache" PATH="$work/bin:$PATH" NO_COLOR=1 WEAVE_ROUTER_KEY=rk_beta_test \
  bash "$installer" --claude --quiet --non-interactive --scope project --base-url "$hosted" </dev/null >"$work/output" 2>&1 ) \
  || { cat "$work/output" >&2; fail "project install"; }
[ ! -e "$p/repo/.claude/weave-beta.json" ] || fail "project install wrote weave-beta.json"

# --- Uninstall removes the beta assets. ---
router "$h" beta on || fail "beta on failed"
uninstall_for "$h" claude
[ ! -e "$beta" ] || fail "uninstall left weave-beta.json"
[ ! -e "$launcher" ] || fail "uninstall left claude-beta"
[ ! -e "$h/.claude/.weave-beta-switch.json" ] || fail "uninstall left the beta switch sidecar"
uninstall_for "$u" claude
[ -e "$u/.weave/bin/claude-beta" ] || fail "uninstall removed a user-owned claude-beta"

# --- Codex: a weave-beta provider and profile, no jq needed. ---
c="$work/codex"
mkdir -p "$c"
install_for "$c" codex --base-url "$hosted"
config="$c/.codex/config.toml"
grep -qx '\[model_providers.weave-beta\]' "$config" || fail "Codex beta provider missing"
grep -qx "base_url = \"$beta_default/v1\"" "$config" || fail "Codex beta provider does not use the beta host"
grep -qx '\[profiles.weave-beta\]' "$config" || fail "Codex weave-beta profile missing"
grep -qx 'model_provider = "weave-beta"' "$config" || fail "weave-beta profile does not select the beta provider"
[ "$(grep -c 'X-Weave-Router-Key' "$config")" -eq 2 ] || fail "Codex beta provider does not carry the router key"
if command -v python3 >/dev/null 2>&1 && python3 -c 'import tomllib' 2>/dev/null; then
  python3 - "$config" <<'PY' || fail "Codex config with beta profile is not valid TOML"
import sys, tomllib
with open(sys.argv[1], "rb") as f:
    config = tomllib.load(f)
assert config["model_provider"] == "weave", config.get("model_provider")
assert config["profiles"]["weave-beta"]["model_provider"] == "weave-beta"
assert config["model_providers"]["weave-beta"]["base_url"].startswith("https://beta.")
PY
fi
install_for "$c" codex --base-url "$hosted"
[ "$(grep -c '^\[profiles\.weave-beta\]$' "$config")" -eq 1 ] || fail "reinstall duplicated the weave-beta profile"
uninstall_for "$c" codex
if grep -q 'weave-beta' "$config" 2>/dev/null; then fail "Codex uninstall left the beta profile"; fi

# --- Status line: BETA badge on the beta host, release SHA when known. ---
sl="$work/statusline"
mkdir -p "$sl/cache"
render() { # render <base-url> [extra env...]
  local base="$1"
  shift
  printf '%s' '{"session_id":"s1","model":{"id":"claude-opus-5"}}' \
    | env "$@" HOME="$sl" XDG_CACHE_HOME="$sl/cache" WEAVE_DISPLAY_SETTINGS_COLD_WAIT_TENTHS=0 \
      PATH="$work/bin:$PATH" ANTHROPIC_BASE_URL="$base" bash "$statusline" 2>/dev/null \
    | sed 's/\x1b\[[0-9;]*m//g'
}
out="$(render "$hosted")"
case "$out" in *BETA*) fail "stable session shows BETA: $out" ;; esac
out="$(render "$beta_default")"
case "$out" in *"WEAVE ROUTER · BETA"*) ;; *) fail "beta session lacks BETA badge: $out" ;; esac
mkdir -p "$sl/cache/weave-router"
printf 'abcdef1' >"$sl/cache/weave-router/release-beta.router.workweave.ai"
out="$(render "$beta_default")"
case "$out" in *"BETA abcdef1"*) ;; *) fail "beta badge lacks the release SHA: $out" ;; esac
out="$(render http://127.0.0.2:9 WEAVE_ROUTER_BETA_URL=http://127.0.0.2:9)"
case "$out" in *BETA*) ;; *) fail "beta override host lacks BETA badge: $out" ;; esac

# The release header is read from the display-settings response.
weave_release_lib="$(sed -n '/^weave_url_host() {/,/^weave_channel_clause() {/p' "$statusline" | sed '$d')"
(
  XDG_CACHE_HOME="$sl/cache2"
  mkdir -p "$XDG_CACHE_HOME/weave-router"
  export WEAVE_RELEASE_HEADERS='x-weave-release-sha|x-weave-policy-sha'
  eval "$weave_release_lib"
  printf 'HTTP/2 200\r\nX-Weave-Release-SHA: 0123456789ABCDEF\r\n\r\n' >"$sl/h1"
  weave_store_release "$sl/h1" "$beta_default"
  [ "$(cat "$XDG_CACHE_HOME/weave-router/release-beta.router.workweave.ai")" = 0123456 ] || exit 1
  printf 'HTTP/2 200\r\nX-Weave-Release-SHA: not-a-sha\r\n\r\n' >"$sl/h2"
  weave_store_release "$sl/h2" "$beta_default"
  [ ! -e "$XDG_CACHE_HOME/weave-router/release-beta.router.workweave.ai" ] || exit 1
) || fail "release header capture"

echo "Beta channel installer regression tests passed"
