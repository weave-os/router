#!/usr/bin/env bash
#
# Regression tests for install/cc-statusline.sh.
#
# Fully offline: every case points WEAVE_STATUSLINE_URL at a file:// copy under
# a temp dir, so no test ever reaches raw.githubusercontent.com. Run directly or
# via `make test-statusline`.
#
# Deliberately exercises the script through its real entrypoint (JSON on stdin,
# a transcript on disk) rather than sourcing individual functions — the bugs
# worth catching here live in the interaction between the rate-limit stamps, the
# forked download, and the pricing lookup.

set -uo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
statusline="$script_dir/../cc-statusline.sh"
[ -f "$statusline" ] || { echo "cannot find cc-statusline.sh at $statusline" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

pass=0
fail=0

ok() { echo "  ok   $1"; pass=$((pass + 1)); }
no() { echo "  FAIL $1"; echo "         expected: $2"; echo "         actual:   $3"; fail=$((fail + 1)); }

check() { # check <name> <actual> <expected>
  if [ "$2" = "$3" ]; then ok "$1"; else no "$1" "$3" "$2"; fi
}

check_contains() { # check_contains <name> <haystack> <needle>
  case "$2" in
    *"$3"*) ok "$1" ;;
    *) no "$1" "output containing '$3'" "$2" ;;
  esac
}

check_not_contains() { # check_not_contains <name> <haystack> <needle>
  case "$2" in
    *"$3"*) no "$1" "output WITHOUT '$3'" "$2" ;;
    *) ok "$1" ;;
  esac
}

# The refresh runs in a forked subshell, so its effect lands asynchronously.
# Poll instead of sleeping a fixed amount, which would be both slower and
# flakier on a loaded CI runner.
wait_for() { # wait_for <timeout_seconds> <command...>
  local deadline=$(($(date +%s) + $1))
  shift
  until "$@" 2>/dev/null; do
    [ "$(date +%s)" -lt "$deadline" ] || return 1
    sleep 0.2
  done
}

# On a cold cache the periodic check fires too and would refresh the copy on its
# own, masking whether the price-miss path did anything. Pre-seeding its stamp
# puts it inside its interval so the miss path is the only thing that can act.
seed_periodic_stamp() { # seed_periodic_stamp <cache_home> <script_path>
  local dir="$1/weave-router"
  mkdir -p "$dir"
  : > "$dir/checked-at$(printf '%s' "$2" | tr -c 'A-Za-z0-9._-' '_')"
}

count_stamps() { # count_stamps <cache_home> [<name_filter>]
  local dir="$1/weave-router"
  [ -d "$dir" ] || { echo 0; return 0; }
  if [ -n "${2:-}" ]; then
    find "$dir" -type f -name "*$2*" | wc -l | tr -d ' '
  else
    find "$dir" -type f | wc -l | tr -d ' '
  fi
}

# wait_for re-invokes its argv on every poll, so a value that changes over time
# has to be recomputed inside the callee. Interpolating `$(...)` at the call site
# freezes it at its first value and the wait can only ever time out.
stamp_count_is() { # stamp_count_is <cache_home> <filter> <expected>
  [ "$(count_stamps "$1" "$2")" = "$3" ]
}

line_count_at_least() { # line_count_at_least <file> <n>
  [ "$(wc -l < "$1" | tr -d ' ')" -ge "$2" ]
}

# curl must speak file:// for the offline fixtures to work. Fail loudly rather
# than silently skipping the download-dependent cases.
probe="$work/probe.txt"
echo probe > "$probe"
if ! curl -fsS "file://$probe" >/dev/null 2>&1; then
  echo "FATAL: this curl has no file:// support; cannot run offline refresh tests" >&2
  exit 1
fi

# ---------- fixtures ----------

# A transcript with one assistant turn routed to a model that IS priced, so the
# only variable across cases is whether the CC-side selection is priced.
transcript="$work/transcript.jsonl"
cat > "$transcript" <<'JSONL'
{"type":"user","message":{"role":"user","content":"hi"}}
{"type":"assistant","message":{"id":"msg_test_1","model":"deepseek/deepseek-v4-pro","usage":{"input_tokens":10000,"output_tokens":2000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}
JSONL

# "upstream" is the real script; "installed" copies are mutated per-case to
# simulate an older on-disk copy.
upstream="$work/upstream.sh"
cp "$statusline" "$upstream"

# Strip a model's price entries to simulate a copy written before that model
# shipped. PRICED_MODEL stays in the table; STALE_MODEL is removed.
STALE_MODEL="claude-opus-5"

make_installed() { # make_installed <dest> [stale]
  local dest="$1"
  mkdir -p "$(dirname "$dest")"
  if [ "${2:-}" = "stale" ]; then
    grep -v "\"$STALE_MODEL\":" "$upstream" > "$dest"
  else
    cp "$upstream" "$dest"
  fi
  chmod +x "$dest"
}

# Replace the generated price block with syntactically-valid bash holding
# invalid JSON, to prove the jq probe fails closed instead of looping.
make_installed_bad_prices() { # make_installed_bad_prices <dest>
  local dest="$1"
  mkdir -p "$(dirname "$dest")"
  awk '
    /^# BEGIN_GENERATED_PRICES$/ { print; print "prices='"'"'{ not valid json'"'"'"; skip = 1; next }
    /^# END_GENERATED_PRICES$/   { skip = 0 }
    !skip { print }
  ' "$upstream" > "$dest"
  chmod +x "$dest"
}

render() { # render <installed_script> <cache_home> <url> <model_id> [transcript]
  echo "{\"model\":{\"id\":\"$4\"},\"transcript_path\":\"${5-$transcript}\"}" \
    | XDG_CACHE_HOME="$2" WEAVE_STATUSLINE_URL="$3" bash "$1" 2>&1 \
    | sed 's/\x1b\[[0-9;]*m//g'
}

# ---------- cases ----------

echo "cc-statusline.sh"

# Baseline: a priced selection reports real savings. Guards against the whole
# suite passing vacuously because savings never render at all.
c="$work/c1"; mkdir -p "$c/cache"; make_installed "$c/cc.sh"
out="$(render "$c/cc.sh" "$c/cache" "file://$upstream" "$STALE_MODEL")"
check_not_contains "priced selection reports nonzero savings" "$out" 'saved $0.00'
check_contains "priced selection still names the routed model" "$out" "deepseek/deepseek-v4-pro"
check "priced selection writes no miss stamp" "$(count_stamps "$c/cache" .miss.)" 0

# The bug this path exists for: an unpriced selection renders $0.00, and the
# script heals itself for the next turn instead of waiting out the interval.
c="$work/c2"; mkdir -p "$c/cache"; make_installed "$c/cc.sh" stale
seed_periodic_stamp "$c/cache" "$c/cc.sh"
out="$(render "$c/cc.sh" "$c/cache" "file://$upstream" "$STALE_MODEL")"
check_contains "unpriced selection renders \$0.00" "$out" 'saved $0.00'
if wait_for 20 grep -q "\"$STALE_MODEL\":" "$c/cc.sh"; then
  ok "unpriced selection refreshes the on-disk copy"
  out="$(render "$c/cc.sh" "$c/cache" "file://$upstream" "$STALE_MODEL")"
  check_not_contains "next turn reports real savings" "$out" 'saved $0.00'
else
  no "unpriced selection refreshes the on-disk copy" "price entries restored" "still missing after 20s"
fi

# Regression: a transient download failure must not consume the retry interval.
# Before the retry_on_fail flag this left the user on $0.00 for a full 7 days.
c="$work/c3"; mkdir -p "$c/cache"; make_installed "$c/cc.sh" stale
seed_periodic_stamp "$c/cache" "$c/cc.sh"
render "$c/cc.sh" "$c/cache" "file://$work/does-not-exist.sh" "$STALE_MODEL" >/dev/null
if wait_for 20 stamp_count_is "$c/cache" .miss. 0; then
  ok "failed download does not consume the retry interval"
else
  no "failed download does not consume the retry interval" "miss stamp removed" "stamp still present"
fi
out="$(render "$c/cc.sh" "$c/cache" "file://$upstream" "$STALE_MODEL")"
if wait_for 20 grep -q "\"$STALE_MODEL\":" "$c/cc.sh"; then
  ok "retry after a failed download heals the copy"
else
  no "retry after a failed download heals the copy" "price entries restored" "still missing"
fi

# Cost guardrail: a model nothing will ever price must not download per turn.
c="$work/c4"; mkdir -p "$c/cache"; make_installed "$c/cc.sh"
for _ in 1 2 3 4 5; do
  render "$c/cc.sh" "$c/cache" "file://$upstream" "model-nobody-prices" >/dev/null
done
sleep 1
check "never-priced model refreshes at most once per interval" \
  "$(count_stamps "$c/cache" .miss.)" 1
check "miss stamp does not replace the periodic stamp" \
  "$(count_stamps "$c/cache")" 2

# On a cold cache the periodic check and the pricing-miss retry both fire in one
# invocation. They must not share a download path: concurrent curl -o plus mv on
# one temp file can install a truncated script, and a syntax-broken statusline
# can never self-heal because it cannot run its own refresh.
c="$work/c4b"; mkdir -p "$c/cache" "$c/bin"; make_installed "$c/cc.sh" stale
# Resolve curl before the shim dir shadows it, and delegate through that. A
# hardcoded /usr/bin/curl would silently fail its downloads wherever curl lives
# elsewhere, leaving the -o log populated but no refresh actually performed.
real_curl="$(command -v curl)"
cat > "$c/bin/curl" <<'SHIM'
#!/usr/bin/env bash
# Record each download target, then delegate to the real curl.
args=("$@")
for i in "${!args[@]}"; do
  if [ "${args[$i]}" = "-o" ]; then echo "${args[$((i + 1))]}" >> "$WEAVE_TEST_CURL_LOG"; fi
done
exec "$WEAVE_TEST_REAL_CURL" "$@"
SHIM
chmod +x "$c/bin/curl"
curl_log="$c/targets.log"; : > "$curl_log"
echo "{\"model\":{\"id\":\"$STALE_MODEL\"},\"transcript_path\":\"$transcript\"}" \
  | PATH="$c/bin:$PATH" WEAVE_TEST_CURL_LOG="$curl_log" WEAVE_TEST_REAL_CURL="$real_curl" \
    XDG_CACHE_HOME="$c/cache" WEAVE_STATUSLINE_URL="file://$upstream" \
    bash "$c/cc.sh" >/dev/null 2>&1
wait_for 20 line_count_at_least "$curl_log" 2
downloads="$(wc -l < "$curl_log" | tr -d ' ')"
distinct="$(sort -u "$curl_log" | wc -l | tr -d ' ')"
check "cold cache fires both the periodic and price-miss refresh" "$downloads" 2
check "concurrent refreshes use distinct temp files" "$distinct" "$downloads"
# Counting -o targets alone would pass even if every download failed, so assert
# the refresh landed and left a script that still parses — a corrupt one is the
# actual consequence of sharing the temp path.
if wait_for 20 grep -q "\"$STALE_MODEL\":" "$c/cc.sh"; then
  ok "concurrent refreshes actually install the new script"
else
  no "concurrent refreshes actually install the new script" "price entries restored" "still missing"
fi
if bash -n "$c/cc.sh" 2>/dev/null; then
  ok "script surviving concurrent refreshes still parses"
else
  no "script surviving concurrent refreshes still parses" "valid bash" "syntax error"
fi

# Malformed pricing must fail closed: no crash, no refresh loop.
c="$work/c5"; mkdir -p "$c/cache"; make_installed_bad_prices "$c/cc.sh"
out="$(render "$c/cc.sh" "$c/cache" "file://$upstream" "$STALE_MODEL")"
check_contains "malformed prices still render the routed model" "$out" "deepseek/deepseek-v4-pro"
sleep 1
check "malformed prices trigger no refresh" "$(count_stamps "$c/cache" .miss.)" 0

# Sentinels are not models and can never be priced; refreshing on them would
# download once per sentinel forever.
for sentinel in "?" "failure" "weave-router"; do
  c="$work/c6-$(printf '%s' "$sentinel" | tr -c 'A-Za-z0-9' '_')"
  mkdir -p "$c/cache"; make_installed "$c/cc.sh" stale
  render "$c/cc.sh" "$c/cache" "file://$upstream" "$sentinel" >/dev/null
  sleep 0.5
  check "sentinel '$sentinel' triggers no refresh" "$(count_stamps "$c/cache" .miss.)" 0
done

# Opt-out must suppress every network path, including the miss-triggered one.
c="$work/c7"; mkdir -p "$c/cache"; make_installed "$c/cc.sh" stale
echo "{\"model\":{\"id\":\"$STALE_MODEL\"},\"transcript_path\":\"$transcript\"}" \
  | XDG_CACHE_HOME="$c/cache" WEAVE_STATUSLINE_UPDATE=0 \
    WEAVE_STATUSLINE_URL="file://$upstream" bash "$c/cc.sh" >/dev/null 2>&1
sleep 0.5
check "WEAVE_STATUSLINE_UPDATE=0 writes no stamps" "$(count_stamps "$c/cache")" 0

# A missing transcript is normal on a fresh session.
c="$work/c8"; mkdir -p "$c/cache"; make_installed "$c/cc.sh"
echo "{\"model\":{\"id\":\"$STALE_MODEL\"}}" \
  | XDG_CACHE_HOME="$c/cache" WEAVE_STATUSLINE_URL="file://$upstream" \
    bash "$c/cc.sh" >/dev/null 2>&1
check "missing transcript exits 0" "$?" 0

# install.sh ships the statusline as a heredoc; genprices keeps only the price
# block in sync, so a code edit to one copy silently diverges from the other.
installer="$script_dir/../install.sh"
if [ -f "$installer" ]; then
  start="$(grep -n 'STATUSLINE_EOF' "$installer" | head -1 | cut -d: -f1)"
  end="$(grep -n 'STATUSLINE_EOF' "$installer" | tail -1 | cut -d: -f1)"
  if [ -n "$start" ] && [ -n "$end" ] && [ "$end" -gt "$start" ]; then
    awk -v s="$start" -v e="$end" 'NR>s && NR<e' "$installer" > "$work/heredoc.sh"
    if diff -q "$work/heredoc.sh" "$statusline" >/dev/null 2>&1; then
      ok "install.sh heredoc matches cc-statusline.sh"
    else
      no "install.sh heredoc matches cc-statusline.sh" "byte-identical copies" \
        "$(diff "$work/heredoc.sh" "$statusline" | head -5)"
    fi
  else
    no "install.sh heredoc matches cc-statusline.sh" "locatable STATUSLINE_EOF markers" "not found"
  fi
fi

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
