#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
uninstaller="${UNINSTALLER:-$script_dir/../uninstall.sh}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin"
printf '%s\n' '#!/usr/bin/env bash' 'exit 22' >"$work/bin/curl"
chmod +x "$work/bin/curl"

run() {
  local home="$1"
  shift
  HOME="$home" XDG_CACHE_HOME="$home/.cache" PATH="$work/bin:$PATH" ANTHROPIC_MODEL="" \
    NO_COLOR=1 WEAVE_ROUTER_KEY=rk_test \
    bash "$installer" --claude --quiet --non-interactive --base-url http://127.0.0.1:9 "$@" </dev/null >"$work/output" 2>&1
}
uninstall() {
  local home="$1"
  shift
  HOME="$home" PATH="$work/bin:$PATH" NO_COLOR=1 bash "$uninstaller" --claude "$@" </dev/null >"$work/output" 2>&1
}
picker_rows() { jq -c '[.modelPicker.options[]? | select(.model == "weave-auto[1m]")] | length' "$1"; }

# A fresh install adds no picker row, model default or passthrough header: the
# router routes whichever model Claude Code sends.
fresh="$work/fresh"
mkdir -p "$fresh/.claude"
printf '%s\n' '{"modelPicker":{"options":[{"model":"my-model","label":"Mine"}]}}' >"$fresh/.claude/settings.json"
settings="$fresh/.claude/settings.json"
run "$fresh" --scope user
test "$(jq -r '.model // "unset"' "$settings")" = unset
test "$(jq -c '.modelPicker' "$settings")" = '{"options":[{"model":"my-model","label":"Mine"}]}'
test "$(jq -r '.env.ANTHROPIC_CUSTOM_HEADERS' "$settings" | grep -c 'X-Weave-Model-Passthrough' || true)" = 0
run "$fresh" off --scope user
run "$fresh" on --scope user
test "$(jq -r '.model // "unset"' "$settings")" = unset
uninstall "$fresh" --scope user
test "$(jq -c '.modelPicker' "$settings")" = '{"options":[{"model":"my-model","label":"Mine"}]}'

# Update retires a Weave Auto install: the row and header go, and the saved
# selection keeps its 1M context budget as opus[1m].
for legacy_model in 'weave-auto[1m]' weave-auto; do
  legacy="$work/legacy-$legacy_model"
  mkdir -p "$legacy/.claude"
  jq -n --arg model "$legacy_model" '{model: $model, modelPicker: {options: [{model: "weave-auto[1m]", label: "Weave Auto"}, {model: "my-model"}]}, env: {ANTHROPIC_CUSTOM_HEADERS: "X-Weave-Router-Key: rk_old\nX-App: claude-code\nX-Weave-Model-Passthrough: 1"}}' >"$legacy/.claude/settings.json"
  run "$legacy" --scope user
  test "$(jq -r '.model' "$legacy/.claude/settings.json")" = 'opus[1m]'
  test "$(jq -c '[.modelPicker.options[].model]' "$legacy/.claude/settings.json")" = '["my-model"]'
  test "$(jq -r '.env.ANTHROPIC_CUSTOM_HEADERS' "$legacy/.claude/settings.json" | grep -c 'X-Weave-Model-Passthrough' || true)" = 0
done

# A model the user picked stays.
chosen="$work/chosen"
mkdir -p "$chosen/.claude"
printf '%s\n' '{"model":"sonnet"}' >"$chosen/.claude/settings.json"
run "$chosen" --scope user
test "$(jq -r '.model' "$chosen/.claude/settings.json")" = sonnet

# An install parked off while Weave Auto was selected comes back on opus[1m].
parked="$work/parked"
mkdir -p "$parked/.claude"
printf '%s\n' '{"model":"weave-auto[1m]","modelPicker":{"options":[{"model":"weave-auto[1m]"}]}}' >"$parked/.claude/settings.json"
run "$parked" --scope user
jq '.model = "weave-auto[1m]" | .modelPicker = {options: [{model: "weave-auto[1m]"}]}' "$parked/.claude/settings.json" >"$work/legacy" && mv "$work/legacy" "$parked/.claude/settings.json"
run "$parked" off --scope user
test "$(jq -r '.model // "unset"' "$parked/.claude/settings.json")" = unset
test "$(jq -r '.modelPicker // "unset"' "$parked/.claude/settings.json")" = unset
run "$parked" on --scope user
test "$(jq -r '.model' "$parked/.claude/settings.json")" = 'opus[1m]'

# Updating an install parked off while Weave Auto was selected also restores opus[1m].
parked_update="$work/parked-update"
mkdir -p "$parked_update/.claude"
printf '%s\n' '{}' >"$parked_update/.claude/settings.json"
run "$parked_update" --scope user
jq '.model = "weave-auto[1m]"' "$parked_update/.claude/settings.json" >"$work/legacy" && mv "$work/legacy" "$parked_update/.claude/settings.json"
run "$parked_update" off --scope user
test "$(jq -r '.weave_auto_selected' "$parked_update/.claude/.weave-parked.json")" = true
run "$parked_update" --scope user
test "$(jq -r '.model' "$parked_update/.claude/settings.json")" = 'opus[1m]'
test ! -f "$parked_update/.claude/.weave-parked.json"

# Project scope migrates the local selection and keeps committed settings clean.
project_home="$work/project-home"
project="$work/project"
mkdir -p "$project_home" "$project/.claude"
git -C "$project" init -q
printf '%s\n' '{"model":"weave-auto[1m]","modelPicker":{"options":[{"model":"weave-auto[1m]"}]}}' >"$project/.claude/settings.local.json"
(cd "$project" && run "$project_home" --scope project)
test "$(jq -r '.model // "unset"' "$project/.claude/settings.json")" = unset
test "$(jq -r '.model' "$project/.claude/settings.local.json")" = 'opus[1m]'
test "$(jq -r '.modelPicker // "unset"' "$project/.claude/settings.local.json")" = unset
(cd "$project" && uninstall "$project_home" --scope project)
test "$(jq -r '.model' "$project/.claude/settings.local.json")" = 'opus[1m]'

echo "Claude Weave Auto installer regression tests passed"
