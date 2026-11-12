#!/usr/bin/env bash
#
# The installer records the model a client ran before Weave Auto as
# X-Weave-Prior-Model, and later installs keep that first record.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
uninstaller="${UNINSTALLER:-$script_dir/../uninstall.sh}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin"
printf '%s\n' '#!/usr/bin/env bash' 'exit 22' >"$work/bin/curl"
chmod +x "$work/bin/curl"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

claude() {
  local home="$1" shell_model="$2"
  shift 2
  HOME="$home" XDG_CACHE_HOME="$home/.cache" PATH="$work/bin:$PATH" ANTHROPIC_MODEL="$shell_model" \
    NO_COLOR=1 WEAVE_ROUTER_KEY=rk_test \
    bash "$installer" --claude --quiet --non-interactive --base-url http://127.0.0.1:9 "$@" </dev/null >"$work/output" 2>&1 \
    || { cat "$work/output" >&2; fail "claude install $*"; }
}
codex() {
  local home="$1"
  shift
  HOME="$home" PATH="$work/bin:$PATH" NO_COLOR=1 WEAVE_ROUTER_KEY=rk_test \
    bash "$installer" --codex --scope user --quiet --base-url http://127.0.0.1:9 "$@" </dev/null >"$work/output" 2>&1 \
    || { cat "$work/output" >&2; fail "codex install $*"; }
}
claude_prior() { jq -r '.env.ANTHROPIC_CUSTOM_HEADERS // ""' "$1" | sed -n 's/^X-Weave-Prior-Model: //p'; }
codex_prior() {
  python3 - "$1" <<'PY'
import sys, tomllib
with open(sys.argv[1], "rb") as f:
    print(tomllib.load(f)["model_providers"]["weave"]["http_headers"].get("X-Weave-Prior-Model", ""))
PY
}
expect() {
  local got="$1" want="$2" what="$3"
  [ "$got" = "$want" ] || fail "$what: got '$got', want '$want'"
}
new_home() {
  local home="$work/$1"
  mkdir -p "$home/.claude" "$home/.codex"
  printf '%s' "$home"
}

# Claude Code: the saved model is recorded before the rollout switches to Weave Auto.
home="$(new_home saved)"
settings="$home/.claude/settings.json"
printf '%s\n' '{"model":"claude-sonnet-4-6[1m]"}' >"$settings"
claude "$home" "" --scope user
expect "$(jq -r '.model' "$settings")" 'weave-auto[1m]' "rollout selects Weave Auto"
expect "$(claude_prior "$settings")" claude-sonnet-4-6 "saved model is recorded without [1m]"
claude "$home" "" --scope user
expect "$(claude_prior "$settings")" claude-sonnet-4-6 "reinstall keeps the record instead of weave-auto"
[ "$(jq -r '.env.ANTHROPIC_CUSTOM_HEADERS' "$settings" | grep -c '^X-Weave-Prior-Model:')" = 1 ] \
  || fail "reinstall duplicated the prior-model header"

# off parks the header and on restores it unchanged.
claude "$home" "" off --scope user
claude "$home" "" on --scope user
expect "$(claude_prior "$settings")" claude-sonnet-4-6 "off/on keeps the record"
HOME="$home" PATH="$work/bin:$PATH" NO_COLOR=1 bash "$uninstaller" --claude --scope user </dev/null >"$work/output" 2>&1 \
  || { cat "$work/output" >&2; fail "uninstall"; }
expect "$(claude_prior "$settings")" "" "uninstall removes the record"

# ANTHROPIC_MODEL beats the saved model: settings env first, then the shell.
home="$(new_home env)"
settings="$home/.claude/settings.json"
printf '%s\n' '{"model":"claude-sonnet-4-6","env":{"ANTHROPIC_MODEL":"claude-opus-4-7"}}' >"$settings"
claude "$home" "claude-haiku-4-5" --scope user
expect "$(claude_prior "$settings")" claude-opus-4-7 "settings ANTHROPIC_MODEL is recorded"
home="$(new_home shell)"
settings="$home/.claude/settings.json"
printf '%s\n' '{"model":"claude-sonnet-4-6"}' >"$settings"
claude "$home" "claude-haiku-4-5" --scope user
expect "$(claude_prior "$settings")" claude-haiku-4-5 "shell ANTHROPIC_MODEL is recorded"

# No model, Weave Auto itself and junk all record unset.
for saved in '{}' '{"model":"weave-auto[1m]"}' '{"model":"not a model"}'; do
  home="$(new_home "unset-$RANDOM")"
  settings="$home/.claude/settings.json"
  printf '%s\n' "$saved" >"$settings"
  claude "$home" "" --scope user
  expect "$(claude_prior "$settings")" unset "settings $saved"
done

# An existing install from before this header keeps routing and records unset
# rather than its current Weave Auto selection.
home="$(new_home legacy)"
settings="$home/.claude/settings.json"
printf '%s\n' '{"model":"weave-auto[1m]","env":{"ANTHROPIC_CUSTOM_HEADERS":"X-Weave-Router-Key: rk_test\nX-App: claude-code\nX-Weave-Model-Passthrough: 1"}}' >"$settings"
claude "$home" "" --scope user
expect "$(claude_prior "$settings")" unset "legacy install"

# Project scope reads the user-level model and keeps the header in settings.local.json.
home="$(new_home project)"
project="$work/project"
mkdir -p "$project"
git -C "$project" init -q
printf '%s\n' '{"model":"claude-opus-4-7"}' >"$home/.claude/settings.json"
(cd "$project" && claude "$home" "" --scope project)
expect "$(claude_prior "$project/.claude/settings.local.json")" claude-opus-4-7 "project scope"
expect "$(claude_prior "$project/.claude/settings.json")" "" "shared project settings carry no record"
(cd "$project" && claude "$home" "" --scope project)
expect "$(claude_prior "$project/.claude/settings.local.json")" claude-opus-4-7 "project reinstall"

# Codex: the saved top-level model is recorded, and kept after the user moves to Weave Auto.
home="$(new_home codex-saved)"
config="$home/.codex/config.toml"
printf '%s\n' 'model = "gpt-5.4"' >"$config"
codex "$home"
expect "$(codex_prior "$config")" gpt-5.4 "codex saved model"
sed -i.bak 's/^model = "gpt-5.4"$/model = "weave-auto"/' "$config"
codex "$home"
expect "$(codex_prior "$config")" gpt-5.4 "codex reinstall keeps the record"

home="$(new_home codex-unset)"
config="$home/.codex/config.toml"
codex "$home"
expect "$(codex_prior "$config")" unset "codex without a model"
grep -qx 'model = "weave-auto"' "$config" || fail "codex default model"
codex "$home"
expect "$(codex_prior "$config")" unset "codex reinstall without a model"

echo "prior_model_test: ok"
