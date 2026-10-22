#!/usr/bin/env bash
#
# Under Git Bash, settings.json stores the statusline command in C:/ form while
# $HOME is /c/, so reinstall and uninstall must still recognise it as router-owned.
# shellcheck disable=SC2016  # the fake cygpath needs its own runtime expansions

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
command -v jq >/dev/null 2>&1 || { echo "statusline_windows_path_test: jq is required" >&2; exit 1; }

fixture_dir="$(mktemp -d)"
trap 'rm -rf "$fixture_dir"' EXIT

mkdir -p "$fixture_dir/bin" "$fixture_dir/weave"
printf '%s\n' '#!/bin/sh' \
  '[ "$1" = "-m" ] && [ "$2" = "--" ] || exit 2' \
  'case "$3" in /c/*) printf "C:%s\n" "${3#/c}" ;; *) printf "%s\n" "$3" ;; esac' >"$fixture_dir/bin/cygpath"
chmod +x "$fixture_dir/bin/cygpath"
mkdir -p "$fixture_dir/broken-bin"
printf '%s\n' '#!/bin/sh' 'exit 127' >"$fixture_dir/broken-bin/cygpath"
chmod +x "$fixture_dir/broken-bin/cygpath"

posix_command="/c/Users/dev/.weave/cc-statusline.sh"
script_path="$fixture_dir/weave/cc-statusline.sh"
settings="$fixture_dir/settings.json"
printf '{"statusLine":{"type":"command","command":"C:/Users/dev/.weave/cc-statusline.sh"}}\n' >"$settings"
printf '#!/usr/bin/env bash\n# Claude Code statusline for the Weave Router.\n' >"$script_path"

pass=0
fail=0
check() {
  if [ "$2" = "$3" ]; then
    pass=$((pass + 1)); echo "  ok   $1"
  else
    fail=$((fail + 1)); echo "  FAIL $1: expected [$2], got [$3]"
  fi
}

for script in install.sh uninstall.sh; do
  unset -f statusline_commands_match claude_statusline_router_owned 2>/dev/null || true
  # Load just the ownership helpers so cygpath and the paths can be stubbed.
  eval "$(sed -n '/^CLAUDE_STATUSLINE_MARKER=/p' "$script_dir/../$script")"
  eval "$(awk '/^statusline_commands_match\(\)/,/^}/' "$script_dir/../$script")"
  eval "$(awk '/^claude_statusline_router_owned\(\)/,/^}/' "$script_dir/../$script")"
  declare -F statusline_commands_match claude_statusline_router_owned >/dev/null ||
    { echo "statusline_windows_path_test: could not load helpers from $script" >&2; exit 1; }

  rc=0
  PATH="$fixture_dir/bin:$PATH" claude_statusline_router_owned \
    "$settings" "$posix_command" "$script_path" "$script_path.weave-router" || rc=$?
  check "$script owns a C:/ statusline command when \$HOME is /c/" "0" "$rc"

  rc=0
  PATH="$fixture_dir/bin:$PATH" claude_statusline_router_owned \
    "$settings" "/c/Users/other/.weave/cc-statusline.sh" "$script_path" "$script_path.weave-router" || rc=$?
  check "$script still rejects a different statusline path" "1" "$rc"

  rc=0
  PATH="$fixture_dir/broken-bin:$PATH" claude_statusline_router_owned \
    "$settings" "$posix_command" "$script_path" "$script_path.weave-router" || rc=$?
  check "$script does not claim ownership when cygpath fails" "1" "$rc"
done

echo
echo "$pass passed"
[ "$fail" -eq 0 ]
