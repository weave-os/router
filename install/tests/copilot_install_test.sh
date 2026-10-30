#!/usr/bin/env bash
# Regression tests for the GitHub Copilot CLI install, toggle, and uninstall
# lifecycle: weave/auto is added to providers.json next to the user's own
# providers, never by replacing Copilot's endpoint.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="$script_dir/../install.sh"
uninstaller="$script_dir/../uninstall.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
home="$work/home"
fake_bin="$work/bin"
mkdir -p "$home/.copilot" "$fake_bin"
cat >"$fake_bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
case "${!#}" in
  http://127.0.0.1:9/validate|http://127.0.0.1:9/health) ;;
  *) exit 22 ;;
esac
while [ $# -gt 0 ]; do
  case "$1" in
    -o) printf '%s' '{}' >"$2"; shift 2 ;;
    -w) printf '200'; shift 2 ;;
    *) shift ;;
  esac
done
FAKE_CURL
chmod +x "$fake_bin/curl"
printf '%s\n' '#!/usr/bin/env bash' 'exit 0' >"$fake_bin/copilot"
chmod +x "$fake_bin/copilot"
test_path="$fake_bin:$PATH"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

run() {
  env -u WEAVE_ROUTER_KEY HOME="$home" PATH="$test_path" NO_COLOR=1 COPILOT_HOME="" "$@"
}

copilot_dir="$home/.copilot"
providers="$copilot_dir/providers.json"
settings="$copilot_dir/settings.json"
parked="$copilot_dir/.weave-parked.json"

cat >"$providers" <<'JSON'
{"providers":[{"name":"ollama","type":"openai","baseUrl":"http://localhost:11434/v1"}],"models":[{"id":"qwen","provider":"ollama"}]}
JSON
printf '%s\n' '{"model":"claude-sonnet-5","banner":"never"}' >"$settings"

run env WEAVE_ROUTER_KEY=rk_copilot_test bash "$installer" --copilot --quiet --non-interactive \
  --base-url http://127.0.0.1:9 --email dev@example.com >/dev/null 2>&1 || fail "install failed"

weave="$(jq -c '.providers[] | select(.name == "weave")' "$providers")"
[ "$(jq -r '.type' <<<"$weave")" = "anthropic" ] || fail "default wire is not anthropic"
[ "$(jq -r '.baseUrl' <<<"$weave")" = "http://127.0.0.1:9" ] || fail "anthropic baseUrl is not the router root"
[ "$(jq -r '.headers["X-Weave-Router-Key"]' <<<"$weave")" = "rk_copilot_test" ] || fail "router key header missing"
[ "$(jq -r '.headers["X-App"]' <<<"$weave")" = "copilot" ] || fail "X-App is not copilot"
[ "$(jq -r '.headers["X-Weave-User-Email"]' <<<"$weave")" = "dev@example.com" ] || fail "email header missing"
[ "$(jq -r 'has("apiKey") or has("bearerToken")' <<<"$weave")" = "false" ] || fail "router key leaked into a credential field Copilot forwards as BYOK auth"
[ "$(jq -r '[.models[] | select(.provider == "weave")] | length' "$providers")" = "1" ] || fail "expected exactly one weave model"
[ "$(jq -r '.models[] | select(.provider == "weave") | .id' "$providers")" = "auto" ] || fail "weave model id is not auto"
[ "$(jq -r '.providers[] | select(.name == "ollama") | .baseUrl' "$providers")" = "http://localhost:11434/v1" ] || fail "install dropped a user provider"
[ "$(jq -r '.models[] | select(.provider == "ollama") | .id' "$providers")" = "qwen" ] || fail "install dropped a user model"
perms="$(stat -c '%a' "$providers" 2>/dev/null || stat -f '%Lp' "$providers")"
[ "$perms" = "600" ] || fail "providers.json is $perms, want 600"
[ "$(jq -r '.model' "$settings")" = "weave/auto" ] || fail "default model not set to weave/auto"
[ "$(jq -r '.banner' "$settings")" = "never" ] || fail "install dropped an unrelated setting"
[ "$(jq -r '.direct_model' "$parked")" = "claude-sonnet-5" ] || fail "previous default model not parked"

for skill in force-model fm unforce-model ufm router-feedback rf router-off router-on router-status; do
  [ -f "$copilot_dir/skills/$skill/SKILL.md" ] || fail "missing /$skill skill"
done
[ -x "$copilot_dir/skills/force-model/scripts/emit.sh" ] || fail "force-model emit.sh not executable"
# shellcheck disable=SC2016 # literal backticks
grep -Fq '`/force-model <model-id>`' "$copilot_dir/skills/force-model/SKILL.md" || fail "skill still documents the Codex sigil"
grep -Fq 'weave-router off --copilot' "$copilot_dir/skills/router-off/SKILL.md" || fail "router-off skill does not target --copilot"
# shellcheck disable=SC2016 # literal $
if grep -Rq -- '--codex\|\$force-model' "$copilot_dir/skills"; then fail "Codex-specific text left in Copilot skills"; fi
[ ! -e "$copilot_dir/skills/disable-routing" ] || fail "Codex-only disable-routing installed for Copilot"

# Re-install is idempotent and keeps the parked model.
run env WEAVE_ROUTER_KEY=rk_copilot_test bash "$installer" --copilot --quiet --non-interactive \
  --base-url http://127.0.0.1:9 >/dev/null 2>&1 || fail "reinstall failed"
[ "$(jq -r '[.providers[] | select(.name == "weave")] | length' "$providers")" = "1" ] || fail "reinstall duplicated the weave provider"
[ "$(jq -r '.direct_model' "$parked")" = "claude-sonnet-5" ] || fail "reinstall lost the parked model"

# update reuses the key on disk and keeps the wire format unless --wire changes it.
run bash "$installer" update --copilot --wire openai --quiet --base-url http://127.0.0.1:9 >/dev/null 2>&1 || fail "update --wire openai failed"
weave="$(jq -c '.providers[] | select(.name == "weave")' "$providers")"
[ "$(jq -r '.type' <<<"$weave")" = "openai" ] || fail "--wire openai did not switch type"
[ "$(jq -r '.wireApi' <<<"$weave")" = "responses" ] || fail "openai wire is not Responses"
[ "$(jq -r '.baseUrl' <<<"$weave")" = "http://127.0.0.1:9/v1" ] || fail "openai baseUrl lacks /v1"
[ "$(jq -r '.headers["X-Weave-Router-Key"]' <<<"$weave")" = "rk_copilot_test" ] || fail "update did not reuse the installed key"
run bash "$installer" update --copilot --quiet >/dev/null 2>&1 || fail "update failed"
[ "$(jq -r '.providers[] | select(.name == "weave") | .type' "$providers")" = "openai" ] || fail "update reverted the installed wire"
[ "$(jq -r '.providers[] | select(.name == "weave") | .baseUrl' "$providers")" = "http://127.0.0.1:9/v1" ] || fail "update retargeted the installed endpoint"

# off restores the previous default; on puts weave/auto back.
run bash "$installer" off --copilot >/dev/null 2>&1 || fail "off failed"
[ "$(jq -r '.model' "$settings")" = "claude-sonnet-5" ] || fail "off did not restore the previous default"
[ "$(jq -r '[.providers[] | select(.name == "weave")] | length' "$providers")" = "1" ] || fail "off removed weave/auto from the picker"
run bash "$installer" on --copilot >/dev/null 2>&1 || fail "on failed"
[ "$(jq -r '.model' "$settings")" = "weave/auto" ] || fail "on did not set weave/auto"

if run env WEAVE_ROUTER_KEY=rk_copilot_test bash "$installer" --copilot --scope project --non-interactive \
    --base-url http://127.0.0.1:9 >/dev/null 2>&1; then
  fail "project scope was accepted for Copilot"
fi

# A user-owned skill with the same name is left alone.
mkdir -p "$copilot_dir/skills/rf"
printf '%s\n' 'my own rf skill' >"$copilot_dir/skills/rf/SKILL.md"
run bash "$uninstaller" --copilot >/dev/null 2>&1 || fail "uninstall failed"
[ "$(jq -r '[.providers[] | select(.name == "weave")] | length' "$providers")" = "0" ] || fail "uninstall left the weave provider"
[ "$(jq -r '[.models[] | select(.provider == "weave")] | length' "$providers")" = "0" ] || fail "uninstall left the weave model"
[ "$(jq -r '.providers[0].name' "$providers")" = "ollama" ] || fail "uninstall dropped a user provider"
[ "$(jq -r '.model' "$settings")" = "claude-sonnet-5" ] || fail "uninstall did not restore the previous default"
[ ! -e "$parked" ] || fail "uninstall left the parked sidecar"
[ ! -e "$copilot_dir/skills/force-model" ] || fail "uninstall left a managed skill"
[ -f "$copilot_dir/skills/rf/SKILL.md" ] || fail "uninstall removed a user-owned skill"

# Fresh install with no prior providers.json, then uninstall removes the file.
fresh="$work/fresh"
mkdir -p "$fresh"
env -u WEAVE_ROUTER_KEY HOME="$fresh" PATH="$test_path" NO_COLOR=1 COPILOT_HOME="" WEAVE_ROUTER_KEY=rk_copilot_test \
  bash "$installer" --copilot --quiet --non-interactive --base-url http://127.0.0.1:9 >/dev/null 2>&1 || fail "fresh install failed"
[ "$(jq -r '.model' "$fresh/.copilot/settings.json")" = "weave/auto" ] || fail "fresh install did not set the default"
env -u WEAVE_ROUTER_KEY HOME="$fresh" PATH="$test_path" NO_COLOR=1 COPILOT_HOME="" bash "$uninstaller" --copilot >/dev/null 2>&1 || fail "fresh uninstall failed"
[ ! -e "$fresh/.copilot/providers.json" ] || fail "uninstall left an empty providers.json"
[ "$(jq -r 'has("model")' "$fresh/.copilot/settings.json")" = "false" ] || fail "uninstall left weave/auto as the default"

# COPILOT_HOME is honored, and a symlinked providers.json is refused.
custom="$work/custom-copilot"
HOME="$home" PATH="$test_path" NO_COLOR=1 COPILOT_HOME="$custom" WEAVE_ROUTER_KEY=rk_copilot_test \
  bash "$installer" --copilot --quiet --non-interactive --base-url http://127.0.0.1:9 >/dev/null 2>&1 || fail "COPILOT_HOME install failed"
[ -f "$custom/providers.json" ] || fail "COPILOT_HOME was ignored"
linked="$work/linked"
mkdir -p "$linked/.copilot"
ln -s "$work/elsewhere.json" "$linked/.copilot/providers.json"
if HOME="$linked" PATH="$test_path" NO_COLOR=1 COPILOT_HOME="" WEAVE_ROUTER_KEY=rk_copilot_test \
    bash "$installer" --copilot --quiet --non-interactive --base-url http://127.0.0.1:9 >/dev/null 2>&1; then
  fail "install wrote through a symlinked providers.json"
fi
[ ! -e "$work/elsewhere.json" ] || fail "router key written through a symlink"

echo "PASS: copilot install lifecycle"
