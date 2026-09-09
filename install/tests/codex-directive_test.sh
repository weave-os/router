#!/usr/bin/env bash
#
# Codex directive hook (UserPromptSubmit) regression tests.
#
# The hook is the deterministic replacement for asking a model to run a skill
# script. Two properties matter more than anything else here:
#
#   1. It NEVER swallows a prompt it does not own. Every failure path — no jq,
#      no curl, no credentials, an unreachable router, a missing session id —
#      must emit {"continue":true} so the user's message reaches the model.
#   2. Arguments reach the router byte-for-byte. The whole reason this hook
#      exists is that a model paraphrasing `$rf - note` silently dropped the
#      leading `-`, turning a thumbs-down verdict into a plain info message.
#
# A mock router stands in for the real one so the wire format is asserted
# directly: the tests read back exactly what the hook POSTed.
#
# Directives are literal text: `$fm`, `$rf` and `$router-session` are what the
# user types and what the hook parses, never shell expansions. The disable is
# file-scoped (it has to precede the first command) because every prompt
# fixture below would otherwise need its own.
# shellcheck disable=SC2016

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
install_dir="$script_dir/.."
hook="${CODEX_DIRECTIVE:-$install_dir/codex-directive.sh}"
[ -f "$hook" ] || { echo "cannot find the directive hook at $hook" >&2; exit 1; }

work="$(mktemp -d)"
mock_pid=""
cleanup() {
  # Capture first: reaping the mock below would otherwise leave the shell
  # exiting 143 (SIGTERM) and turn a green run into a CI failure.
  local status=$?
  if [ -n "$mock_pid" ]; then
    kill "$mock_pid" 2>/dev/null
    # Reap it so the shell does not print its own "Terminated" job notice.
    wait "$mock_pid" 2>/dev/null || true
  fi
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT

pass=0
fail=0
ok()   { printf '  ok   %s\n' "$1"; pass=$((pass + 1)); }
no()   { printf '  FAIL %s\n         expected: %s\n         actual:   %s\n' "$1" "$2" "$3"; fail=$((fail + 1)); }
check() { # check <name> <expected> <actual>
  if [ "$2" = "$3" ]; then ok "$1"; else no "$1" "$2" "$3"; fi
}

command -v jq >/dev/null 2>&1 || { echo "jq is required for these tests" >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "python3 is required for these tests" >&2; exit 1; }

printf 'codex directive hook\n'

# ---------- mock router ----------
#
# Records the request and answers in the OpenAI shape the router's synthetic
# directive short-circuit uses, so the hook's parsing is exercised for real.
# Random high port: a fixed one collides with whatever else a CI runner or a
# developer machine happens to have bound.
port=$(( 20000 + RANDOM % 20000 ))
recorded="$work/recorded.json"
cat >"$work/mock.py" <<'MOCK'
import json, pathlib, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

RECORD = pathlib.Path(sys.argv[2])


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("Content-Length", "0"))).decode()
        RECORD.write_text(json.dumps({
            "path": self.path,
            "session_id": self.headers.get("Session-Id"),
            "router_key": self.headers.get("X-Weave-Router-Key"),
            "app": self.headers.get("X-App"),
            "email": self.headers.get("X-Weave-User-Email"),
            "name": self.headers.get("X-Weave-User-Name"),
            "content": json.loads(raw)["messages"][0]["content"],
        }))
        # MOCK_CONTENT lets a test answer with a non-string content, which a
        # real router should never send but which must not block the turn.
        reply = pathlib.Path(sys.argv[3]).read_text() if len(sys.argv) > 3 and pathlib.Path(sys.argv[3]).exists() \
            else json.dumps("✦ **Weave Router** → gpt-6-astra · pinned by force-model")
        body = json.dumps({"choices": [{"message": {
            "content": json.loads(reply),
        }}]}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):
        pass


HTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
MOCK

codex_home="$work/codex"
mkdir -p "$codex_home"
write_config() { # write_config <base_url>
  cat >"$codex_home/config.toml" <<EOF
model_provider = "weave"

[model_providers.weave]
base_url = "$1"
name = "Weave Router"
wire_api = "responses"

[model_providers.weave.http_headers]
X-App = "codex"
X-Weave-Router-Key = "rk_hooktest"
EOF
}
write_config "http://127.0.0.1:$port/v1"

reply_file="$work/reply.json"
python3 "$work/mock.py" "$port" "$recorded" "$reply_file" &
mock_pid=$!
ready=""
for _ in $(seq 1 60); do
  if curl -fsS -o /dev/null --max-time 1 -X POST \
      -d '{"messages":[{"content":"ping"}]}' \
      "http://127.0.0.1:$port/v1/chat/completions" 2>/dev/null; then
    ready=1
    break
  fi
  sleep 0.25
done
[ -n "$ready" ] || { echo "mock router never came up on port $port" >&2; exit 1; }
rm -f "$recorded"

run_hook() { # run_hook <prompt> <session_id>
  printf '{"prompt":%s,"session_id":%s,"model":"gpt-5.6-sol"}' \
    "$(jq -Rn --arg p "$1" '$p')" "$(jq -Rn --arg s "$2" '$s')" \
    | CODEX_HOME="$codex_home" bash "$hook"
}
decision() { jq -r '.decision // "continue"' <<<"$1"; }
sent() { jq -r '.content // ""' "$recorded" 2>/dev/null; }

# ---------- pass-through: the hook must never eat a prompt it does not own ----------

check "an ordinary prompt passes through" \
  "continue" "$(decision "$(run_hook 'what is 2+2' sess-1)")"
check "prose that merely mentions a directive passes through" \
  "continue" "$(decision "$(run_hook 'explain what $fm does' sess-1)")"
check "an unknown \$directive passes through" \
  "continue" "$(decision "$(run_hook '$deploy prod' sess-1)")"
check "a directive needing an argument passes through without one" \
  "continue" "$(decision "$(run_hook '$fm' sess-1)")"
check "\$rf with no feedback text passes through" \
  "continue" "$(decision "$(run_hook '$rf' sess-1)")"

# A pin keyed on a session the caller is not in is worse than no pin: it would
# silently apply to someone else's thread.
check "a missing session id passes through rather than mis-keying the pin" \
  "continue" "$(decision "$(run_hook '$fm astra' '')")"

# ---------- directives reach the router verbatim ----------

rm -f "$recorded"
out="$(run_hook '$fm astra' sess-abc)"
check "\$fm blocks the turn" "block" "$(decision "$out")"
check "\$fm sends the force-model directive" " /force-model astra" "$(sent)"
check "\$fm forwards the session id" "sess-abc" "$(jq -r '.session_id' "$recorded")"
check "\$fm forwards the router key" "rk_hooktest" "$(jq -r '.router_key' "$recorded")"
check "\$fm identifies the client" "codex" "$(jq -r '.app' "$recorded")"
check "\$fm posts to the chat-completions path" "/v1/chat/completions" "$(jq -r '.path' "$recorded")"
check "the router's marker is shown to the user" \
  "✦ Weave Router → gpt-6-astra · pinned by force-model" \
  "$(jq -r '.reason' <<<"$out")"

rm -f "$recorded"
run_hook '$force-model gpt-6-astra' sess-abc >/dev/null
check "the long form works too" " /force-model gpt-6-astra" "$(sent)"

rm -f "$recorded"
run_hook '$ufm' sess-abc >/dev/null
check "\$ufm needs no argument" " /unforce-model" "$(sent)"

# ---------- the bug this hook exists to fix ----------
#
# A leading -/+ is a router-feedback verdict (splitLeadingRating). Asking a
# model to "pass the feedback text as arguments" dropped it, downgrading a
# thumbs-down to an unrated note. The hook must not touch it.

rm -f "$recorded"
run_hook '$rf - too slow on this one' sess-abc >/dev/null
check "a leading-dash verdict survives verbatim" \
  " /router-feedback - too slow on this one" "$(sent)"

rm -f "$recorded"
run_hook '$rf + nailed it' sess-abc >/dev/null
check "a leading-plus verdict survives verbatim" \
  " /router-feedback + nailed it" "$(sent)"

rm -f "$recorded"
run_hook '$router-feedback 👎 wrong model twice in a row' sess-abc >/dev/null
check "an emoji verdict survives verbatim" \
  " /router-feedback 👎 wrong model twice in a row" "$(sent)"

rm -f "$recorded"
run_hook '$rf   padded   ' sess-abc >/dev/null
check "surrounding whitespace is trimmed but interior spacing is kept" \
  " /router-feedback padded" "$(sent)"

# ---------- \$router-session is answered locally ----------
#
# The session id is already in the hook payload, so this costs neither a model
# turn nor a round trip.

rm -f "$recorded"
out="$(run_hook '$router-session' sess-local)"
check "\$router-session blocks the turn" "block" "$(decision "$out")"
check "\$router-session reports the id" \
  "✦ Weave Router · session id: sess-local" "$(jq -r '.reason' <<<"$out")"
check "\$router-session makes no router call" "" "$(sent)"

# A commented-out example inside the table is not configuration. First match
# wins, so treating one as live would send the router key to a stale endpoint.
cat >"$codex_home/config.toml" <<TOML
[model_providers.weave]
# base_url = "http://127.0.0.1:9/v1"
base_url = "http://127.0.0.1:$port/v1"

[model_providers.weave.http_headers]
# X-Weave-Router-Key = "rk_stale"
X-Weave-Router-Key = "rk_hooktest"
TOML
rm -f "$recorded"
out="$(run_hook '$fm astra' sess-abc)"
check "a commented-out example does not shadow the live endpoint" "block" "$(decision "$out")"
check "and the live key is the one sent" "rk_hooktest" "$(jq -r '.router_key' "$recorded")"
# The same hazard one line over: a comment trailing another assignment. The
# matches are unanchored, so a commented endpoint riding on an earlier line
# wins under first-match unless comments are stripped rather than line-skipped.
cat >"$codex_home/config.toml" <<TOML
[model_providers.weave]
name = "Weave Router" # base_url = "http://127.0.0.1:9/v1"
base_url = "http://127.0.0.1:$port/v1"
wire_api = "responses" # X-Weave-Router-Key = "rk_inline_stale"
http_headers = { "X-Weave-Router-Key" = "rk_hooktest" }
TOML
rm -f "$recorded"
out="$(run_hook '$fm astra' sess-abc)"
check "an inline comment on an earlier line does not shadow the endpoint" "block" "$(decision "$out")"
check "nor the key" "rk_hooktest" "$(jq -r '.router_key' "$recorded")"

# TOML literal strings are single-quoted and have no escapes. A # inside one is
# data too, and treating it as a comment truncates the line -- dropping any key
# that follows it in an inline http_headers table.
cat >"$codex_home/config.toml" <<TOML
[model_providers.weave]
base_url = "http://127.0.0.1:$port/v1"
http_headers = { "X-App" = 'codex#1', "X-Weave-Router-Key" = "rk_hooktest" }
TOML
rm -f "$recorded"
out="$(run_hook '$fm astra' sess-abc)"
check "a # inside a literal string does not truncate the line" "block" "$(decision "$out")"
check "so a key after it is still found" "rk_hooktest" "$(jq -r '.router_key' "$recorded")"

# A # inside a quoted value is data, not a comment, so the value must survive.
cat >"$codex_home/config.toml" <<TOML
[model_providers.weave]
base_url = "http://127.0.0.1:$port/v1"

[model_providers.weave.http_headers]
X-Weave-Router-Key = "rk_hooktest"
X-Weave-User-Name = "Dev #2"
TOML
rm -f "$recorded"
run_hook '$fm astra' sess-abc >/dev/null
check "a # inside a quoted value is kept" "Dev #2" "$(jq -r '.name' "$recorded")"

write_config "http://127.0.0.1:$port/v1"

# ---------- fail open ----------

write_config "http://127.0.0.1:9/v1"
check "an unreachable router passes the prompt through" \
  "continue" "$(decision "$(run_hook '$fm astra' sess-abc)")"

rm -f "$codex_home/config.toml"
check "a missing config passes the prompt through" \
  "continue" "$(decision "$(run_hook '$fm astra' sess-abc)")"

write_config "http://127.0.0.1:$port/v1"
check "a malformed payload passes the prompt through" \
  "continue" "$(decision "$(printf 'not json' | CODEX_HOME="$codex_home" bash "$hook")")"
check "an empty payload passes the prompt through" \
  "continue" "$(decision "$(printf '' | CODEX_HOME="$codex_home" bash "$hook")")"

# A config Codex has rewritten keeps the provider table but loses our comment
# markers and re-emits the header name unquoted; the credential reader must
# still find both. This is the same failure class that broke the installer.
cat >"$codex_home/config.toml" <<EOF
model_provider = "weave"
[model_providers.weave]
base_url = "http://127.0.0.1:$port/v1"
name = "Weave Router"
[model_providers.weave.http_headers]
X-Weave-Router-Key = "rk_hooktest"
EOF
rm -f "$recorded"
check "a Codex-rewritten config still yields credentials" \
  "block" "$(decision "$(run_hook '$fm astra' sess-abc)")"
check "and the directive still reaches the router" " /force-model astra" "$(sent)"

# ---------- identity headers ----------
#
# The installer plants X-Weave-User-Email / X-Weave-User-Name in config.toml so
# a shared router key still attributes turns to a person. A directive turn that
# omitted them would show up unattributed next to that user's ordinary turns.

cat >"$codex_home/config.toml" <<EOF
model_provider = "weave"

[model_providers.weave]
base_url = "http://127.0.0.1:$port/v1"
name = "Weave Router"

[model_providers.weave.http_headers]
X-App = "codex"
X-Weave-Router-Key = "rk_hooktest"
X-Weave-User-Email = "dev@example.invalid"
X-Weave-User-Name = "A Developer"
EOF
rm -f "$recorded"
run_hook '$fm astra' sess-abc >/dev/null
check "the configured email is forwarded" "dev@example.invalid" "$(jq -r '.email' "$recorded")"
check "the configured display name is forwarded" "A Developer" "$(jq -r '.name' "$recorded")"

# write_codex_config escapes backslash and quote before writing, so a display
# name containing a quote round-trips through TOML escaping. Reading it as raw
# text stops at the first escaped quote and forwards a truncated identity.
cat >"$codex_home/config.toml" <<TOML
[model_providers.weave]
base_url = "http://127.0.0.1:$port/v1"

[model_providers.weave.http_headers]
X-Weave-Router-Key = "rk_hooktest"
X-Weave-User-Name = "A \"J\" Developer"
X-Weave-User-Email = "dev@example.invalid"
TOML
rm -f "$recorded"
run_hook '$fm astra' sess-abc >/dev/null
check "a quoted display name is unescaped, not truncated" \
  'A "J" Developer' "$(jq -r '.name' "$recorded")"
check "the key still reads alongside an escaped name" "rk_hooktest" "$(jq -r '.router_key' "$recorded")"

# An install with no identity must send no header at all rather than an empty
# one -- the router treats a present-but-empty header as a value.
write_config "http://127.0.0.1:$port/v1"
rm -f "$recorded"
run_hook '$fm astra' sess-abc >/dev/null
check "no email header when the install has no identity" "null" "$(jq -r '.email' "$recorded")"
check "no name header when the install has no identity" "null" "$(jq -r '.name' "$recorded")"

# ---------- a malformed reply must not block ----------
#
# jq -r renders an object or array as JSON, which is non-empty; blocking on that
# would show the user raw JSON instead of passing their prompt through.

printf '%s' '{"unexpected":"object"}' >"$reply_file"
check "a non-string content passes the prompt through" \
  "continue" "$(decision "$(run_hook '$fm astra' sess-abc)")"
printf '%s' '["an","array"]' >"$reply_file"
check "an array content passes the prompt through" \
  "continue" "$(decision "$(run_hook '$fm astra' sess-abc)")"
printf '%s' 'null' >"$reply_file"
check "a null content passes the prompt through" \
  "continue" "$(decision "$(run_hook '$fm astra' sess-abc)")"
rm -f "$reply_file"
check "a string content still blocks" \
  "block" "$(decision "$(run_hook '$fm astra' sess-abc)")"

# ---------- router-session skill fallback ----------
#
# Reading the newest rollout would name whichever session wrote last, so with no
# CODEX_SESSION_ID the script must fail rather than report another session's id.
# Required, not optional: guarding these on [ -f ] would let the file go missing
# and still report a green run.
emit="$install_dir/codex-skills/router-session/scripts/emit.sh"
if [ -f "$emit" ]; then
  ok "the router-session skill helper is present"
else
  no "the router-session skill helper is present" "$emit" "missing"
fi
check "the skill reports CODEX_SESSION_ID when set" "sess-from-env" \
  "$(CODEX_SESSION_ID=sess-from-env bash "$emit" 2>/dev/null)"
mkdir -p "$work/rollouts/sessions/2026/01/01"
: >"$work/rollouts/sessions/2026/01/01/rollout-2026-01-01T00-00-00-11111111-2222-3333-4444-555555555555.jsonl"
if CODEX_SESSION_ID="" CODEX_HOME="$work/rollouts" bash "$emit" >/dev/null 2>&1; then
  no "the skill fails rather than naming another session" "non-zero exit" "exit 0"
else
  ok "the skill fails rather than naming another session"
fi

# ---------- installer heredoc must not drift ----------
#
# install.sh embeds this helper so the standalone `curl | sh` install has no
# sibling asset to copy. Nothing keeps the two in sync, so an edit to one
# silently ships the other stale.
installer="$install_dir/install.sh"
if [ -f "$installer" ]; then
  start="$(grep -n 'CODEX_DIRECTIVE_EOF' "$installer" | head -1 | cut -d: -f1)"
  end="$(grep -n 'CODEX_DIRECTIVE_EOF' "$installer" | tail -1 | cut -d: -f1)"
  if [ -n "$start" ] && [ -n "$end" ] && [ "$end" -gt "$start" ]; then
    awk -v s="$start" -v e="$end" 'NR>s && NR<e' "$installer" >"$work/heredoc.sh"
    if diff -q "$work/heredoc.sh" "$hook" >/dev/null 2>&1; then
      ok "install.sh heredoc matches codex-directive.sh"
    else
      no "install.sh heredoc matches codex-directive.sh" "identical" "$(diff "$work/heredoc.sh" "$hook" | head -5)"
    fi
  else
    no "install.sh heredoc matches codex-directive.sh" "CODEX_DIRECTIVE_EOF markers" "not found"
  fi
fi

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
