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
grep -qx 'features.hooks = true' "$config" \
  || fail "Codex hooks were not enabled"
grep -Fq '[[hooks.SessionStart]]' "$config" \
  || fail "Codex SessionStart hook was not installed"
grep -Fq '[[hooks.Stop]]' "$config" \
  || fail "Codex Stop hook was not installed"
status_helper="$home/.weave/codex-status.sh"
  [ "$(grep -Fc "command = \"$status_helper\"" "$config")" -eq 2 ] \
  || fail "Codex hooks do not point at the installed status helper"
[ -f "$status_helper" ] || fail "Codex status helper was not installed"
grep -Fq '<!-- weave-router managed codex status -->' "$status_helper" \
  || fail "Codex status helper has no ownership marker"
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

for alias in fm ufm rf; do
  alias_skill="$home/.codex/skills/$alias/SKILL.md"
  [ -f "$alias_skill" ] || fail "Codex \$$alias skill was not installed"
  grep -Fq "<!-- weave-router managed $alias skill -->" "$alias_skill" \
    || fail "Codex \$$alias skill has no ownership marker"
done
for name in force-model fm unforce-model ufm router-feedback rf; do
  emit="$home/.codex/skills/$name/scripts/emit.sh"
  [ -x "$emit" ] || fail "Codex \$$name skill is missing an executable emit.sh"
done
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
[ ! -e "$status_helper" ] || fail "uninstall did not remove the Codex status helper"

for name in force-model fm unforce-model ufm router-feedback rf \
            router-off router-on router-status router-models; do
  [ ! -e "$home/.codex/skills/$name" ] \
    || fail "uninstall left the Codex \$$name skill directory behind"
done

# A pre-existing hooks scalar or table is incompatible with inline lifecycle
# arrays. Preserve either user shape and install routing without managed hooks.
for hooks_shape in scalar table; do
  if [ "$hooks_shape" = scalar ]; then
    # The literal ${HOME} is the point: it must survive the install unexpanded.
    # shellcheck disable=SC2016
    printf '%s\n' 'hooks = "${HOME}/.codex/hooks.json"' >"$config"
  else
    printf '%s\n' '[hooks]' 'enabled = true' >"$config"
  fi
  run_hosted_install
  grep -Fq 'model_provider = "weave"' "$config" \
    || fail "hooks conflict prevented Codex routing setup ($hooks_shape)"
  if grep -Fq '[[hooks.SessionStart]]' "$config" || grep -Fq '[[hooks.Stop]]' "$config"; then
    fail "installer added inline hooks to conflicting hooks $hooks_shape config"
  fi
  if [ "$hooks_shape" = scalar ]; then
    # shellcheck disable=SC2016
    grep -Fq 'hooks = "${HOME}/.codex/hooks.json"' "$config" \
      || fail "installer did not preserve hooks path"
  else
    grep -Fq 'enabled = true' "$config" \
      || fail "installer did not preserve hooks table"
  fi
  run_uninstall
  [ ! -e "$status_helper" ] || fail "uninstall left the status helper after hooks conflict test"
done

# A user-owned status helper must not be overwritten or made executable by the
# managed hooks. The installer refuses the install rather than wiring around it.
mkdir -p "$(dirname "$status_helper")"
printf '%s\n' 'user-authored status helper' >"$status_helper"
if run_hosted_install; then
  fail "installer accepted an unowned Codex status helper"
fi
grep -qx 'user-authored status helper' "$status_helper" \
  || fail "installer modified an unowned Codex status helper"
rm -f "$status_helper"

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


# ---------- Codex rewrites config.toml and our comment markers do not survive ----------
#
# Codex round-trips the whole file through a TOML serializer whenever it
# persists its own state (hook trust hashes, project trust levels, the model
# NUX table). Comments are dropped, so the managed markers disappear while
# `[model_providers.weave]` survives -- re-emitted in the serializer's own
# shape: keys alphabetized and our inline `http_headers` expanded into a
# [model_providers.weave.http_headers] subtable.
#
# Every reader and writer keyed on the markers alone broke on that config:
# install appended a second provider table (TOML rejects a table declared
# twice, so Codex refused to start), the endpoint reader reported a live
# install as absent, the key reader missed the router key, and uninstall left
# the provider and its key on disk. Seed exactly that file for each case.
seed_codex_normalized_config() {
  rm -rf "$home/.codex" "$home/.weave"
  mkdir -p "$home/.codex"
  cat >"$config" <<'NORMALIZED'
model_provider = "weave"

[features]
hooks = true

[hooks.state."/Users/a/.codex/config.toml:stop:0:0"]
enabled = true
trusted_hash = "sha256:deadbeef"

[model_providers.weave]
base_url = "https://router.workweave.ai/v1"
name = "Weave Router"
requires_openai_auth = true
wire_api = "responses"

[model_providers.weave.http_headers]
X-App = "codex"
X-Weave-Router-Key = "rk_normalized_key"

[projects."/Users/a/Code/weave"]
trust_level = "trusted"
NORMALIZED
}

# assert_config_parses fails when config.toml is not loadable TOML -- the exact
# check Codex performs at startup, and the one that caught the duplicate table.
# Skipped rather than faked where no python3 is available; the count assertions
# below still pin the regression.
assert_config_parses() {
  command -v python3 >/dev/null 2>&1 || return 0
  python3 - "$config" <<'PARSE' || fail "$1"
import sys, tomllib
with open(sys.argv[1], "rb") as fh:
    tomllib.load(fh)
PARSE
}

seed_codex_normalized_config
run_hosted_install
assert_config_parses "install over a Codex-rewritten config produced unparseable TOML"
[ "$(grep -c '^\[model_providers\.weave\]$' "$config")" -eq 1 ] \
  || fail "install over a Codex-rewritten config duplicated [model_providers.weave]"
[ "$(grep -c '^\[model_providers\.weave\.http_headers\]$' "$config")" -eq 0 ] \
  || fail "install over a Codex-rewritten config left the serializer's headers subtable"
[ "$(grep -c '^model_provider = "weave"$' "$config")" -eq 1 ] \
  || fail "install over a Codex-rewritten config duplicated the top-level model_provider"
# Unrelated Codex-owned state is not ours to drop while rewriting around it.
grep -Fq '[hooks.state."/Users/a/.codex/config.toml:stop:0:0"]' "$config" \
  || fail "install over a Codex-rewritten config dropped Codex hook state"
grep -Fq '[projects."/Users/a/Code/weave"]' "$config" \
  || fail "install over a Codex-rewritten config dropped Codex project trust"

# The endpoint reader backs `login`/`status`. Reading only between the markers
# reported "No Weave Router install found for codex in this scope" on a
# perfectly good install, which sent users through a reinstall to fix it.
seed_codex_normalized_config
router_status="$(HOME="$home" PATH="$test_path" NO_COLOR=1 \
  bash "$installer" status --scope user 2>&1 || true)"
printf '%s' "$router_status" | grep -q 'Codex: points at Router' \
  || fail "status did not detect a Codex install whose markers Codex had dropped"

# `login codex` is where users hit this first: it refused to enroll a
# subscription against a working install and told them to run the installer
# again. The device-authorization step still fails offline here -- what must not
# come back is the "no install" refusal that precedes it.
login_out="$(HOME="$home" PATH="$test_path" NO_COLOR=1 \
  bash "$installer" login codex --scope user 2>&1 || true)"
if printf '%s' "$login_out" | grep -q 'No Weave Router install found'; then
  fail "login codex did not see an install whose markers Codex had dropped"
fi

# The key reader has to accept both spellings of the header name: our writer
# emits one quoted inline table, Codex re-emits it as a bare key in a subtable.
# Missing it made a re-install mint a second router key -- or, non-interactively,
# fail outright with no key to fall back on.
seed_codex_normalized_config
HOME="$home" PATH="$test_path" NO_COLOR=1 WEAVE_ROUTER_KEY="" \
  bash "$installer" --codex --scope user --quiet --non-interactive \
    --base-url https://router.workweave.ai </dev/null >/dev/null 2>&1 \
  || fail "re-install could not reuse the key from a Codex-rewritten config"
grep -Fq 'rk_normalized_key' "$config" \
  || fail "re-install replaced the router key already in a Codex-rewritten config"

# Uninstall reported success while leaving the provider -- and the key inside
# it -- behind, so routing kept working and the credential stayed on disk.
seed_codex_normalized_config
run_uninstall
[ "$(grep -c '^\[model_providers\.weave\]$' "$config")" -eq 0 ] \
  || fail "uninstall left the provider table from a Codex-rewritten config"
if grep -Fq 'rk_normalized_key' "$config"; then
  fail "uninstall left the router key in a Codex-rewritten config"
fi
if grep -Fq 'model_provider = "weave"' "$config"; then
  fail "uninstall left Codex routed at the Weave provider"
fi
# Codex's own state still has to survive an uninstall.
grep -Fq '[projects."/Users/a/Code/weave"]' "$config" \
  || fail "uninstall dropped Codex project trust"

# A provider whose name merely starts with "weave" is a different provider and
# must be left alone -- the table matcher is anchored, not a prefix search.
seed_codex_normalized_config
cat >>"$config" <<'NEIGHBOUR'

[model_providers.weaver]
base_url = "https://example.invalid/v1"
name = "Weaver"
NEIGHBOUR
run_hosted_install
grep -Fq '[model_providers.weaver]' "$config" \
  || fail "install removed an unrelated provider whose name starts with weave"
run_uninstall
grep -Fq '[model_providers.weaver]' "$config" \
  || fail "uninstall removed an unrelated provider whose name starts with weave"

rm -rf "$home/.codex" "$home/.weave"

echo "Codex installer routing regression tests passed"
