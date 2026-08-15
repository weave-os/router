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
[ -f "$installer" ] || { echo "cannot find installer at $installer" >&2; exit 1; }

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

# --quiet downgrades that to a warning: quiet callers opted out of the noise
# and out of a nonzero exit for a config refresh that did land.
run "$upd_home" -- update --claude --quiet
check "quiet update exits 0 despite the rejection" "$?" 0

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

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
