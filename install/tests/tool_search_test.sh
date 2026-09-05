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
  local home="$1"
  shift
  HOME="$home" XDG_CACHE_HOME="$home/.cache" PATH="$fake_bin:$PATH" \
    NO_COLOR=1 WEAVE_ROUTER_KEY="rk_test" \
    bash "$installer" --claude --quiet --non-interactive \
      --base-url http://127.0.0.1:9 "$@" </dev/null >/dev/null 2>&1
}

user_home="$work/user"
mkdir -p "$user_home"
run_install "$user_home" --scope user
test "$(jq -r '.env.ENABLE_TOOL_SEARCH' "$user_home/.claude/settings.json")" = "true"

project_home="$work/project-home"
project="$work/project"
mkdir -p "$project_home" "$project"
git -C "$project" init -q
(cd "$project" && run_install "$project_home" --scope project)
test "$(jq -r '.env.ENABLE_TOOL_SEARCH' "$project/.claude/settings.json")" = "true"

echo "Claude tool-search installer regression tests passed"
