#!/usr/bin/env bash
#
# Regression tests for Claude Code key reuse, --rotate-key, and `update`.
#
# Fully offline: an isolated $HOME keeps real config untouched and a fake curl
# (always exit 22) stands in for the /health + /validate probes, so no case
# reaches the network. Run directly or via `make test-install`.

set -uo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
uninstaller="${UNINSTALLER:-$script_dir/../uninstall.sh}"
[ -f "$installer" ] || { echo "cannot find installer at $installer" >&2; exit 1; }
[ -f "$uninstaller" ] || { echo "cannot find uninstaller at $uninstaller" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fake_bin="$work/bin"
mkdir -p "$fake_bin"
printf '%s\n' '#!/usr/bin/env bash' 'exit 22' >"$fake_bin/curl"
chmod +x "$fake_bin/curl"
test_path="$fake_bin:$PATH"

pass=0
fail=0
ok() { echo "  ok   $1"; pass=$((pass + 1)); }
no() { echo "  FAIL $1"; echo "         expected: $2"; echo "         actual:   $3"; fail=$((fail + 1)); }

check() { # check <name> <actual> <expected>
  if [ "$2" = "$3" ]; then ok "$1"; else no "$1" "$3" "$2"; fi
}

# run <home> [env KEY=...] -- <installer args...>
# Runs the installer with an isolated HOME and no controlling terminal on stdin,
# so any code path that would prompt fails loudly instead of hanging the suite.
run() {
  local home="$1"; shift
  local key=""
  if [ "${1:-}" != "--" ]; then key="$1"; shift; fi
  [ "${1:-}" = "--" ] && shift
  HOME="$home" XDG_CACHE_HOME="$home/.cache" PATH="$test_path" NO_COLOR=1 \
    WEAVE_ROUTER_KEY="$key" \
    bash "$installer" "$@" --base-url http://127.0.0.1:9 </dev/null >/dev/null 2>&1
}

installed_key() { # installed_key <settings_file>
  jq -r '.env.ANTHROPIC_CUSTOM_HEADERS // ""' "$1" 2>/dev/null \
    | sed -n 's/^X-Weave-Router-Key: //p'
}

echo "install.sh key reuse"

# ---------- user scope ----------

home="$work/user"; mkdir -p "$home"
settings="$home/.claude/settings.json"

run "$home" rk_first -- --claude --scope user --quiet --non-interactive
check "first install stores the env key" "$(installed_key "$settings")" "rk_first"

# The complaint this exists for: re-running to pick up new assets must not
# demand the key again. Without read-back this is a hard exit 1.
run "$home" -- --claude --scope user --quiet --non-interactive
check "re-run with no env var succeeds" "$?" 0
check "re-run keeps the installed key" "$(installed_key "$settings")" "rk_first"

# Env must stay the highest-precedence source: a rotated key exported by the
# user cannot be silently ignored in favor of the stale one on disk.
run "$home" rk_second -- --claude --scope user --quiet --non-interactive
check "env key overwrites the installed key" "$(installed_key "$settings")" "rk_second"

# --rotate-key deliberately skips read-back. With no env and no tty there is
# nothing to rotate to, so it must fail rather than reuse or hang.
run "$home" -- --claude --scope user --quiet --non-interactive --rotate-key
check "--rotate-key with no key source fails" "$?" 1
check "--rotate-key failure leaves the key intact" "$(installed_key "$settings")" "rk_second"

# ---------- project scope ----------
#
# The key lives in the gitignored settings.local.json here, not settings.json,
# so read-back has to look in a different place than user scope.

proj_home="$work/project-home"; proj="$work/project"
mkdir -p "$proj_home" "$proj"
git -C "$proj" init -q .
local_settings="$proj/.claude/settings.local.json"

( cd "$proj" && run "$proj_home" rk_proj -- --claude --scope project --quiet --non-interactive )
check "project install stores the key locally" "$(installed_key "$local_settings")" "rk_proj"
( cd "$proj" && run "$proj_home" -- --claude --scope project --quiet --non-interactive )
check "project re-run reuses settings.local.json key" "$(installed_key "$local_settings")" "rk_proj"
# Project uninstall must remove both the active router config and the private
# endpoint marker without touching unrelated Claude settings.
project_settings="$proj/.claude/settings.json"
printf '%s\n' '{"model":"opus[1m]","hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"telemetry"}]}]}}' >"$project_settings"
( cd "$proj" && run "$proj_home" rk_proj -- --claude --scope project --quiet --non-interactive )
check "project install writes the endpoint marker" \
  "$(jq -r '.env.WEAVE_ROUTER_BASE_URL // ""' "$local_settings")" "http://127.0.0.1:9"

jq '.env.KEEP_ME = "preserve"' "$local_settings" >"$local_settings.tmp" && mv "$local_settings.tmp" "$local_settings"
( cd "$proj" && HOME="$proj_home" PATH="$test_path" NO_COLOR=1 \
    bash "$uninstaller" --claude --scope project </dev/null >/dev/null 2>&1 )
check "project uninstall succeeds" "$?" 0
check "project uninstall removes the endpoint marker" \
  "$(jq -r '.env.WEAVE_ROUTER_BASE_URL // ""' "$local_settings")" ""
check "project uninstall removes the router key" "$(installed_key "$local_settings")" ""
check "project uninstall preserves unrelated local env" \
  "$(jq -r '.env.KEEP_ME // ""' "$local_settings")" "preserve"
check "project uninstall preserves the model" \
  "$(jq -r '.model // ""' "$project_settings")" "opus[1m]"
check "project uninstall preserves hooks" \
  "$(jq -r '.hooks.PostToolUse[0].hooks[0].command // ""' "$project_settings")" "telemetry"

# Repeating uninstall is a supported no-op and must not remove the unrelated
# local setting left above.
( cd "$proj" && HOME="$proj_home" PATH="$test_path" NO_COLOR=1 \
    bash "$uninstaller" --claude --scope project </dev/null >/dev/null 2>&1 )
check "project uninstall is idempotent" "$?" 0
check "second uninstall preserves unrelated local env" \
  "$(jq -r '.env.KEEP_ME // ""' "$local_settings")" "preserve"

# ---------- update ----------

upd_home="$work/update"; mkdir -p "$upd_home"
upd_settings="$upd_home/.claude/settings.json"

# update against a machine with no install must error, not prompt or no-op.
run "$upd_home" -- update --claude
check "update with no installed key errors" "$?" 1
[ ! -f "$upd_settings" ] && ok "update with no key wrote no settings" \
  || no "update with no key wrote no settings" "no settings.json" "file exists"

run "$upd_home" rk_upd -- --claude --scope user --quiet --non-interactive
# The fake curl rejects /validate, and update treats that as fatal (unlike
# install's warn) so cron catches a revoked key instead of logging past it.
run "$upd_home" -- update --claude
check "update surfaces a rejected key as an error" "$?" 1
check "update still refreshed the config" "$(installed_key "$upd_settings")" "rk_upd"

# --quiet downgrades the message to a warning but must not downgrade the exit
# code: a cron job checks $? for success, and a scheduled run that reports 0
# here would silently hide a revoked key until the router starts 401ing.
run "$upd_home" -- update --claude --quiet
check "quiet update still exits nonzero on rejection" "$?" 1

# update never prompts, so it has no use for a flag whose whole job is to force
# one. Accepting it silently would look like rotation happened.
run "$upd_home" -- update --claude --rotate-key
check "update rejects --rotate-key" "$?" 2

# Only Claude Code is wired up; the other targets must say so rather than
# half-running an install path that still expects a prompt.
run "$upd_home" -- update --codex
check "update rejects an unsupported target" "$?" 2

# ---------- command baseline seeding ----------
#
# The statusline's background wrapper refresh only swaps a file whose bytes
# match the last canonical copy. install.sh seeds that baseline so a fresh
# install doesn't burn an interval establishing one. Both sides key the cache
# on the physical command-dir path, so resolve it the same way here — on macOS
# mktemp hands back /var/... for /private/var/....
cmd_dir_real="$(cd "$upd_home/.claude/commands" && pwd -P)"
baseline="$upd_home/.cache/weave-router/commands$(printf '%s' "$cmd_dir_real" | tr -c 'A-Za-z0-9._-' '_')"
if [ -f "$baseline/force-model.md" ] && [ -f "$baseline/router-off.md" ]; then
  ok "install seeds the slash-command baseline"
else
  no "install seeds the slash-command baseline" "canonical wrappers cached" "missing under $baseline"
fi
# Baselines are the UNRENDERED canonical files: the refresh renders {{SCOPE}}
# per install, and a pre-rendered baseline would never match upstream.
if grep -q '{{SCOPE}}' "$baseline/router-off.md" 2>/dev/null; then
  ok "seeded baseline keeps the {{SCOPE}} placeholder"
else
  no "seeded baseline keeps the {{SCOPE}} placeholder" "unrendered copy" "placeholder substituted"
fi

# ---------- update after `off`: parked sidecar key + base URL carry-over ----------
#
# `off` moves the router URL and key header out of settings.json/settings.local.json
# and into .weave-parked.json. A re-run while toggled off must still find the key
# there (not demand a fresh paste) and must not silently retarget the endpoint at
# the hosted default just because --base-url wasn't repeated.
#
# It must also leave the toggle in a coherent state: an update rewrites the full
# router config live, so it re-enables the router and has to consume the sidecar.
# Leaving it behind desyncs the toggle — `status` would keep reporting off while
# traffic routes through the router, and `off` would no-op instead of undoing it.

off_home="$work/off"; mkdir -p "$off_home"
off_settings="$off_home/.claude/settings.json"
off_parked="$off_home/.claude/.weave-parked.json"
custom_url="http://custom-router.internal:9999"

HOME="$off_home" XDG_CACHE_HOME="$off_home/.cache" PATH="$test_path" NO_COLOR=1 \
  WEAVE_ROUTER_KEY="rk_off" \
  bash "$installer" --claude --scope user --quiet --non-interactive --base-url "$custom_url" </dev/null >/dev/null 2>&1
HOME="$off_home" PATH="$test_path" NO_COLOR=1 \
  bash "$installer" off --claude </dev/null >/dev/null 2>&1
check "off parks the router key, not settings.json" "$(installed_key "$off_settings")" ""

# Direct invocation, not the `run` helper — `run` always appends its own
# --base-url, which would make base_url_explicit=true and defeat the very
# carry-over behavior this checks.
HOME="$off_home" XDG_CACHE_HOME="$off_home/.cache" PATH="$test_path" NO_COLOR=1 \
  bash "$installer" update --claude </dev/null >/dev/null 2>&1
check "update while off recovers the parked key into settings.json" \
  "$(installed_key "$off_settings")" "rk_off"
check "update while off preserves the custom base URL, not the hosted default" \
  "$(jq -r '.env.ANTHROPIC_BASE_URL // ""' "$off_settings")" "$custom_url"

# The sidecar has to be gone, or the toggle is desynced from what's live.
[ ! -f "$off_parked" ] && ok "update while off consumes the parked sidecar" \
  || no "update while off consumes the parked sidecar" "sidecar removed" "file still present"

# The real symptom of a desync: `off` silently no-ops because the stale sidecar
# makes it think it's already off, leaving router config live with no way back.
HOME="$off_home" PATH="$test_path" NO_COLOR=1 \
  bash "$installer" off --claude </dev/null >/dev/null 2>&1
check "off still works after an update that ran while off" \
  "$(installed_key "$off_settings")" ""

# ---------- project scope: update while off must drop the direct override ----------
#
# In project scope `off` parks settings.local.json's env and then overrides
# ANTHROPIC_BASE_URL to Anthropic *in that same file*, since the committed
# settings.json (shared by the team) never carries the off state. write_claude_settings'
# local-file merge has to strip that override same as it strips the old auth
# headers, or it silently wins over the freshly-written router URL and a
# "successful" update leaves Claude Code talking straight to Anthropic.

proj_off_home="$work/project-off"; proj_off="$work/project-off-repo"
mkdir -p "$proj_off_home" "$proj_off"
git -C "$proj_off" init -q .
proj_off_settings="$proj_off/.claude/settings.json"
proj_off_local="$proj_off/.claude/settings.local.json"

( cd "$proj_off" && HOME="$proj_off_home" XDG_CACHE_HOME="$proj_off_home/.cache" PATH="$test_path" NO_COLOR=1 \
    WEAVE_ROUTER_KEY="rk_proj_off" \
    bash "$installer" --claude --scope project --quiet --non-interactive </dev/null >/dev/null 2>&1 )
( cd "$proj_off" && HOME="$proj_off_home" PATH="$test_path" NO_COLOR=1 \
    bash "$installer" off --claude --scope project </dev/null >/dev/null 2>&1 )
check "project off overrides settings.local.json's base URL" \
  "$(jq -r '.env.ANTHROPIC_BASE_URL // ""' "$proj_off_local")" "https://api.anthropic.com"

( cd "$proj_off" && HOME="$proj_off_home" XDG_CACHE_HOME="$proj_off_home/.cache" PATH="$test_path" NO_COLOR=1 \
    bash "$installer" update --claude --scope project </dev/null >/dev/null 2>&1 )
check "update while off (project scope) recovers the key into settings.local.json" \
  "$(installed_key "$proj_off_local")" "rk_proj_off"
check "update while off (project scope) drops the direct-to-Anthropic override" \
  "$(jq -r '.env.ANTHROPIC_BASE_URL // ""' "$proj_off_local")" ""
check "update while off (project scope) leaves the router URL live in settings.json" \
  "$(jq -r '.env.ANTHROPIC_BASE_URL // ""' "$proj_off_settings")" "https://router.workweave.ai"

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
