#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fake_bin="$work/bin"
mkdir -p "$fake_bin"
printf '%s\n' '#!/usr/bin/env bash' 'exit 22' >"$fake_bin/curl"
chmod +x "$fake_bin/curl"

run_install() {
  local home="$1" action=""
  shift
  if [ "${1:-}" = update ]; then action=update; shift; fi
  HOME="$home" XDG_CACHE_HOME="$home/.cache" PATH="$fake_bin:$PATH" \
    NO_COLOR=1 WEAVE_ROUTER_KEY="rk_test" \
    bash "$installer" ${action:+"$action"} --claude --quiet --non-interactive \
      --base-url http://127.0.0.1:9 "$@" </dev/null >/dev/null 2>&1
}

user_home="$work/user"
mkdir -p "$user_home"
run_install "$user_home" --scope user
test "$(jq -r '.env.ENABLE_TOOL_SEARCH' "$user_home/.claude/settings.json")" = "true"
test "$(jq -r '.env.CLAUDE_CODE_AUTO_MODE_SERVER' "$user_home/.claude/settings.json")" = "0"
test "$(jq -r '.env.WEAVE_ROUTER_CONFIG_VERSION' "$user_home/.claude/settings.json")" = "1"
test "$(jq -r '.env.WEAVE_ROUTER_CONFIG_VERSION' "$user_home/.claude/settings.json")" = "$(sed -n 's/^WEAVE_REQUIRED_CONFIG_VERSION=//p' "$script_dir/../cc-statusline.sh")"
sed -i 's/"WEAVE_ROUTER_CONFIG_VERSION": "1"/"WEAVE_ROUTER_CONFIG_VERSION": "0"/' "$user_home/.claude/settings.json"
run_install "$user_home" update --scope user || test "$?" = 1
test "$(jq -r '.env.WEAVE_ROUTER_CONFIG_VERSION' "$user_home/.claude/settings.json")" = "1"
for action in off on; do
  HOME="$user_home" PATH="$fake_bin:$PATH" bash "$installer" "$action" --claude --scope user >/dev/null 2>&1
  expected=null
  [ "$action" != on ] || expected=1
  test "$(jq -r '.env.WEAVE_ROUTER_CONFIG_VERSION' "$user_home/.claude/settings.json")" = "$expected"
done

project_home="$work/project-home"
project="$work/project"
mkdir -p "$project_home" "$project"
git -C "$project" init -q
(cd "$project" && run_install "$project_home" --scope project)
test "$(jq -r '.env.ENABLE_TOOL_SEARCH' "$project/.claude/settings.json")" = "true"
test "$(jq -r '.env.CLAUDE_CODE_AUTO_MODE_SERVER' "$project/.claude/settings.json")" = "null"
test "$(jq -r '.env.CLAUDE_CODE_AUTO_MODE_SERVER' "$project/.claude/settings.local.json")" = "0"

toggle_project() {
  (cd "$project" && HOME="$project_home" XDG_CACHE_HOME="$project_home/.cache" PATH="$fake_bin:$PATH" NO_COLOR=1 \
    bash "$installer" "$1" --claude --scope project </dev/null >/dev/null 2>&1)
}
test "$(jq -r '.env.WEAVE_ROUTER_CONFIG_VERSION' "$project/.claude/settings.json")" = "null"
test "$(jq -r '.env.WEAVE_ROUTER_CONFIG_VERSION' "$project/.claude/settings.local.json")" = "1"
sed -i 's/"WEAVE_ROUTER_CONFIG_VERSION": "1"/"WEAVE_ROUTER_CONFIG_VERSION": "0"/' "$project/.claude/settings.local.json"
(cd "$project" && run_install "$project_home" update --scope project) || test "$?" = 1
test "$(jq -r '.env.WEAVE_ROUTER_CONFIG_VERSION' "$project/.claude/settings.local.json")" = "1"
toggle_project off
test "$(jq -r '.env.WEAVE_ROUTER_CONFIG_VERSION' "$project/.claude/settings.local.json")" = "null"
test "$(jq -r '.env.CLAUDE_CODE_AUTO_MODE_SERVER' "$project/.claude/settings.local.json")" = "null"
toggle_project on
test "$(jq -r '.env.WEAVE_ROUTER_CONFIG_VERSION' "$project/.claude/settings.local.json")" = "1"
test "$(jq -r '.env.CLAUDE_CODE_AUTO_MODE_SERVER' "$project/.claude/settings.local.json")" = "0"

custom="$work/custom dir"
mkdir -p "$custom"
run_install "$user_home" --dir "$custom"
test "$(jq -r '.env.WEAVE_ROUTER_CONFIG_VERSION' "$custom/.claude/settings.json")" = "1"

HOME="$user_home" PATH="$fake_bin:$PATH" bash "$script_dir/../uninstall.sh" --claude --scope user >/dev/null 2>&1
test "$(jq -r '.env.WEAVE_ROUTER_CONFIG_VERSION' "$user_home/.claude/settings.json")" = "null"
(cd "$project" && HOME="$project_home" PATH="$fake_bin:$PATH" bash "$script_dir/../uninstall.sh" --claude --scope project >/dev/null 2>&1)
test "$(jq -r '.env.WEAVE_ROUTER_CONFIG_VERSION' "$project/.claude/settings.local.json")" = "null"

echo "Claude tool-search and auto-mode installer regression tests passed"
