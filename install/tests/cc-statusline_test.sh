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

# `wait_for` runs its argv, so a negated condition needs to be its own command.
not_a_dir() { # not_a_dir <path>
  [ ! -d "$1" ]
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

cache_transcript="$work/cache-transcript.jsonl"
cat > "$cache_transcript" <<'JSONL'
{"type":"assistant","message":{"id":"msg_cache_1","model":"gpt-5.4","usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":1000000}}}
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

# Cache-heavy routes must price each side with its own catalog multiplier.
c="$work/c-cache"; mkdir -p "$c/cache"; make_installed "$c/cc.sh"
out="$(render "$c/cc.sh" "$c/cache" "file://$upstream" "gpt-5.4-pro" "$cache_transcript")"
check_contains "cache reads use per-model multipliers" "$out" 'saved $29.75'

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

# ---------- slash-command refresh ----------
#
# The wrappers under <install>/.claude/commands/ have no other refresh point —
# the statusline is the only thing Claude Code runs every turn. Same offline
# technique: a file:// "upstream" commands dir the fixtures mutate per case.

commands_upstream="$work/commands-upstream"
mkdir -p "$commands_upstream"
cp "$script_dir/../commands"/*.md "$commands_upstream/"

# Lay out a user-scope install: statusline under .weave/, wrappers under
# .claude/commands/, and a seeded baseline (what install.sh writes) holding the
# unrendered canonical bodies.
make_command_install() { # make_command_install <root> <cache_home> [scope_args]
  local root="$1" cache="$2" scope_args="${3:-}" name body
  mkdir -p "$root/.weave" "$root/.claude/commands"
  cp "$upstream" "$root/.weave/cc-statusline.sh"
  chmod +x "$root/.weave/cc-statusline.sh"
  local baseline="$cache/weave-router/commands$(printf '%s' "$(cd "$root/.claude/commands" && pwd -P)" | tr -c 'A-Za-z0-9._-' '_')"
  mkdir -p "$baseline"
  for name in "$script_dir/../commands"/*.md; do
    body="$(cat "$name")"
    # Mirror install_slash_commands: rendered body plus the ownership marker.
    printf '%s\n<!-- weave-router managed command: %s -->' \
      "${body//\{\{SCOPE\}\}/$scope_args}" "$(basename "$name" .md)" \
      >"$root/.claude/commands/$(basename "$name")"
    cp "$name" "$baseline/$(basename "$name")"
  done
}

# WEAVE_STATUSLINE_UPDATE=0 is a master kill switch that covers this path too,
# so these cases can't use it to isolate the wrapper refresh. Point the script's
# own refresh at the same offline fixture instead — it lands as a content no-op.
sync_commands() { # sync_commands <root> <cache_home> <commands_url_base>
  echo "{\"model\":{\"id\":\"$STALE_MODEL\"},\"transcript_path\":\"$transcript\"}" \
    | XDG_CACHE_HOME="$2" WEAVE_STATUSLINE_URL="file://$upstream" WEAVE_COMMANDS_URL_BASE="$3" \
      bash "$1/.weave/cc-statusline.sh" >/dev/null 2>&1
}

# An upstream wrapper change reaches an untouched install.
c="$work/cmd1"; mkdir -p "$c/cache"; make_command_install "$c/root" "$c/cache"
printf '%s\n' '---' 'description: refreshed fm.' '---' '' '/force-model $ARGUMENTS' \
  >"$commands_upstream/fm.md"
sync_commands "$c/root" "$c/cache" "file://$commands_upstream"
if wait_for 20 grep -q 'refreshed fm.' "$c/root/.claude/commands/fm.md"; then
  ok "an upstream wrapper change reaches the install"
else
  no "an upstream wrapper change reaches the install" "refreshed body" "$(cat "$c/root/.claude/commands/fm.md")"
fi

# A wrapper the user edited is theirs. Overwriting it is the one unrecoverable
# mistake here, so the baseline comparison must veto the swap.
c="$work/cmd2"; mkdir -p "$c/cache"; make_command_install "$c/root" "$c/cache"
printf '%s\n' 'MY OWN WRAPPER' >"$c/root/.claude/commands/rf.md"
printf '%s\n' '---' 'description: refreshed rf.' '---' '' '/router-feedback $ARGUMENTS' \
  >"$commands_upstream/rf.md"
sync_commands "$c/root" "$c/cache" "file://$commands_upstream"
sleep 1
check "a user-edited wrapper is never overwritten" \
  "$(cat "$c/root/.claude/commands/rf.md")" "MY OWN WRAPPER"

# A project-scope install writes wrappers straight into the repo's own
# .claude/commands/ and (unlike cc-statusline.sh) does not gitignore them, so a
# git-tracked wrapper must never be rewritten by the unattended weekly refresh —
# that would surface as unexplained dirty files in someone's working tree.
c="$work/cmd2b"; mkdir -p "$c/cache"; make_command_install "$c/root" "$c/cache"
( cd "$c/root" && git init -q . && git add .claude/commands/fm.md && git commit -q -m "commit wrappers" )
before="$(cat "$c/root/.claude/commands/fm.md")"
printf '%s\n' '---' 'description: refreshed fm again.' '---' '' '/force-model $ARGUMENTS' \
  >"$commands_upstream/fm.md"
sync_commands "$c/root" "$c/cache" "file://$commands_upstream"
sleep 1
check "a git-tracked wrapper is never overwritten" \
  "$(cat "$c/root/.claude/commands/fm.md")" "$before"

# The router-* wrappers bake this install's scope into their npx line. A
# refresh that dropped it would point a project install's toggle at the
# user-scope config — silently flipping the wrong install.
c="$work/cmd3"; mkdir -p "$c/cache"
make_command_install "$c/root" "$c/cache" " --scope project"
printf '%s\n' '---' 'description: refreshed off.' 'allowed-tools: Bash(npx:*)' '---' '' \
  'Turn it off:' '' '`npx @workweave/router off --claude{{SCOPE}}`' \
  >"$commands_upstream/router-off.md"
sync_commands "$c/root" "$c/cache" "file://$commands_upstream"
if wait_for 20 grep -q 'refreshed off.' "$c/root/.claude/commands/router-off.md"; then
  check_contains "refreshed router-off keeps this install's scope" \
    "$(cat "$c/root/.claude/commands/router-off.md")" 'off --claude --scope project'
else
  no "refreshed router-off keeps this install's scope" "refreshed body" "not refreshed"
fi

# A 404 (or any HTML error page) must never land as a slash command.
c="$work/cmd4"; mkdir -p "$c/cache"; make_command_install "$c/root" "$c/cache"
before="$(cat "$c/root/.claude/commands/fm.md")"
sync_commands "$c/root" "$c/cache" "file://$work/no-such-commands-dir"
sleep 1
check "a failed fetch leaves the installed wrapper alone" \
  "$(cat "$c/root/.claude/commands/fm.md")" "$before"

# A wrapper the user uninstalled must stay uninstalled — resurrecting it is a
# surprise, and the refresh has no way to know it was ever wanted.
c="$work/cmd5"; mkdir -p "$c/cache"; make_command_install "$c/root" "$c/cache"
rm -f "$c/root/.claude/commands/ufm.md"
sync_commands "$c/root" "$c/cache" "file://$commands_upstream"
sleep 1
if [ -e "$c/root/.claude/commands/ufm.md" ]; then
  no "a removed wrapper is not resurrected" "still absent" "file re-created"
else
  ok "a removed wrapper is not resurrected"
fi

# Opt-out has to cover this path too, not just the script's own refresh.
c="$work/cmd6"; mkdir -p "$c/cache"; make_command_install "$c/root" "$c/cache"
before="$(cat "$c/root/.claude/commands/fm.md")"
echo "{\"model\":{\"id\":\"$STALE_MODEL\"},\"transcript_path\":\"$transcript\"}" \
  | XDG_CACHE_HOME="$c/cache" WEAVE_STATUSLINE_URL="file://$upstream" WEAVE_COMMANDS_UPDATE=0 \
    WEAVE_COMMANDS_URL_BASE="file://$commands_upstream" \
    bash "$c/root/.weave/cc-statusline.sh" >/dev/null 2>&1
sleep 1
check "WEAVE_COMMANDS_UPDATE=0 suppresses the wrapper refresh" \
  "$(cat "$c/root/.claude/commands/fm.md")" "$before"

# The master kill switch has to cover the wrapper refresh too, not just the
# script's own — a user who opted out of one network path opted out of both.
c="$work/cmd7"; mkdir -p "$c/cache"; make_command_install "$c/root" "$c/cache"
before="$(cat "$c/root/.claude/commands/fm.md")"
echo "{\"model\":{\"id\":\"$STALE_MODEL\"},\"transcript_path\":\"$transcript\"}" \
  | XDG_CACHE_HOME="$c/cache" WEAVE_STATUSLINE_UPDATE=0 \
    WEAVE_COMMANDS_URL_BASE="file://$commands_upstream" \
    bash "$c/root/.weave/cc-statusline.sh" >/dev/null 2>&1
sleep 1
check "WEAVE_STATUSLINE_UPDATE=0 suppresses the wrapper refresh too" \
  "$(cat "$c/root/.claude/commands/fm.md")" "$before"

# ---------- org "hide terminal surfaces" gate ----------
#
# The gate decides from a per-install cache file only; the network fetch runs
# in a detached background refresh that repopulates the cache for the NEXT
# invocation, and a missing or stale cache fails open. These cases stub
# GET /v1/display-settings via the file:// seam: WEAVE_ROUTER_BASE_URL or the
# install's ANTHROPIC_BASE_URL accepts a file:// URL, which curl reads as the
# response body, exercising the full parse-and-decide path without a network.
# The key just needs to be present and non-empty for the refresh to proceed.
#
# write_install <base> <scope:user|project> <key> <url> — lay down a statusline
# copy plus the settings.json/settings.local.json the gate reads, in the layout
# the installer uses for that scope.
write_install() { # write_install <base> <scope> <key> <display_settings_url>
  local base="$1" scope="$2" key="$3" url="$4"
  if [ "$scope" = "user" ]; then
    mkdir -p "$base/.weave" "$base/.claude"
    cp "$upstream" "$base/.weave/cc-statusline.sh"
    cat > "$base/.claude/settings.json" <<EOF
{"env":{"ANTHROPIC_BASE_URL":"$url","ANTHROPIC_CUSTOM_HEADERS":"X-Weave-Router-Key: $key"}}
EOF
    chmod +x "$base/.weave/cc-statusline.sh"
  else
    mkdir -p "$base/.claude"
    cp "$upstream" "$base/.claude/cc-statusline.sh"
    cat > "$base/.claude/settings.json" <<EOF
{"env":{"ANTHROPIC_BASE_URL":"$url"}}
EOF
    cat > "$base/.claude/settings.local.json" <<EOF
{"env":{"ANTHROPIC_CUSTOM_HEADERS":"X-Weave-Router-Key: $key"}}
EOF
    chmod +x "$base/.claude/cc-statusline.sh"
  fi
}

# gate_cache <cache_home> <script> — the cache file the gate reads/writes for
# this install (mirrors the slug derivation in cc-statusline.sh).
gate_cache() {
  echo "$1/weave-router/display-settings$(printf '%s' "$2" | tr -c 'A-Za-z0-9._-' '_')"
}

# Hidden org goes blank via the cache: the first run fails open while the
# background refresh caches hide=true, and the second run decides from it.
c="$work/g1"; mkdir -p "$c/proj/.claude" "$c/cache"
printf '{"hide_terminal_surfaces": true}' > "$c/ds.json"
write_install "$c/proj" project "rk_proj_hidden" "file://$c/ds.json"
cache_file="$(gate_cache "$c/cache" "$c/proj/.claude/cc-statusline.sh")"
echo "{\"model\":{\"id\":\"$STALE_MODEL\"},\"transcript_path\":\"$transcript\"}" \
  | XDG_CACHE_HOME="$c/cache" WEAVE_STATUSLINE_UPDATE=0 WEAVE_COMMANDS_UPDATE=0 HOME="$c/home" \
    WEAVE_ROUTER_BASE_URL= ANTHROPIC_BASE_URL= WEAVE_ROUTER_KEY= ANTHROPIC_CUSTOM_HEADERS= \
    bash "$c/proj/.claude/cc-statusline.sh" >/dev/null 2>&1
wait_for 5 test -f "$cache_file"
check "background refresh caches the hidden setting" "$(cat "$cache_file" 2>/dev/null)" "1"
out="$(echo "{\"model\":{\"id\":\"$STALE_MODEL\"},\"transcript_path\":\"$transcript\"}" \
  | XDG_CACHE_HOME="$c/cache" WEAVE_STATUSLINE_UPDATE=0 WEAVE_COMMANDS_UPDATE=0 HOME="$c/home" \
    WEAVE_ROUTER_BASE_URL= ANTHROPIC_BASE_URL= WEAVE_ROUTER_KEY= ANTHROPIC_CUSTOM_HEADERS= \
    bash "$c/proj/.claude/cc-statusline.sh" 2>&1)"
check "project-scope hidden org renders a blank statusline" "$out" ""

# The same project install must NOT fall back to the user-scope key: with a
# hidden project org but a visible user org in $HOME, the refresh caches from
# the project key ("1"), not the user key ("0"), and the statusline goes blank.
c="$work/g2"; mkdir -p "$c/proj/.claude" "$c/home/.claude" "$c/cache"
printf '{"hide_terminal_surfaces": true}' > "$c/ds_proj.json"
printf '{"hide_terminal_surfaces": false}' > "$c/ds_user.json"
write_install "$c/proj" project "rk_proj_hidden" "file://$c/ds_proj.json"
write_install "$c/home" user "rk_user_visible" "file://$c/ds_user.json"
cache_file="$(gate_cache "$c/cache" "$c/proj/.claude/cc-statusline.sh")"
echo "{\"model\":{\"id\":\"$STALE_MODEL\"},\"transcript_path\":\"$transcript\"}" \
  | XDG_CACHE_HOME="$c/cache" WEAVE_STATUSLINE_UPDATE=0 WEAVE_COMMANDS_UPDATE=0 HOME="$c/home" \
    WEAVE_ROUTER_BASE_URL= ANTHROPIC_BASE_URL= WEAVE_ROUTER_KEY= ANTHROPIC_CUSTOM_HEADERS= \
    bash "$c/proj/.claude/cc-statusline.sh" >/dev/null 2>&1
wait_for 5 test -f "$cache_file"
check "project install caches from its own key, not the user-scope one" \
  "$(cat "$cache_file" 2>/dev/null)" "1"
out="$(echo "{\"model\":{\"id\":\"$STALE_MODEL\"},\"transcript_path\":\"$transcript\"}" \
  | XDG_CACHE_HOME="$c/cache" WEAVE_STATUSLINE_UPDATE=0 WEAVE_COMMANDS_UPDATE=0 HOME="$c/home" \
    WEAVE_ROUTER_BASE_URL= ANTHROPIC_BASE_URL= WEAVE_ROUTER_KEY= ANTHROPIC_CUSTOM_HEADERS= \
    bash "$c/proj/.claude/cc-statusline.sh" 2>&1)"
check "project install hides via its own org setting" "$out" ""

# Visible org (hidden=false) caches "0" and keeps rendering normally.
c="$work/g3"; mkdir -p "$c/proj/.claude" "$c/cache"
printf '{"hide_terminal_surfaces": false}' > "$c/ds.json"
write_install "$c/proj" project "rk_proj_visible" "file://$c/ds.json"
cache_file="$(gate_cache "$c/cache" "$c/proj/.claude/cc-statusline.sh")"
echo "{\"model\":{\"id\":\"$STALE_MODEL\"},\"transcript_path\":\"$transcript\"}" \
  | XDG_CACHE_HOME="$c/cache" WEAVE_STATUSLINE_UPDATE=0 WEAVE_COMMANDS_UPDATE=0 HOME="$c/home" \
    WEAVE_ROUTER_BASE_URL= ANTHROPIC_BASE_URL= WEAVE_ROUTER_KEY= ANTHROPIC_CUSTOM_HEADERS= \
    bash "$c/proj/.claude/cc-statusline.sh" >/dev/null 2>&1
wait_for 5 test -f "$cache_file"
check "visible org caches the visible setting" "$(cat "$cache_file" 2>/dev/null)" "0"
out="$(echo "{\"model\":{\"id\":\"$STALE_MODEL\"},\"transcript_path\":\"$transcript\"}" \
  | XDG_CACHE_HOME="$c/cache" WEAVE_STATUSLINE_UPDATE=0 WEAVE_COMMANDS_UPDATE=0 HOME="$c/home" \
    WEAVE_ROUTER_BASE_URL= ANTHROPIC_BASE_URL= WEAVE_ROUTER_KEY= ANTHROPIC_CUSTOM_HEADERS= \
    bash "$c/proj/.claude/cc-statusline.sh" 2>&1 | sed 's/\x1b\[[0-9;]*m//g')"
check_contains "visible org still renders the statusline" "$out" "deepseek/deepseek-v4-pro"

# A fresh hidden cache decides without any network access: the install points
# at an unreachable endpoint, but the pre-seeded fresh cache blanks the
# statusline on the very first run.
c="$work/g4"; mkdir -p "$c/proj/.claude" "$c/cache"
write_install "$c/proj" project "rk_key" "file://$c/missing.json"
cache_file="$(gate_cache "$c/cache" "$c/proj/.claude/cc-statusline.sh")"
mkdir -p "$(dirname "$cache_file")"
printf '1' > "$cache_file"
out="$(echo "{\"model\":{\"id\":\"$STALE_MODEL\"},\"transcript_path\":\"$transcript\"}" \
  | XDG_CACHE_HOME="$c/cache" WEAVE_STATUSLINE_UPDATE=0 WEAVE_COMMANDS_UPDATE=0 HOME="$c/home" \
    WEAVE_ROUTER_BASE_URL= ANTHROPIC_BASE_URL= WEAVE_ROUTER_KEY= ANTHROPIC_CUSTOM_HEADERS= \
    bash "$c/proj/.claude/cc-statusline.sh" 2>&1)"
check "fresh hidden cache blanks the statusline without touching the network" "$out" ""

# A STALE hidden cache must fail open, not pin the gate closed: with the TTL
# forced to zero the pre-seeded "1" is stale, the endpoint is unreachable, and
# the statusline renders normally instead of staying hidden forever.
c="$work/g5"; mkdir -p "$c/proj/.claude" "$c/cache"
write_install "$c/proj" project "rk_key" "file://$c/missing.json"
cache_file="$(gate_cache "$c/cache" "$c/proj/.claude/cc-statusline.sh")"
mkdir -p "$(dirname "$cache_file")"
printf '1' > "$cache_file"
out="$(echo "{\"model\":{\"id\":\"$STALE_MODEL\"},\"transcript_path\":\"$transcript\"}" \
  | XDG_CACHE_HOME="$c/cache" WEAVE_STATUSLINE_UPDATE=0 WEAVE_COMMANDS_UPDATE=0 HOME="$c/home" \
    WEAVE_ROUTER_BASE_URL= ANTHROPIC_BASE_URL= WEAVE_ROUTER_KEY= ANTHROPIC_CUSTOM_HEADERS= \
    WEAVE_DISPLAY_SETTINGS_TTL_SECONDS=0 \
    bash "$c/proj/.claude/cc-statusline.sh" 2>&1 | sed 's/\x1b\[[0-9;]*m//g')"
check_contains "stale hidden cache fails open when the router is unreachable" "$out" "deepseek/deepseek-v4-pro"
sleep 1
check "failed refresh leaves the stale cache untouched" "$(cat "$cache_file" 2>/dev/null)" "1"

# Cache miss with an unreachable router renders normally (fail-open on first
# run) and the failed refresh leaves no cache file behind.
c="$work/g6"; mkdir -p "$c/proj/.claude" "$c/cache"
write_install "$c/proj" project "rk_key" "file://$c/missing.json"
cache_file="$(gate_cache "$c/cache" "$c/proj/.claude/cc-statusline.sh")"
out="$(echo "{\"model\":{\"id\":\"$STALE_MODEL\"},\"transcript_path\":\"$transcript\"}" \
  | XDG_CACHE_HOME="$c/cache" WEAVE_STATUSLINE_UPDATE=0 WEAVE_COMMANDS_UPDATE=0 HOME="$c/home" \
    WEAVE_ROUTER_BASE_URL= ANTHROPIC_BASE_URL= WEAVE_ROUTER_KEY= ANTHROPIC_CUSTOM_HEADERS= \
    bash "$c/proj/.claude/cc-statusline.sh" 2>&1 | sed 's/\x1b\[[0-9;]*m//g')"
check_contains "cache miss with an unreachable router renders normally" "$out" "deepseek/deepseek-v4-pro"
sleep 1
check "failed refresh writes no cache file" "$(test -f "$cache_file" && echo present || echo absent)" "absent"

# Concurrent refreshes must never be in flight at the same time: two
# overlapping requests can complete out of order, and the loser's write would
# replace a newer setting with an older one AND stamp it fresh, pinning the
# gate on a stale value for a whole TTL. The curl shim below stalls the first
# responder on hide=true until a second answers hide=false, so an unserialized
# implementation has both in flight together and ends on the stale "1".
c="$work/g7"; mkdir -p "$c/proj/.claude" "$c/cache" "$c/bin"
write_install "$c/proj" project "rk_key" "file://$c/ds.json"
printf '{"hide_terminal_surfaces": true}' > "$c/ds.json"
cache_file="$(gate_cache "$c/cache" "$c/proj/.claude/cc-statusline.sh")"
cat > "$c/bin/curl" <<'SHIM'
#!/usr/bin/env bash
# Claim a turn number atomically — mkdir is the test-and-set; a read-then-write
# counter races here exactly as it would in the code under test.
n=1
while ! mkdir "$WEAVE_TEST_TURNS/$n" 2>/dev/null; do n=$((n + 1)); done
echo "$n" >> "$WEAVE_TEST_CALLS"
# Announce this fetch for its whole duration; a concurrent one records a
# violation. This is what the mutex must make impossible.
[ -e "$WEAVE_TEST_INFLIGHT" ] && echo overlap >> "$WEAVE_TEST_OVERLAP"
: > "$WEAVE_TEST_INFLIGHT"
if [ "$n" -eq 1 ]; then
  # Stall the stale responder until the fresh one has written, or until the
  # deadline passes (serialized runs never produce a second call, and this
  # test must not hang waiting for one that will never come).
  for _ in $(seq 1 60); do
    [ -f "$WEAVE_TEST_RELEASE" ] && break
    sleep 0.05
  done
  rm -f "$WEAVE_TEST_INFLIGHT"
  printf '{"hide_terminal_surfaces": true}'
else
  rm -f "$WEAVE_TEST_INFLIGHT"
  printf '{"hide_terminal_surfaces": false}'
  : > "$WEAVE_TEST_RELEASE"
fi
SHIM
chmod +x "$c/bin/curl"
mkdir -p "$c/turns"; : > "$c/calls"; : > "$c/overlap"; rm -f "$c/inflight"
for _ in 1 2; do
  echo "{\"model\":{\"id\":\"$STALE_MODEL\"},\"transcript_path\":\"$transcript\"}" \
    | PATH="$c/bin:$PATH" XDG_CACHE_HOME="$c/cache" WEAVE_STATUSLINE_UPDATE=0 \
      WEAVE_COMMANDS_UPDATE=0 HOME="$c/home" WEAVE_ROUTER_BASE_URL= ANTHROPIC_BASE_URL= \
      WEAVE_ROUTER_KEY= ANTHROPIC_CUSTOM_HEADERS= WEAVE_TEST_TURNS="$c/turns" \
      WEAVE_TEST_CALLS="$c/calls" WEAVE_TEST_RELEASE="$c/release" \
      WEAVE_TEST_INFLIGHT="$c/inflight" WEAVE_TEST_OVERLAP="$c/overlap" \
      bash "$c/proj/.claude/cc-statusline.sh" >/dev/null 2>&1 &
done
wait
# Both refreshes are detached, so the foreground exit says nothing about them.
wait_for 15 test -f "$cache_file"
sleep 3
# Mutual exclusion is the property under test, so assert on overlap rather than
# a fetch count: two invocations that happen to serialize (the first finishes
# and releases before the second starts) fetch twice quite legitimately, and on
# a slow runner that is what happens. Asserting the cached VALUE would be
# tautological — with one fetch it is the first responder's either way.
check "concurrent refreshes never fetch at the same time" \
  "$(wc -l < "$c/overlap" | tr -d ' ')" 0
# Guard against the above passing vacuously because nothing ever fetched.
if [ "$(wc -l < "$c/calls" | tr -d ' ')" -ge 1 ]; then
  ok "a stale cache does trigger a background refresh"
else
  no "a stale cache does trigger a background refresh" "at least one fetch" "no fetch happened"
fi

# An ABANDONED lock (crashed holder) must not block refreshes forever: a
# pre-seeded lock older than the reclaim threshold gets taken over, the fetch
# happens, and the lock is released rather than leaked.
c="$work/g8"; mkdir -p "$c/proj/.claude" "$c/cache" "$c/bin"
write_install "$c/proj" project "rk_key" "file://$c/ds.json"
printf '{"hide_terminal_surfaces": true}' > "$c/ds.json"
cache_file="$(gate_cache "$c/cache" "$c/proj/.claude/cc-statusline.sh")"
mkdir -p "$(dirname "$cache_file")"
mkdir -p "$cache_file.lock"
touch -t 202001010000 "$cache_file.lock"
echo "{\"model\":{\"id\":\"$STALE_MODEL\"},\"transcript_path\":\"$transcript\"}" \
  | XDG_CACHE_HOME="$c/cache" WEAVE_STATUSLINE_UPDATE=0 WEAVE_COMMANDS_UPDATE=0 HOME="$c/home" \
    WEAVE_ROUTER_BASE_URL= ANTHROPIC_BASE_URL= WEAVE_ROUTER_KEY= ANTHROPIC_CUSTOM_HEADERS= \
    bash "$c/proj/.claude/cc-statusline.sh" >/dev/null 2>&1
wait_for 15 test -f "$cache_file"
check "an abandoned lock is reclaimed rather than blocking refreshes forever" \
  "$(cat "$cache_file" 2>/dev/null)" "1"
# The cache is written just before the lock is released, so sampling right
# after the write catches the holder mid-release. Poll for the release.
if wait_for 15 not_a_dir "$cache_file.lock"; then
  ok "a reclaimed lock is released, not leaked"
else
  no "a reclaimed lock is released, not leaked" "lock removed" "still held after 15s"
fi

# A lock a live holder still owns must be left alone: a fresh lock means a
# refresh is already in flight, so this invocation must not fetch behind it.
c="$work/g9"; mkdir -p "$c/proj/.claude" "$c/cache"
write_install "$c/proj" project "rk_key" "file://$c/ds.json"
printf '{"hide_terminal_surfaces": true}' > "$c/ds.json"
cache_file="$(gate_cache "$c/cache" "$c/proj/.claude/cc-statusline.sh")"
mkdir -p "$(dirname "$cache_file")"
mkdir -p "$cache_file.lock"
echo "{\"model\":{\"id\":\"$STALE_MODEL\"},\"transcript_path\":\"$transcript\"}" \
  | XDG_CACHE_HOME="$c/cache" WEAVE_STATUSLINE_UPDATE=0 WEAVE_COMMANDS_UPDATE=0 HOME="$c/home" \
    WEAVE_ROUTER_BASE_URL= ANTHROPIC_BASE_URL= WEAVE_ROUTER_KEY= ANTHROPIC_CUSTOM_HEADERS= \
    bash "$c/proj/.claude/cc-statusline.sh" >/dev/null 2>&1
sleep 2
check "a lock a live holder owns is left alone" \
  "$(test -f "$cache_file" && echo wrote || echo skipped)" "skipped"

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
