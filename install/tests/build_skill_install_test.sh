#!/usr/bin/env bash
#
# Offline regression tests for the managed build skill: Claude Code and Codex
# installs copy the tree, refresh managed copies, preserve user-owned files,
# and uninstall removes only what the installer recorded.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
uninstaller="${UNINSTALLER:-$(dirname "$installer")/uninstall.sh}"
[ -f "$installer" ] || { echo "cannot find installer at $installer" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
fake_bin="$work/bin"
mkdir -p "$fake_bin"
printf '%s\n' '#!/usr/bin/env bash' 'exit 22' >"$fake_bin/curl"
chmod +x "$fake_bin/curl"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

run() {
  local home="$1"
  shift
  HOME="$home" PATH="$fake_bin:$PATH" WEAVE_ROUTER_KEY="rk_test_key" NO_COLOR=1 \
    bash "$@" </dev/null >"$work/out.log" 2>&1 || { cat "$work/out.log" >&2; fail "command failed: $*"; }
}

managed="<!-- weave-router managed build skill -->"

# Claude Code: skill tree plus agent definitions.
home="$work/claude"
mkdir -p "$home"
run "$home" "$installer" --claude --scope user --quiet --non-interactive --base-url http://127.0.0.1:9
skill="$home/.claude/skills/build"
grep -Fq "$managed" "$skill/SKILL.md" || fail "Claude build skill not installed"
for rel in references/worker-contracts.md references/client-controls.md references/pr-loop.md; do
  [ -f "$skill/$rel" ] || fail "Claude build skill missing $rel"
done
[ -x "$skill/scripts/pin-coordinator.sh" ] || fail "pin-coordinator.sh is not executable"
[ ! -e "$skill/agents" ] || fail "agent definitions leaked into the skill tree"
for agent in build-researcher build-implementer build-verifier build-reviewer; do
  grep -Fq "<!-- weave-router managed agent: $agent -->" "$home/.claude/agents/$agent.md" \
    || fail "Claude agent $agent not installed"
  ! grep -q '^model:' "$home/.claude/agents/$agent.md" || fail "agent $agent pins a model instead of routing"
done
[ "$(bash "$skill/scripts/pin-coordinator.sh")" = " /force-parent claude-opus-5-5" ] \
  || fail "coordinator pin does not default to claude-opus-5-5"

# A reinstall refreshes managed files, drops stale ones, and keeps user files.
printf 'stale\n' >"$skill/references/stale.md"
printf 'references/stale.md\n' >>"$skill/.weave-router-files"
printf 'mine\n' >"$skill/notes.md"
printf 'edited\n' >>"$skill/SKILL.md"
printf 'user agent\n' >"$home/.claude/agents/my-agent.md"
run "$home" "$installer" --claude --scope user --quiet --non-interactive --base-url http://127.0.0.1:9
[ ! -e "$skill/references/stale.md" ] || fail "reinstall kept a stale managed file"
! grep -Fxq edited "$skill/SKILL.md" || fail "reinstall did not refresh SKILL.md"
[ -f "$skill/notes.md" ] || fail "reinstall removed a user file inside the skill"

# Manifest entries behind a symlinked directory, and a symlinked manifest, never
# reach outside the skill.
outside="$work/outside"
mkdir -p "$outside"
printf 'keep\n' >"$outside/victim.md"
mv "$skill/references" "$work/references.real"
ln -s "$outside" "$skill/references"
printf 'references/victim.md\nreferences/deep/victim.md\n' >>"$skill/.weave-router-files"
mkdir -p "$outside/deep"
printf 'keep\n' >"$outside/deep/victim.md"
run "$home" "$installer" --claude --scope user --quiet --non-interactive --base-url http://127.0.0.1:9
[ -f "$outside/victim.md" ] || fail "reinstall deleted a file through a symlinked directory"
[ -f "$outside/deep/victim.md" ] || fail "reinstall deleted a nested file through a symlinked directory"
rm "$skill/references"
mv "$work/references.real" "$skill/references"
rm "$skill/.weave-router-files"
ln -s "$outside/manifest" "$skill/.weave-router-files"
run "$home" "$installer" --claude --scope user --quiet --non-interactive --base-url http://127.0.0.1:9
[ ! -e "$outside/manifest" ] || fail "reinstall wrote the manifest through a symlink"
[ ! -L "$skill/.weave-router-files" ] || fail "reinstall kept a symlinked manifest"

run "$home" "$uninstaller" --claude --scope user
[ ! -e "$skill/SKILL.md" ] || fail "uninstall left the managed SKILL.md"
[ -f "$skill/notes.md" ] || fail "uninstall removed a user file inside the skill"
for agent in build-researcher build-implementer build-verifier build-reviewer; do
  [ ! -e "$home/.claude/agents/$agent.md" ] || fail "uninstall left agent $agent"
done
[ -f "$home/.claude/agents/my-agent.md" ] || fail "uninstall removed a user agent"

# A user-owned build skill is never overwritten, and its agents stay unmanaged.
home="$work/claude-user-skill"
mkdir -p "$home/.claude/skills/build"
printf 'my build skill\n' >"$home/.claude/skills/build/SKILL.md"
run "$home" "$installer" --claude --scope user --quiet --non-interactive --base-url http://127.0.0.1:9
[ "$(cat "$home/.claude/skills/build/SKILL.md")" = "my build skill" ] || fail "installer overwrote a user build skill"
[ ! -e "$home/.claude/agents/build-researcher.md" ] || fail "installer added agents beside a user build skill"
run "$home" "$uninstaller" --claude --scope user
[ "$(cat "$home/.claude/skills/build/SKILL.md")" = "my build skill" ] || fail "uninstall removed a user build skill"

# A user-owned agent with a build-* name is never overwritten or removed.
home="$work/claude-user-agent"
mkdir -p "$home/.claude/agents"
printf 'my reviewer\n' >"$home/.claude/agents/build-reviewer.md"
run "$home" "$installer" --claude --scope user --quiet --non-interactive --base-url http://127.0.0.1:9
[ "$(cat "$home/.claude/agents/build-reviewer.md")" = "my reviewer" ] || fail "installer overwrote a user agent"
[ -f "$home/.claude/agents/build-researcher.md" ] || fail "installer skipped unrelated managed agents"
run "$home" "$uninstaller" --claude --scope user
[ "$(cat "$home/.claude/agents/build-reviewer.md")" = "my reviewer" ] || fail "uninstall removed a user agent"
[ ! -e "$home/.claude/agents/build-researcher.md" ] || fail "uninstall left a managed agent"

# Codex: skill tree only; Codex subagents are spawned inline.
home="$work/codex"
mkdir -p "$home"
run "$home" "$installer" --codex --scope user --quiet --base-url http://127.0.0.1:9
skill="$home/.codex/skills/build"
grep -Fq "$managed" "$skill/SKILL.md" || fail "Codex build skill not installed"
[ -x "$skill/scripts/pin-coordinator.sh" ] || fail "Codex pin-coordinator.sh is not executable"
[ ! -e "$skill/agents" ] || fail "agent definitions leaked into the Codex skill"
run "$home" "$uninstaller" --codex --scope user
[ ! -e "$skill" ] || fail "Codex uninstall left the build skill"

# Uninstall never follows a symlinked directory or manifest out of the skill.
home="$work/codex-symlink"
mkdir -p "$home"
run "$home" "$installer" --codex --scope user --quiet --non-interactive --base-url http://127.0.0.1:9
skill="$home/.codex/skills/build"
outside="$work/outside-uninstall"
mkdir -p "$outside/deep"
printf 'keep\n' >"$outside/victim.md"
printf 'keep\n' >"$outside/deep/victim.md"
rm -rf "$skill/references"
ln -s "$outside" "$skill/references"
printf 'references/victim.md\nreferences/deep/victim.md\n' >>"$skill/.weave-router-files"
run "$home" "$uninstaller" --codex --scope user
[ -f "$outside/victim.md" ] || fail "uninstall deleted a file through a symlinked directory"
[ -f "$outside/deep/victim.md" ] || fail "uninstall deleted a nested file through a symlinked directory"

echo "Build skill installer tests passed"
