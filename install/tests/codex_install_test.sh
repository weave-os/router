#!/usr/bin/env bash
#
# Regression tests for Codex installation. The loopback endpoint keeps router
# validation local, and an isolated HOME prevents changes to real config.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
[ -f "$installer" ] || { echo "cannot find installer at $installer" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
home="$work/home"
mkdir -p "$home"

run_install() {
  HOME="$home" WEAVE_ROUTER_KEY="rk_test_key" NO_COLOR=1 \
    bash "$installer" --codex --scope user --quiet \
      --base-url http://127.0.0.1:9
}

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

run_install
config="$home/.codex/config.toml"
[ -f "$config" ] || fail "Codex config was not created"
grep -qx 'model_provider = "weave"' "$config" \
  || fail "Weave was not selected as the default provider"
grep -qx 'requires_openai_auth = true' "$config" \
  || fail "Weave provider does not require ChatGPT OAuth"

# A repeat install must refresh one managed block, not duplicate its auth rule.
run_install
[ "$(grep -cx 'requires_openai_auth = true' "$config")" -eq 1 ] \
  || fail "repeat install duplicated the OAuth requirement"

echo "Codex installer OAuth regression tests passed"
