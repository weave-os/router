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

fresh="$work/fresh"
mkdir -p "$fresh/.claude"
printf '%s\n' '{"modelPicker":{"options":[{"model":"my-model","label":"Mine"}]}}' >"$fresh/.claude/settings.json"
settings="$fresh/.claude/settings.json"
run "$fresh" --scope user
test "$(jq -r '.model' "$settings")" = 'weave-auto[1m]'
test "$(jq -r '.modelPicker.options[0].label' "$settings")" = "Weave Auto"
test "$(jq -r '.modelPicker.options[1].model' "$settings")" = my-model
jq -r '.env.ANTHROPIC_CUSTOM_HEADERS' "$settings" | grep -qx 'X-Weave-Model-Passthrough: 1'
run "$fresh" --scope user
test "$(picker_rows "$settings")" = 1

# Off removes the picker row and default so Claude Code never sends weave-auto to Anthropic.
run "$fresh" off --scope user
test "$(jq -r '.model // "unset"' "$settings")" = unset
test "$(picker_rows "$settings")" = 0
run "$fresh" on --scope user
test "$(jq -r '.model' "$settings")" = 'weave-auto[1m]'
test "$(picker_rows "$settings")" = 1

uninstall "$fresh" --scope user
test "$(jq -r '.model // "unset"' "$settings")" = unset
test "$(jq -c '.modelPicker' "$settings")" = '{"options":[{"model":"my-model","label":"Mine"}]}'

# A plain weave-auto (picked before the 1M default) moves to the [1m] row.
legacy="$work/legacy"
mkdir -p "$legacy/.claude"
printf '%s\n' '{"model":"weave-auto","modelPicker":{"options":[{"model":"weave-auto"}]},"env":{"ANTHROPIC_CUSTOM_HEADERS":"X-Weave-Model-Passthrough: 1"}}' >"$legacy/.claude/settings.json"
run "$legacy" --scope user
test "$(jq -r '.model' "$legacy/.claude/settings.json")" = 'weave-auto[1m]'
test "$(jq -c '[.modelPicker.options[].model]' "$legacy/.claude/settings.json")" = '["weave-auto[1m]"]'

# A model saved before passthrough was always routed, so the first passthrough
# install moves it to Weave Auto. After that a model the user picks stays.
chosen="$work/chosen"
mkdir -p "$chosen/.claude"
printf '%s\n' '{"model":"opus","env":{"ANTHROPIC_MODEL":"opus"}}' >"$chosen/.claude/settings.json"
run "$chosen" --scope user
test "$(jq -r '.model' "$chosen/.claude/settings.json")" = 'weave-auto[1m]'
test "$(jq -r '.env.ANTHROPIC_MODEL // "unset"' "$chosen/.claude/settings.json")" = unset
test "$(picker_rows "$chosen/.claude/settings.json")" = 1
jq '.model = "opus"' "$chosen/.claude/settings.json" >"$work/picked" && mv "$work/picked" "$chosen/.claude/settings.json"
run "$chosen" --scope user
test "$(jq -r '.model' "$chosen/.claude/settings.json")" = opus
test "$(picker_rows "$chosen/.claude/settings.json")" = 1
run "$chosen" off --scope user
run "$chosen" on --scope user
test "$(jq -r '.model' "$chosen/.claude/settings.json")" = opus

# Project scope keeps the default and the passthrough header out of committed settings.
project_home="$work/project-home"
project="$work/project"
mkdir -p "$project_home" "$project"
git -C "$project" init -q
(cd "$project" && run "$project_home" --scope project)
test "$(jq -r '.model // "unset"' "$project/.claude/settings.json")" = unset
test "$(jq -r '.model' "$project/.claude/settings.local.json")" = 'weave-auto[1m]'
test "$(picker_rows "$project/.claude/settings.local.json")" = 1
(cd "$project" && run "$project_home" off --scope project)
test "$(jq -r '.model // "unset"' "$project/.claude/settings.local.json")" = unset
(cd "$project" && run "$project_home" on --scope project)
test "$(jq -r '.model' "$project/.claude/settings.local.json")" = 'weave-auto[1m]'
(cd "$project" && uninstall "$project_home" --scope project)
test "$(jq -r '.model // "unset"' "$project/.claude/settings.local.json")" = unset
test "$(picker_rows "$project/.claude/settings.local.json")" = 0

echo "Claude Weave Auto installer regression tests passed"
