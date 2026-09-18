#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
uninstaller="${UNINSTALLER:-$script_dir/../uninstall.sh}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin" "$work/user/.claude"
printf '%s\n' '#!/usr/bin/env bash' 'exit 22' >"$work/bin/curl"
chmod +x "$work/bin/curl"
test_home="$work/user"

run() {
  HOME="$test_home" XDG_CACHE_HOME="$test_home/.cache" PATH="$work/bin:$PATH" \
    ANTHROPIC_MODEL="" CLAUDE_CODE_DISABLE_1M_CONTEXT="" NO_COLOR=1 WEAVE_ROUTER_KEY=rk_test \
    bash "$installer" --claude --quiet --non-interactive --base-url http://127.0.0.1:9 "$@" </dev/null >"$work/output" 2>&1
}
run_update() {
  if run update "$@"; then
    return 0
  else
    status=$?
  fi
  test "$status" -eq 1
}
uninstall() {
  HOME="$test_home" PATH="$work/bin:$PATH" NO_COLOR=1 \
    bash "$uninstaller" --claude --scope user "$@" </dev/null >"$work/output" 2>&1
}
model() { jq -r '.model // "unset"' "$1"; }
settings="$test_home/.claude/settings.json"
state="$test_home/.claude/.weave-context-window.json"

printf '%s\n' '{"model":"opus","statusLine":{"type":"command","command":"my-status"},"env":{"MY_SETTING":"keep"}}' >"$settings"
run
test "$(model "$settings")" = opus
test ! -e "$state"
run --context-window 1m
test "$(model "$settings")" = 'opus[1m]'
test "$(jq -r '.original' "$state")" = opus
test "$(jq -r '.statusLine.command' "$settings")" = my-status
test "$(jq -r '.env.MY_SETTING' "$settings")" = keep
test "$(jq -r '.env.DISABLE_COMPACT // "unset"' "$settings")" = unset
run_update --context-window 1m
test "$(jq -r '.original' "$state")" = opus
run off
test "$(model "$settings")" = opus
run on
test "$(model "$settings")" = 'opus[1m]'
run off
run_update
test "$(model "$settings")" = 'opus[1m]'
uninstall
test "$(model "$settings")" = opus
test ! -e "$state"

# An intentional later selection wins over our saved model on off/on/uninstall.
run --context-window 1m
jq '.model = "haiku"' "$settings" >"$work/edited.json"
mv "$work/edited.json" "$settings"
run off
run on
test "$(model "$settings")" = haiku
uninstall
test "$(model "$settings")" = haiku
if run --context-window 1m; then echo 'unsupported model unexpectedly accepted'; exit 1; fi
test "$(model "$settings")" = haiku

# Missing and explicitly null defaults are restored exactly.
for original in '{}' '{"model":null}'; do
  printf '%s\n' "$original" >"$settings"
  run --context-window 1m
  test "$(model "$settings")" = 'sonnet[1m]'
  uninstall
  test "$(jq 'has("model")' "$settings")" = "$(printf '%s' "$original" | jq 'has("model")')"
done

printf '%s\n' '{"env":{"CLAUDE_CODE_DISABLE_1M_CONTEXT":"true"}}' >"$settings"
if run --context-window 1m; then echo '1M opt-out unexpectedly overwritten'; exit 1; fi
test "$(model "$settings")" = unset
printf '%s\n' '{"env":{"ANTHROPIC_MODEL":"custom-model"}}' >"$settings"
if run --context-window 1m; then echo 'model env override unexpectedly overwritten'; exit 1; fi
test "$(jq -r '.env.ANTHROPIC_MODEL' "$settings")" = custom-model

# A project-local opt-in respects an inherited choice and leaves tracked model settings alone.
printf '%s\n' '{"model":"opus"}' >"$settings"
project="$work/project"
mkdir -p "$project/.claude"
git -C "$project" init -q
printf '%s\n' '{"model":"sonnet"}' >"$project/.claude/settings.json"
(cd "$project" && run --scope project --context-window 1m)
local_settings="$project/.claude/settings.local.json"
test "$(model "$project/.claude/settings.json")" = sonnet
test "$(model "$local_settings")" = 'sonnet[1m]'
test "$(model "$settings")" = opus
(cd "$project" && run --scope project off)
test "$(model "$local_settings")" = unset
(cd "$project" && run --scope project on)
test "$(model "$local_settings")" = 'sonnet[1m]'
(cd "$project" && uninstall --scope project)
test "$(model "$local_settings")" = unset

# --dir uses the same reversible ownership contract, without touching user settings.
mkdir -p "$work/isolated"
run --dir "$work/isolated" --context-window 1m
test "$(model "$work/isolated/.claude/settings.json")" = 'opus[1m]'
uninstall --dir "$work/isolated"
test "$(model "$work/isolated/.claude/settings.json")" = unset
test "$(model "$settings")" = opus

if run --codex --context-window 1m; then echo 'wrong target unexpectedly accepted'; exit 1; fi
if run --context-window 200k; then echo 'invalid window unexpectedly accepted'; exit 1; fi
echo 'Claude context-window lifecycle tests passed'
