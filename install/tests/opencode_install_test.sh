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
cat >"$fake_bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
[ "${!#}" = "http://127.0.0.1:9/validate" ] || exit 22
while [ $# -gt 0 ]; do
  case "$1" in
    -o) printf '%s' '{}' >"$2"; shift 2 ;;
    -w) printf '200'; shift 2 ;;
    *) shift ;;
  esac
done
FAKE_CURL
chmod +x "$fake_bin/curl"
cat >"$fake_bin/opencode" <<'FAKE_OPENCODE'
#!/usr/bin/env bash
if [ "${1:-}" = "--version" ]; then
  printf '%s\n' "${FAKE_OPENCODE_VERSION:-2.0.0}"
  exit 0
fi
exit 0
FAKE_OPENCODE
chmod +x "$fake_bin/opencode"
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

run_install_output() {
  HOME="$home" XDG_CONFIG_HOME="$home/xdg" PATH="$test_path" NO_COLOR=1 \
    WEAVE_ROUTER_KEY="rk_opencode_test" \
    bash "$installer" --opencode --dir "$install_dir" --non-interactive \
      --base-url http://127.0.0.1:9 2>&1
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
legacy_plugin_dir="$install_dir/.weave"
mkdir -p "$legacy_plugin_dir"
printf '%s\n' 'export default legacyPlugin' >"$legacy_plugin_dir/opencode-weave.ts"
printf '%s\n' 'export const legacyDirectives = true' >"$legacy_plugin_dir/directives.ts"
printf '%s\n' 'export const legacyClassifier = true' >"$legacy_plugin_dir/classifier-thread.ts"
cat >"$config" <<'JSON'
{
  "$schema": "https://opencode.ai/config.json",
  "model": "anthropic/claude-sonnet-4-5",
  "provider": {"other": {"name": "Other"}},
  "providers": {
    "weave": {"name": "Legacy Weave"},
    "weave-claude": {"name": "Legacy Claude"},
    "weave-codex": {"name": "Legacy Codex"},
    "other": {"name": "Other legacy provider"}
  },
  "plugin": ["user-plugin", "/legacy/.weave/opencode-weave.ts"],
  "mcp": {"keep": {"type": "local"}}
}
JSON

bad_version_dir="$work/opencode-v1"
if HOME="$home" XDG_CONFIG_HOME="$home/xdg" PATH="$test_path" NO_COLOR=1 \
    WEAVE_ROUTER_KEY="rk_opencode_test" FAKE_OPENCODE_VERSION="1.9.0" \
    bash "$installer" --opencode --dir "$bad_version_dir" --quiet --non-interactive \
      --base-url http://127.0.0.1:9 >/dev/null 2>"$work/opencode-v1.err"; then
  fail "install accepted an OpenCode major version other than 2"
fi
grep -Fq "OpenCode major version 2 is required" "$work/opencode-v1.err" || \
  fail "incompatible OpenCode version error was not reported"
[ ! -e "$bad_version_dir/opencode.json" ] || \
  fail "incompatible OpenCode version wrote a config"

symlink_dir="$work/opencode-symlink"
mkdir -p "$symlink_dir" "$work/symlink-target"
ln -s "$work/symlink-target" "$symlink_dir/.weave"
if (umask 022 && HOME="$home" XDG_CONFIG_HOME="$home/xdg" PATH="$test_path" NO_COLOR=1 \
    WEAVE_ROUTER_KEY="rk_opencode_test" \
    bash "$installer" --opencode --dir "$symlink_dir" --quiet --non-interactive \
      --base-url http://127.0.0.1:9 >/dev/null 2>&1); then
  fail "install wrote through a legacy plugin directory symlink"
fi
[ ! -e "$symlink_dir/opencode.json" ] || fail "install wrote the router key before rejecting the legacy symlink"

run_install
# Reinstall must upgrade the legacy 128K managed context limit.
jq '.provider.weave.models.auto.limit.context = 128000' "$config" >"$config.tmp"
mv "$config.tmp" "$config"
install_output="$(run_install_output)"
grep -Fq "npx @weave-os/router login claude" <<<"$install_output" || fail "install did not print the managed enrollment command"
grep -Fq "npx @weave-os/router login codex" <<<"$install_output" || fail "install did not print the managed Codex enrollment command"
login_output="$(HOME="$home" XDG_CONFIG_HOME="$home/xdg" PATH="$test_path" NO_COLOR=1 \
  bash "$installer" login claude --dir "$install_dir" --non-interactive --quiet 2>&1 || true)"
grep -Fq "Claude login requires an interactive terminal" <<<"$login_output" || fail "managed login did not reuse the OpenCode install"
if grep -Fq "Weave Router is not installed" <<<"$login_output"; then
  fail "managed login ignored the OpenCode install endpoint"
fi
# A Claude install that already resolves an endpoint but carries no key must
# keep that endpoint: the OpenCode key only fills in when it belongs to the
# same router, never by swapping the endpoint underneath the caller.
claude_settings_dir="$install_dir/.claude"
mkdir -p "$claude_settings_dir"
printf '%s\n' '{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8"}}' >"$claude_settings_dir/settings.json"
login_output="$(HOME="$home" XDG_CONFIG_HOME="$home/xdg" PATH="$test_path" NO_COLOR=1 \
  bash "$installer" login claude --dir "$install_dir" --non-interactive --quiet 2>&1 || true)"
grep -Fq "No router key found" <<<"$login_output" || fail "managed login sent the OpenCode key to a different Claude endpoint"
printf '%s\n' '{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:9/"}}' >"$claude_settings_dir/settings.json"
login_output="$(HOME="$home" XDG_CONFIG_HOME="$home/xdg" PATH="$test_path" NO_COLOR=1 \
  bash "$installer" login claude --dir "$install_dir" --non-interactive --quiet 2>&1 || true)"
grep -Fq "Claude login requires an interactive terminal" <<<"$login_output" || fail "managed login did not reuse the OpenCode key for the matching Claude endpoint"
rm -rf "$claude_settings_dir"
[ "$(jq -r '.model' "$config")" = "weave/auto" ] || fail "install did not activate weave/auto"
[ "$(jq -r '.direct_model' "$parked")" = "anthropic/claude-sonnet-4-5" ] || fail "install did not park the previous model"
[ "$(jq -r '.provider.weave.models.auto.limit.context' "$config")" = "500000" ] || fail "reinstall did not update the old context limit to 500000"
[ "$(jq -r '.provider.weave.models.auto.limit.output' "$config")" = "32000" ] || fail "virtual model output limit is missing"
[ "$(jq -r '.provider.weave.models.auto.reasoning' "$config")" = "true" ] || fail "virtual model reasoning capability is missing"
[ "$(jq -r '.provider.weave.models.auto.attachment' "$config")" = "true" ] || fail "virtual model attachment capability is missing"
[ "$(jq -r '.provider.other.name' "$config")" = "Other" ] || fail "install replaced an unrelated provider"
[ "$(jq -r '(.providers // {}) | has("weave")' "$config")" = "false" ] || fail "install left the legacy providers.weave block"
[ "$(jq -r '(.providers // {}) | has("weave-claude")' "$config")" = "false" ] || fail "install left the legacy providers.weave-claude block"
[ "$(jq -r '(.providers // {}) | has("weave-codex")' "$config")" = "false" ] || fail "install left the legacy providers.weave-codex block"
[ "$(jq -r '.providers.other.name' "$config")" = "Other legacy provider" ] || fail "install removed an unrelated legacy provider"

[ "$(jq -r '.mcp.keep.type' "$config")" = "local" ] || fail "install replaced unrelated MCP config"
[ "$(jq -r '.plugin | index("user-plugin") != null' "$config")" = "true" ] || fail "install removed a user plugin"
[ "$(jq -r '[.plugin[]? | select(tostring | endswith("/opencode-weave.ts"))] | length' "$config")" = "0" ] || fail "install kept a legacy plugin registration"
[ ! -e "$legacy_plugin_dir/opencode-weave.ts" ] || fail "install left the legacy plugin file"
[ ! -e "$legacy_plugin_dir/directives.ts" ] || fail "install left the legacy directive hook"
[ ! -e "$legacy_plugin_dir/classifier-thread.ts" ] || fail "install left the legacy classifier hook"
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
mkdir -p "$legacy_plugin_dir"
printf '%s\n' 'export default legacyPlugin' >"$legacy_plugin_dir/opencode-weave.ts"
printf '%s\n' 'export const legacyDirectives = true' >"$legacy_plugin_dir/directives.ts"
printf '%s\n' 'export const legacyClassifier = true' >"$legacy_plugin_dir/classifier-thread.ts"
jq --arg plugin "$legacy_plugin_dir/opencode-weave.ts" \
  '.plugin += [$plugin] | .providers.weave = {name: "Legacy Weave"} | .providers["weave-codex"] = {name: "Legacy Codex"} | .providers["weave-claude"] = {name: "Legacy Claude"}' \
  "$config" >"$config.tmp"
mv "$config.tmp" "$config"
run_uninstall
[ "$(jq -r '.model' "$config")" = "google/gemini-3.8-flash" ] || fail "uninstall did not restore the direct model"
[ "$(jq -r '(.provider // {}) | has("weave")' "$config")" = "false" ] || fail "uninstall left the Weave provider"
[ "$(jq -r '(.providers // {}) | has("weave")' "$config")" = "false" ] || fail "uninstall left legacy providers.weave"
[ "$(jq -r '(.providers // {}) | has("weave-codex")' "$config")" = "false" ] || fail "uninstall left legacy providers.weave-codex"
[ "$(jq -r '(.providers // {}) | has("weave-claude")' "$config")" = "false" ] || fail "uninstall left legacy providers.weave-claude"
[ "$(jq -r '.providers.other.name' "$config")" = "Other legacy provider" ] || fail "uninstall removed an unrelated legacy provider"
[ "$(jq -r '.plugin | index("user-plugin") != null' "$config")" = "true" ] || fail "uninstall removed a user plugin"
[ "$(jq -r '[.plugin[]? | select(tostring | endswith("/opencode-weave.ts"))] | length' "$config")" = "0" ] || fail "uninstall left the legacy plugin registration"
[ ! -e "$legacy_plugin_dir/opencode-weave.ts" ] || fail "uninstall left the legacy plugin"
[ ! -e "$legacy_plugin_dir/directives.ts" ] || fail "uninstall left the legacy directive hook"
[ ! -e "$legacy_plugin_dir/classifier-thread.ts" ] || fail "uninstall left the legacy classifier hook"
[ ! -e "$legacy_plugin_dir" ] || fail "uninstall left the empty legacy plugin directory"
[ ! -e "$parked" ] || fail "uninstall left the parked model"
[ ! -e "$install_dir/.opencode/commands/fm.md" ] || fail "uninstall left managed commands"

echo "OpenCode installer lifecycle passed"
