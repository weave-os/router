#!/usr/bin/env bash
#
# Regression tests for Codex installation. A fake curl keeps validation
# offline, and an isolated HOME prevents changes to real config.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
[ -f "$installer" ] || { echo "cannot find installer at $installer" >&2; exit 1; }
uninstaller="${UNINSTALLER:-$(dirname "$installer")/uninstall.sh}"
[ -f "$uninstaller" ] || { echo "cannot find uninstaller at $uninstaller" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
home="$work/home"
fake_bin="$work/bin"
mkdir -p "$home" "$fake_bin"
printf '%s\n' '#!/usr/bin/env bash' 'exit 22' >"$fake_bin/curl"
chmod +x "$fake_bin/curl"
test_path="$fake_bin:$PATH"

run_hosted_install() {
  HOME="$home" PATH="$test_path" WEAVE_ROUTER_KEY="rk_test_key" NO_COLOR=1 \
    bash "$installer" --codex --scope user --quiet \
      --base-url https://router.workweave.ai
}

run_install() {
  HOME="$home" PATH="$test_path" WEAVE_ROUTER_KEY="rk_test_key" NO_COLOR=1 \
    bash "$installer" --codex --scope user --quiet \
      --base-url http://127.0.0.1:9
}

run_local_install() {
  HOME="$home" PATH="$test_path" WEAVE_ROUTER_KEY="rk_test_key" NO_COLOR=1 \
    bash "$installer" --codex --scope user --quiet --local
}

run_disable_routing() {
  HOME="$home" PATH="$test_path" NO_COLOR=1 \
    bash "$installer" disable-routing --scope user --quiet
}

run_uninstall() {
  HOME="$home" PATH="$test_path" NO_COLOR=1 \
    bash "$uninstaller" --codex --scope user
}

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# Old installer versions placed Markdown wrappers here, but current Codex does
# not discover them as slash commands. The upgrade must remove only those
# canonical files and leave unrelated user prompts alone.
mkdir -p "$home/.codex/prompts"
for command in force-model unforce-model router-feedback fm ufm rf; do
  cp "$script_dir/../commands/$command.md" "$home/.codex/prompts/$command.md"
done
printf '%s\n' 'user-authored prompt' >"$home/.codex/prompts/keep-me.md"

run_hosted_install
config="$home/.codex/config.toml"
[ -f "$config" ] || fail "Codex config was not created"
grep -qx 'model_provider = "weave"' "$config" \
  || fail "Weave was not selected as the default provider"
grep -qx 'requires_openai_auth = true' "$config" \
  || fail "Weave provider does not require ChatGPT OAuth"
if grep -Fq 'X-Weave-Router-Strategy' "$config"; then
  fail "Codex provider pinned a routing strategy instead of the router default"
fi

# No install target pins a strategy: every endpoint, hosted or self-hosted,
# uses the router's own deployment default.
run_local_install
grep -Fq 'base_url = "http://localhost:8080/v1"' "$config" \
  || fail "Codex --local did not select the local router"
if grep -Fq 'X-Weave-Router-Strategy' "$config"; then
  fail "Codex --local forced the optional HMM strategy"
fi
run_install
grep -Fq 'base_url = "http://127.0.0.1:9/v1"' "$config" \
  || fail "Codex custom --base-url was not preserved"
if grep -Fq 'X-Weave-Router-Strategy' "$config"; then
  fail "Codex custom --base-url forced the optional HMM strategy"
fi
run_hosted_install
if grep -Fq 'X-Weave-Router-Strategy' "$config"; then
  fail "public-hosted Codex reinstall pinned a routing strategy"
fi

# The stale wrappers must no longer suggest unsupported `/prompts:*` aliases.
for command in force-model unforce-model router-feedback fm ufm rf; do
  prompt="$home/.codex/prompts/$command.md"
  [ ! -e "$prompt" ] || fail "obsolete Codex $command prompt was not removed"
done
[ -f "$home/.codex/prompts/keep-me.md" ] \
  || fail "installer removed an unrelated Codex prompt"

skill="$home/.codex/skills/disable-routing/SKILL.md"
[ -f "$skill" ] || fail "Codex disable-routing skill was not installed"
grep -Fq '<!-- weave-router managed disable-routing skill -->' "$skill" \
  || fail "Codex disable-routing skill has no ownership marker"
grep -Fq 'weave-router off --codex' "$skill" \
  || fail "Codex disable-routing skill does not use the safe off toggle"
if HOME="$home" PATH="$test_path" NO_COLOR=1 bash "$installer" disable-routing --claude --scope user --quiet >/dev/null 2>&1; then
  fail "disable-routing accepted a non-Codex target"
fi

# The alias must pick Codex even without a --codex flag and return a later
# process to Codex's default provider without removing the managed block.
run_disable_routing
grep -qx '# model_provider = "weave"  # weave-router: off (run on to re-enable)' "$config" \
  || fail "disable-routing did not turn off the Codex provider"
[ "$(grep -c '^\[model_providers\.weave\]$' "$config")" -eq 1 ] \
  || fail "disable-routing removed or duplicated the managed provider"

# A repeat install must refresh one managed block, not duplicate its auth rule.
run_hosted_install
[ "$(grep -cx 'requires_openai_auth = true' "$config")" -eq 1 ] \
  || fail "repeat install duplicated the OAuth requirement"

run_uninstall
[ ! -e "$skill" ] || fail "uninstall did not remove the Codex disable-routing skill"

# A same-named user skill is not ours to overwrite or remove. This also
# covers an upgrade on a machine where the name was already taken.
mkdir -p "$(dirname "$skill")"
printf '%s\n' 'user-authored skill' >"$skill"
run_hosted_install
grep -qx 'user-authored skill' "$skill" \
  || fail "install overwrote a user-owned Codex disable-routing skill"
run_uninstall
grep -qx 'user-authored skill' "$skill" \
  || fail "uninstall removed a user-owned Codex disable-routing skill"

echo "Codex installer routing regression tests passed"
