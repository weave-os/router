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
claude_home="$work/claude"
mkdir -p "$claude_home"
install_for "$claude_home" claude --base-url "$hosted"
settings="$claude_home/.claude/settings.json"
beta_file="$claude_home/.claude/weave-beta.json"
launcher="$claude_home/.weave/bin/claude-beta"
[ -f "$beta_file" ] || fail "weave-beta.json was not written"
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$beta_file")" = "$beta_default" ] || fail "beta settings do not point at the beta host"
[ "$(jq -r '.env.ANTHROPIC_CUSTOM_HEADERS' "$beta_file")" = "$(jq -r '.env.ANTHROPIC_CUSTOM_HEADERS' "$settings")" ] \
  || fail "beta settings do not carry the same router key headers"
[ "$(jq -r '.env | has("WEAVE_ROUTER_BETA_URL")' "$beta_file")" = false ] || fail "default beta host was written as an override"
[ "$(stat -c %a "$beta_file" 2>/dev/null || stat -f %Lp "$beta_file")" = 600 ] || fail "weave-beta.json is not mode 600"
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$settings")" = "$hosted" ] || fail "stable settings were changed"
[ -x "$launcher" ] || fail "claude-beta launcher was not installed"
grep -Fq -- "--settings" "$launcher" || fail "claude-beta does not pass --settings"
grep -Fq "$beta_file" "$launcher" || fail "claude-beta does not point at weave-beta.json"

# The launcher runs claude with the beta settings and forwards arguments.
# shellcheck disable=SC2016 # the stub expands these when it runs
printf '%s\n' '#!/usr/bin/env bash' 'printf "%s\n" "$@" >"$CLAUDE_ARGS_OUT"' >"$work/bin/claude"
CLAUDE_ARGS_OUT="$work/claude-args" PATH="$work/bin:$PATH" "$launcher" --resume abc
[ "$(cat "$work/claude-args")" = "$(printf '%s\n' --settings "$beta_file" --resume abc)" ] || fail "claude-beta argv: $(cat "$work/claude-args")"
printf '%s\n' '#!/usr/bin/env bash' 'exit 0' >"$work/bin/claude"

# A user-owned file at the launcher path is left alone on reinstall.
user_home="$work/user-launcher"
mkdir -p "$user_home/.weave/bin"
printf '%s\n' '#!/usr/bin/env bash' 'echo mine' >"$user_home/.weave/bin/claude-beta"
install_for "$user_home" claude --base-url "$hosted"
[ "$(sed -n 2p "$user_home/.weave/bin/claude-beta")" = "echo mine" ] || fail "installer overwrote a user-owned claude-beta"

# A --dir install refuses a symlinked launcher directory.
dir_home="$work/dir-install"
mkdir -p "$dir_home/tree/.weave" "$work/elsewhere"
ln -s "$work/elsewhere" "$dir_home/tree/.weave/bin"
if HOME="$dir_home" XDG_CACHE_HOME="$dir_home/.cache" PATH="$work/bin:$PATH" NO_COLOR=1 WEAVE_ROUTER_KEY=rk_beta_test \
  bash "$installer" --claude --quiet --non-interactive --dir "$dir_home/tree" --base-url "$hosted" </dev/null >"$work/output" 2>&1; then
  fail "--dir install wrote through a symlinked .weave/bin"
fi
[ ! -e "$work/elsewhere/claude-beta" ] || fail "--dir install wrote the launcher through a symlink"

# --- beta on / status / off rewrites only the base URL. ---
router "$claude_home" beta status || fail "beta status failed"
grep -q 'stable' "$work/output" || fail "beta status did not report stable: $(cat "$work/output")"
router "$claude_home" beta on || fail "beta on failed"
grep -q 'Restart Claude Code' "$work/output" || fail "beta on did not ask for a restart"
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$settings")" = "$beta_default" ] || fail "beta on did not move the base URL"
[ "$(jq -r '.env.ANTHROPIC_CUSTOM_HEADERS' "$settings")" = "$(jq -r '.env.ANTHROPIC_CUSTOM_HEADERS' "$beta_file")" ] \
  || fail "beta on changed the router key"
router "$claude_home" beta status || fail "beta status failed"
grep -q 'beta' "$work/output" || fail "beta status did not report beta"
router "$claude_home" beta on || fail "repeat beta on failed"
grep -q 'already on beta' "$work/output" || fail "repeat beta on was not a no-op"
# `update` keeps the beta endpoint and still writes beta settings for it.
# Offline, update cannot verify the key and exits non-zero; the files it
# rewrote before that check are what matter here.
router "$claude_home" update --claude --quiet --scope user || true
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$settings")" = "$beta_default" ] || fail "update dropped the beta switch"
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$beta_file")" = "$beta_default" ] || fail "update rewrote beta settings off the beta host"
router "$claude_home" beta off || fail "beta off failed"
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$settings")" = "$hosted" ] || fail "beta off did not restore the stable URL"
[ ! -e "$claude_home/.claude/.weave-beta-switch.json" ] || fail "beta off left its sidecar"
if router "$claude_home" beta --codex on; then fail "beta accepted --codex"; fi
router "$claude_home" beta --status || fail "beta --status failed"
grep -q 'Claude Code: stable' "$work/output" || fail "beta --status did not report the channel: $(cat "$work/output")"
if router "$claude_home" beta sideways; then fail "beta accepted an unknown action"; fi

# beta on refuses while the router is off.
router "$claude_home" off --claude || fail "off failed"
router "$claude_home" beta on || true
grep -q "isn't routed" "$work/output" || fail "beta on while off did not refuse"
router "$claude_home" on --claude || fail "on failed"
# beta off while the router is off keeps the sidecar that restores stable.
router "$claude_home" beta on || fail "beta on failed"
router "$claude_home" off --claude || fail "off failed"
router "$claude_home" beta off || true
[ -e "$claude_home/.claude/.weave-beta-switch.json" ] || fail "beta off while off dropped its sidecar"
router "$claude_home" on --claude || fail "on failed"
router "$claude_home" beta off || fail "beta off failed"
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$settings")" = "$hosted" ] || fail "beta off after on did not restore stable"

# --- Self-hosted: no beta unless WEAVE_ROUTER_BETA_URL names one. ---
selfhosted_home="$work/selfhosted"
mkdir -p "$selfhosted_home"
install_for "$selfhosted_home" claude --base-url http://127.0.0.1:9
[ ! -e "$selfhosted_home/.claude/weave-beta.json" ] || fail "self-hosted install wrote beta settings without a beta host"
[ ! -e "$selfhosted_home/.weave/bin/claude-beta" ] || fail "self-hosted install wrote a launcher without a beta host"
router "$selfhosted_home" beta on || true
grep -q 'no beta channel' "$work/output" || fail "beta on without a beta host did not explain"
WEAVE_ROUTER_BETA_URL=http://127.0.0.2:9 install_for "$selfhosted_home" claude --base-url http://127.0.0.1:9
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$selfhosted_home/.claude/weave-beta.json")" = http://127.0.0.2:9 ] || fail "WEAVE_ROUTER_BETA_URL was not used"
[ "$(jq -r '.env.WEAVE_ROUTER_BETA_URL' "$selfhosted_home/.claude/weave-beta.json")" = http://127.0.0.2:9 ] || fail "beta override not carried for the status line"
# beta on|off find the saved self-hosted beta endpoint without the env var,
# and beta off restores the stable URL instead of claiming it is stable.
selfhosted_settings="$selfhosted_home/.claude/settings.json"
router "$selfhosted_home" beta on || fail "self-hosted beta on failed"
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$selfhosted_settings")" = http://127.0.0.2:9 ] || fail "self-hosted beta on did not use the saved beta host"
router "$selfhosted_home" beta off || fail "self-hosted beta off failed"
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$selfhosted_settings")" = http://127.0.0.1:9 ] || fail "self-hosted beta off did not restore stable: $(cat "$work/output")"
# Without the saved stable URL, beta off never falls back to the hosted router.
router "$selfhosted_home" beta on || fail "self-hosted beta on failed"
rm -f "$selfhosted_home/.claude/.weave-beta-switch.json"
router "$selfhosted_home" beta off || true
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$selfhosted_settings")" = http://127.0.0.2:9 ] || fail "beta off guessed a stable endpoint for a self-hosted install"
grep -q 'stable endpoint is missing' "$work/output" || fail "beta off without the sidecar did not explain"
# A sidecar left from a hosted `beta on` never picks the beta host after the
# install moves to a self-hosted router.
printf '{"stable_base_url":"%s"}\n' "$hosted" >"$selfhosted_home/.claude/.weave-beta-switch.json"
jq '.env.ANTHROPIC_BASE_URL = "http://127.0.0.1:9"' "$selfhosted_settings" >"$work/settings.tmp" && mv "$work/settings.tmp" "$selfhosted_settings"
rm -f "$selfhosted_home/.claude/weave-beta.json"
router "$selfhosted_home" beta on || true
[ "$(jq -r '.env.ANTHROPIC_BASE_URL' "$selfhosted_settings")" = http://127.0.0.1:9 ] || fail "beta on took the beta host from a stale sidecar"
rm -f "$selfhosted_home/.claude/.weave-beta-switch.json"
# Retargeting to a router with no beta channel removes the old beta files.
install_for "$selfhosted_home" claude --base-url http://127.0.0.1:9
[ ! -e "$selfhosted_home/.claude/weave-beta.json" ] || fail "retarget kept weave-beta.json for the old beta host"
[ ! -e "$selfhosted_home/.weave/bin/claude-beta" ] || fail "retarget kept the claude-beta launcher"
printf '{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.3:9"},"model":"mine"}\n' >"$selfhosted_home/.claude/weave-beta.json"
install_for "$selfhosted_home" claude --base-url http://127.0.0.1:9
[ "$(jq -r '.model' "$selfhosted_home/.claude/weave-beta.json")" = mine ] || fail "install removed a user-owned weave-beta.json"
uninstall_for "$selfhosted_home" claude
[ -e "$selfhosted_home/.claude/weave-beta.json" ] || fail "uninstall removed a user-owned weave-beta.json"

# Project scope never writes personal beta assets.
project_home="$work/project"
mkdir -p "$project_home/repo"
git -C "$project_home/repo" init -q
( cd "$project_home/repo" && HOME="$project_home" XDG_CACHE_HOME="$project_home/.cache" PATH="$work/bin:$PATH" NO_COLOR=1 WEAVE_ROUTER_KEY=rk_beta_test \
  bash "$installer" --claude --quiet --non-interactive --scope project --base-url "$hosted" </dev/null >"$work/output" 2>&1 ) \
  || { cat "$work/output" >&2; fail "project install"; }
[ ! -e "$project_home/repo/.claude/weave-beta.json" ] || fail "project install wrote weave-beta.json"

# --- Uninstall removes the beta assets. ---
router "$claude_home" beta on || fail "beta on failed"
uninstall_for "$claude_home" claude
[ ! -e "$beta_file" ] || fail "uninstall left weave-beta.json"
[ ! -e "$launcher" ] || fail "uninstall left claude-beta"
[ ! -e "$claude_home/.claude/.weave-beta-switch.json" ] || fail "uninstall left the beta switch sidecar"
ln -sf "$work/elsewhere-beta.json" "$user_home/.claude/weave-beta.json"
uninstall_for "$user_home" claude
[ ! -L "$user_home/.claude/weave-beta.json" ] || fail "uninstall left a symlinked weave-beta.json"
[ -e "$user_home/.weave/bin/claude-beta" ] || fail "uninstall removed a user-owned claude-beta"

# --- Codex: a weave-beta provider and profile, no jq needed. ---
codex_home="$work/codex"
mkdir -p "$codex_home"
install_for "$codex_home" codex --base-url "$hosted"
config="$codex_home/.codex/config.toml"
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
install_for "$codex_home" codex --base-url "$hosted"
[ "$(grep -c '^\[profiles\.weave-beta\]$' "$config")" -eq 1 ] || fail "reinstall duplicated the weave-beta profile"
uninstall_for "$codex_home" codex
if grep -q 'weave-beta' "$config" 2>/dev/null; then fail "Codex uninstall left the beta profile"; fi

# --- Status line: BETA badge on the beta host, release SHA when known. ---
statusline_home="$work/statusline"
mkdir -p "$statusline_home/cache"
render() { # render <base-url> [extra env...]
  local base="$1"
  shift
  printf '%s' '{"session_id":"s1","model":{"id":"claude-opus-5"}}' \
    | env "$@" HOME="$statusline_home" XDG_CACHE_HOME="$statusline_home/cache" WEAVE_DISPLAY_SETTINGS_COLD_WAIT_TENTHS=0 \
      PATH="$work/bin:$PATH" ANTHROPIC_BASE_URL="$base" bash "$statusline" 2>/dev/null \
    | sed 's/\x1b\[[0-9;]*m//g'
}
out="$(render "$hosted")"
case "$out" in *BETA*) fail "stable session shows BETA: $out" ;; esac
out="$(render "$beta_default")"
case "$out" in *"WEAVE ROUTER · BETA"*) ;; *) fail "beta session lacks BETA badge: $out" ;; esac
mkdir -p "$statusline_home/cache/weave-router"
weave_release_lib="$(sed -n '/^weave_url_key() {/,/^weave_channel_clause() {/p' "$statusline" | sed '$d')"
release_cache_for() { # release_cache_for <cache-home> <base-url>
  ( XDG_CACHE_HOME="$1"; eval "$weave_release_lib"; weave_release_cache "$2" )
}
printf 'abcdef1' >"$(release_cache_for "$statusline_home/cache" "$beta_default")"
out="$(render "$beta_default")"
case "$out" in *"BETA abcdef1"*) ;; *) fail "beta badge lacks the release SHA: $out" ;; esac
out="$(render http://127.0.0.2:9 WEAVE_ROUTER_BETA_URL=http://127.0.0.2:9)"
case "$out" in *BETA*) ;; *) fail "beta override host lacks BETA badge: $out" ;; esac
out="$(render http://127.0.0.2:8 WEAVE_ROUTER_BETA_URL=http://127.0.0.2:9)"
case "$out" in *BETA*) fail "a stable router on the beta host's other port shows BETA: $out" ;; esac

# The release header is read from the display-settings response.
(
  XDG_CACHE_HOME="$statusline_home/cache2"
  mkdir -p "$XDG_CACHE_HOME/weave-router"
  export WEAVE_RELEASE_HEADERS='x-weave-release-sha|x-weave-policy-sha'
  eval "$weave_release_lib"
  printf 'HTTP/2 200\r\nX-Weave-Release-SHA: 0123456789ABCDEF\r\n\r\n' >"$statusline_home/good_headers"
  weave_store_release "$statusline_home/good_headers" "$beta_default"
  release_file="$(weave_release_cache "$beta_default")"
  [ "$(cat "$release_file")" = 0123456 ] || exit 1
  [ "$(weave_release_cache "$hosted")" != "$release_file" ] || exit 1
  printf 'HTTP/2 200\r\nX-Weave-Release-SHA: not-a-sha\r\n\r\n' >"$statusline_home/bad_headers"
  weave_store_release "$statusline_home/bad_headers" "$beta_default"
  [ ! -e "$release_file" ] || exit 1
) || fail "release header capture"

echo "Beta channel installer regression tests passed"
