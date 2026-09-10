#!/usr/bin/env bash
#
# Regression tests for install/codex-toggle.sh, the UserPromptSubmit hook that
# answers the four local-config toggles without an inference turn.
#
# A fake npx keeps this offline and lets the tests assert exactly which
# subcommand the hook chose. The overriding property under test is FAIL OPEN:
# anything the hook does not positively recognize has to reach the model
# untouched, because a hook that swallows a prompt is worse than one that
# never fires.

# Every directive under test is a literal `$name` that Codex sends verbatim,
# so single quotes are the point here, not an oversight. Must precede the
# first command to apply to the whole file.
# shellcheck disable=SC2016

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
src="$script_dir/../codex-toggle.sh"
[ -f "$src" ] || { echo "cannot find hook at $src" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
fake_bin="$work/bin"
mkdir -p "$fake_bin"

# The installed copy has {{SCOPE}} substituted; render it the way the
# installer does so the test exercises what actually ships.
hook="$work/codex-toggle.sh"
sed 's/{{SCOPE}}//g' "$src" >"$hook"
chmod +x "$hook"
grep -Fq '{{SCOPE}}' "$hook" && { echo "scope token survived rendering" >&2; exit 1; }

# Fake npx records its arguments and prints a recognizable line.
cat >"$fake_bin/npx" <<'FAKE'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$NPX_LOG"
printf 'weave-router fake output\n'
FAKE
chmod +x "$fake_bin/npx"

fails=0
check() {
  local name="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then
    printf '  ok   %s\n' "$name"
  else
    printf '  FAIL %s\n       expected: %s\n       actual:   %s\n' "$name" "$expected" "$actual"
    fails=$((fails + 1))
  fi
}

contains() {
  local name="$1" needle="$2" haystack="$3"
  case "$haystack" in
    *"$needle"*) printf '  ok   %s\n' "$name" ;;
    *) printf '  FAIL %s\n       missing: %s\n       in:      %s\n' "$name" "$needle" "$haystack"
       fails=$((fails + 1)) ;;
  esac
}

# run_hook PROMPT -> prints the hook's stdout. What npx saw is read back with
# npx_log, not through a variable: run_hook is used in a command substitution,
# so anything it assigns dies with the subshell.
run_hook() {
  local prompt="$1"
  : >"$work/npx.log"
  jq -Rn --arg p "$prompt" '{prompt:$p, session_id:"sess-1", model:"gpt-5.6-sol"}' \
    | NPX_LOG="$work/npx.log" PATH="$fake_bin:$PATH" bash "$hook"
}

npx_log() { cat "$work/npx.log" 2>/dev/null || true; }

passthrough='{"continue":true}'

echo
echo "fail open"

check "ordinary prose reaches the model" \
  "$passthrough" "$(run_hook 'what does $router-off do?')"

check "a bare word is not a directive" \
  "$passthrough" "$(run_hook 'hello')"

# The router answers these itself; a hook that also claimed them would
# re-create the duplicate-path problem #1257 closed.
for directive in '$fm astra' '$rf - too slow' '$router-session' '$router-models'; do
  check "router-owned $directive is left to the router" \
    "$passthrough" "$(run_hook "$directive")"
done

# These four take no arguments, so a tail means the user meant something else.
check "a toggle with an argument passes through" \
  "$passthrough" "$(run_hook '$router-off please')"

check "an unknown \$directive passes through" \
  "$passthrough" "$(run_hook '$not-a-real-directive')"

echo
echo "toggles"

out="$(run_hook '$router-status')"
contains "router-status blocks the turn" '"decision":"block"' "$out"
contains "router-status runs the status subcommand" 'weave-router status --codex' "$(npx_log)"
contains "router-status surfaces the command output" 'weave-router fake output' "$out"
# Nothing changed, so the restart note would be noise.
case "$out" in
  *'next `codex` launch'*) printf '  FAIL router-status should not claim a restart is needed\n'; fails=$((fails + 1)) ;;
  *) printf '  ok   router-status omits the restart note\n' ;;
esac

out="$(run_hook '$router-off')"
contains "router-off runs the off subcommand" 'weave-router off --codex' "$(npx_log)"
contains "router-off says when it takes effect" 'next `codex` launch' "$out"

out="$(run_hook '$router-on')"
contains "router-on runs the on subcommand" 'weave-router on --codex' "$(npx_log)"

out="$(run_hook '$disable-routing')"
contains "disable-routing runs the off subcommand" 'weave-router off --codex' "$(npx_log)"
contains "disable-routing names its reversal" '$router-on' "$out"

echo
echo "the response never stops the turn with an empty reason"
# Setting continue:false alongside decision:block makes Codex render a bare
# "Hook stopped" and discard the reason -- the exact bug seen in 0.2.17.
out="$(run_hook '$router-status')"
if printf '%s' "$out" | jq -e 'has("continue")' >/dev/null 2>&1; then
  printf '  FAIL a blocking response must not also carry continue\n'
  fails=$((fails + 1))
else
  printf '  ok   a blocking response carries decision+reason only\n'
fi
reason="$(printf '%s' "$out" | jq -r '.reason // ""')"
if [ -n "$reason" ]; then
  printf '  ok   the blocked turn shows the user a reason\n'
else
  printf '  FAIL blocked with an empty reason\n'
  fails=$((fails + 1))
fi

echo
echo "a missing tool is not a swallowed prompt"
# A PATH with nothing on it would stop `bash` itself from starting, which
# looks like a pass-through failure but tests nothing. Give the hook exactly
# the tools it needs to reach each guard, and withhold the one under test.
minimal_bin="$work/minimal"; mkdir -p "$minimal_bin"
for tool in jq cat mktemp sed sleep; do
  resolved="$(command -v "$tool" 2>/dev/null)" || continue
  ln -sf "$resolved" "$minimal_bin/$tool"
done
out="$(printf '%s' '{"prompt":"$router-off","session_id":"s"}' \
  | PATH="$minimal_bin" "$(command -v bash)" "$hook" 2>/dev/null || true)"
check "no npx on PATH passes through" "$passthrough" "$out"

# Same guard one tool earlier: without jq the hook cannot even build a reply.
jqless_bin="$work/jqless"; mkdir -p "$jqless_bin"
for tool in cat mktemp sed sleep; do
  resolved="$(command -v "$tool" 2>/dev/null)" || continue
  ln -sf "$resolved" "$jqless_bin/$tool"
done
out="$(printf '%s' '{"prompt":"$router-off","session_id":"s"}' \
  | PATH="$jqless_bin" "$(command -v bash)" "$hook" 2>/dev/null || true)"
check "no jq on PATH passes through" "$passthrough" "$out"

echo
echo "the curl-installer copy has not drifted"
# install.sh embeds this hook as a heredoc so a standalone `curl | sh` install
# has no sibling asset to copy. Nothing keeps the two in sync, so an edit to
# one silently ships the other stale -- the same trap codex-status.sh already
# fell into once.
installer="$script_dir/../install.sh"
if [ -f "$installer" ]; then
  start="$(grep -n 'CODEX_TOGGLE_EOF' "$installer" | head -1 | cut -d: -f1)"
  end="$(grep -n 'CODEX_TOGGLE_EOF' "$installer" | tail -1 | cut -d: -f1)"
  if [ -n "$start" ] && [ -n "$end" ] && [ "$end" -gt "$start" ]; then
    awk -v s="$start" -v e="$end" 'NR>s && NR<e' "$installer" >"$work/toggle-heredoc.sh"
    if diff -q "$work/toggle-heredoc.sh" "$src" >/dev/null 2>&1; then
      printf '  ok   install.sh embeds the canonical hook verbatim\n'
    else
      printf '  FAIL install.sh heredoc has drifted from codex-toggle.sh\n'
      diff "$work/toggle-heredoc.sh" "$src" | head -10
      fails=$((fails + 1))
    fi
  else
    printf '  FAIL could not locate the CODEX_TOGGLE_EOF markers in install.sh\n'
    fails=$((fails + 1))
  fi
fi

echo
if [ "$fails" -ne 0 ]; then
  echo "$fails check(s) failed"
  exit 1
fi
echo "Codex toggle hook regression tests passed"
