#!/usr/bin/env bash
# Regression tests for the OpenCode install, toggle, and uninstall lifecycle.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="$script_dir/../install.sh"
uninstaller="$script_dir/../uninstall.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
home="$work/home"
install_dir="$work/opencode"
fake_bin="$work/bin"
mkdir -p "$home" "$install_dir" "$fake_bin"
printf '%s\n' '#!/usr/bin/env bash' 'exit 22' >"$fake_bin/curl"
chmod +x "$fake_bin/curl"
test_path="$fake_bin:$PATH"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

run_install() {
  HOME="$home" XDG_CONFIG_HOME="$home/xdg" PATH="$test_path" NO_COLOR=1 \
    WEAVE_ROUTER_KEY="rk_opencode_test" \
    bash "$installer" --opencode --dir "$install_dir" --quiet --non-interactive \
      --base-url http://127.0.0.1:9 >/dev/null 2>&1
}

run_toggle() {
  HOME="$home" XDG_CONFIG_HOME="$home/xdg" PATH="$test_path" NO_COLOR=1 \
    bash "$installer" "$1" --opencode --dir "$install_dir" >/dev/null 2>&1
}

run_uninstall() {
  HOME="$home" XDG_CONFIG_HOME="$home/xdg" PATH="$test_path" NO_COLOR=1 \
    bash "$uninstaller" --opencode --dir "$install_dir" >/dev/null 2>&1
}

config="$install_dir/opencode.json"
parked="$install_dir/.weave-parked.json"
managed_plugin="$install_dir/.weave/opencode-weave.ts"
cat >"$config" <<'JSON'
{
  "$schema": "https://opencode.ai/config.json",
  "model": "anthropic/claude-sonnet-4-5",
  "provider": {"other": {"name": "Other"}},
  "plugin": ["user-plugin"],
  "mcp": {"keep": {"type": "local"}}
}
JSON

run_install
[ "$(jq -r '.model' "$config")" = "weave/auto" ] || fail "install did not activate weave/auto"
[ "$(jq -r '.direct_model' "$parked")" = "anthropic/claude-sonnet-4-5" ] || fail "install did not park the previous model"
[ "$(jq -r '.provider.weave.models.auto.limit.context' "$config")" = "128000" ] || fail "virtual model context limit is missing"
[ "$(jq -r '.provider.weave.models.auto.limit.output' "$config")" = "32000" ] || fail "virtual model output limit is missing"
[ "$(jq -r '.provider.weave.models.auto.reasoning' "$config")" = "true" ] || fail "virtual model reasoning capability is missing"
[ "$(jq -r '.provider.weave.models.auto.attachment' "$config")" = "true" ] || fail "virtual model attachment capability is missing"
[ "$(jq -r '.provider.other.name' "$config")" = "Other" ] || fail "install replaced an unrelated provider"
[ "$(jq -r '.mcp.keep.type' "$config")" = "local" ] || fail "install replaced unrelated MCP config"
[ -f "$managed_plugin" ] || fail "subscription plugin was not copied"
jq -e --arg plugin "$managed_plugin" '.plugin | index($plugin)' "$config" >/dev/null || fail "subscription plugin was not registered"
[ -f "$install_dir/.opencode/commands/fm.md" ] || fail "--dir commands were not installed beside the config"
[ ! -e "$home/xdg/opencode/commands/fm.md" ] || fail "--dir install mutated global OpenCode commands"
case "$(uname -s)" in
  Darwin) mode="$(stat -f '%Lp' "$config")" ;;
  *) mode="$(stat -c '%a' "$config")" ;;
esac
[ "$mode" = "600" ] || fail "opencode.json mode is $mode, expected 600"

run_toggle off
[ "$(jq -r '.model' "$config")" = "anthropic/claude-sonnet-4-5" ] || fail "off did not restore the previous model"

# A direct model selected while off becomes the next exact restore target.
jq '.model = "google/gemini-3.8-flash"' "$config" >"$config.tmp"
mv "$config.tmp" "$config"
run_toggle on
[ "$(jq -r '.model' "$config")" = "weave/auto" ] || fail "on did not reactivate weave/auto"
run_toggle off
[ "$(jq -r '.model' "$config")" = "google/gemini-3.8-flash" ] || fail "off did not restore the latest direct model"
run_toggle on
run_uninstall
[ "$(jq -r '.model' "$config")" = "google/gemini-3.8-flash" ] || fail "uninstall did not restore the direct model"
[ "$(jq -r '(.provider // {}) | has("weave")' "$config")" = "false" ] || fail "uninstall left the Weave provider"
[ "$(jq -r '.plugin | index("user-plugin") != null' "$config")" = "true" ] || fail "uninstall removed a user plugin"
[ ! -e "$managed_plugin" ] || fail "uninstall left the subscription plugin"
[ ! -e "$parked" ] || fail "uninstall left the parked model"
[ ! -e "$install_dir/.opencode/commands/fm.md" ] || fail "uninstall left managed commands"

echo "OpenCode installer lifecycle passed"
