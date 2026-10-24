#!/usr/bin/env bash
# Offline tests for commit attribution choices and the bare multi-client uninstall.
set -uo pipefail
unset WEAVE_ROUTER_KEY WEAVE_USER_EMAIL

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="$script_dir/../install.sh"
uninstaller="$script_dir/../uninstall.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin" "$work/tmp"
cat >"$work/bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
prev=""
for arg; do [ "$prev" = -w ] && printf 200; prev="$arg"; done
exit 0
FAKE_CURL
chmod +x "$work/bin/curl"

pass=0
fail=0
check() {
  local name="$1"; shift
  if "$@"; then echo "  ok   $name"; pass=$((pass + 1)); else echo "  FAIL $name"; fail=$((fail + 1)); fi
}
has() { grep -Fq -- "$2" <<<"$1"; }
lacks() { ! grep -Fq -- "$2" <<<"$1"; }
run_env() { local home="$1"; shift; mkdir -p "$home"; env HOME="$home" XDG_CACHE_HOME="$home/.cache" TMPDIR="$work/tmp" PATH="$work/bin:$PATH" NO_COLOR=1 "$@" </dev/null 2>&1; }
install() { local home="$1"; shift; run_env "$home" WEAVE_ROUTER_KEY=rk_test bash "$installer" "$@" --scope user --non-interactive --base-url http://127.0.0.1:9; }
uninstall() { local home="$1"; shift; run_env "$home" bash "$uninstaller" "$@"; }
attribution() { jq -c '.attribution // "none"' "$1/.claude/settings.json"; }
weave_attribution() { attribution "$1" | grep -q 'Co-Authored-By: Weave Router'; }

echo "commit attribution"

home="$work/attr"
out="$(install "$home" --claude)"
check "a fresh install credits Weave Router" weave_attribution "$home"
check "a fresh install says so and how to turn it off" has "$out" "re-run with --no-attribution"
out="$(install "$home" --claude --no-attribution)"
check "--no-attribution removes the credit" test "$(attribution "$home")" = '"none"'
check "--no-attribution install does not announce a credit" lacks "$out" "will credit Weave Router"
install "$home" --claude >/dev/null
check "a reinstall keeps attribution off" test "$(attribution "$home")" = '"none"'

home="$work/attr-own"
mkdir -p "$home/.claude"
printf '{"attribution":{"commit":"Co-Authored-By: Me <me@example.com>"}}\n' >"$home/.claude/settings.json"
install "$home" --claude >/dev/null
check "the user's own attribution is kept" test "$(attribution "$home")" = '{"commit":"Co-Authored-By: Me <me@example.com>"}'

home="$work/attr-pr"
mkdir -p "$home/.claude"
printf '{"env":{"ANTHROPIC_BASE_URL":"x"},"attribution":{"commit":"Co-Authored-By: Weave Router <router@weaveos.com>","pr":"made by me"}}\n' >"$home/.claude/settings.json"
install "$home" --claude --no-attribution >/dev/null
check "--no-attribution keeps a PR line the user wrote" test "$(attribution "$home")" = '{"pr":"made by me"}'

echo "bare uninstall"

home="$work/both"
mkdir -p "$home/.claude"
printf '{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"my-own-hook.sh"},{"type":"command","command":"%s/.weave/cc-statusline.sh --prompt-hook"}]}]}}\n' "$home" >"$home/.claude/settings.json"
install "$home" --claude >/dev/null
install "$home" --claude >/dev/null
check "install registers the /router-status prompt hook once" test "$(jq '[.hooks.UserPromptSubmit[].hooks[].command | select(test("cc-statusline\\.sh\"? --prompt-hook( |$)"))] | length' "$home/.claude/settings.json")" = 1
check "install keeps the user's own prompt hook" test "$(jq '[.hooks.UserPromptSubmit[].hooks[].command | select(. == "my-own-hook.sh")] | length' "$home/.claude/settings.json")" = 1
check "a missing helper makes the hook a no-op" test "$(cd "$work" && sh -c "$(jq -r '.hooks.UserPromptSubmit[-1].hooks[0].command' "$home/.claude/settings.json" | sed 's#/.weave/#/.missing/#g')" </dev/null; echo $?)" = 0
install "$home" --codex >/dev/null
out="$(uninstall "$home")"
check "uninstall removes the /router-status prompt hook" test "$(jq '[.hooks.UserPromptSubmit[]?.hooks[]?.command | select(endswith("--prompt-hook"))] | length' "$home/.claude/settings.json")" = 0
check "uninstall keeps the user's own prompt hook" test "$(jq -r '.hooks.UserPromptSubmit[0].hooks[0].command' "$home/.claude/settings.json")" = my-own-hook.sh
check "bare uninstall removes Codex too" test "$(grep -c 'model_providers.weave' "$home/.codex/config.toml" 2>/dev/null || echo 0)" = 0
check "bare uninstall removes Claude Code routing" test "$(jq -r '.env.ANTHROPIC_BASE_URL // "none"' "$home/.claude/settings.json")" = none
check "bare uninstall names every client it removed" has "$out" "Removed Weave Router from: Claude Code, Codex."

home="$work/codex-only"
install "$home" --codex >/dev/null
out="$(uninstall "$home")"
check "clients without Weave Router are not reported" has "$out" "Removed Weave Router from: Codex."
check "clients without Weave Router print nothing" lacks "$out" "already uninstalled"

home="$work/off"
install "$home" --claude --no-attribution >/dev/null
run_env "$home" bash "$installer" off --claude --scope user >/dev/null
out="$(uninstall "$home")"
check "bare uninstall finds a Claude Code install that is turned off" has "$out" "Removed Weave Router from: Claude Code."

out="$(uninstall "$work/empty")"
check "nothing installed says so" has "$out" "nothing to remove"

home="$work/explicit"
install "$home" --claude >/dev/null
install "$home" --codex >/dev/null
uninstall "$home" --claude >/dev/null
check "an explicit --claude uninstall leaves Codex alone" grep -q 'model_providers.weave' "$home/.codex/config.toml"

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
