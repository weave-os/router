#!/usr/bin/env bash
#
# Installed scripts update only from the published npm release.
#
# Every updater (statusline, Codex status helper, slash-command wrappers) and
# the curl-piped `install.sh --uninstall` used to fetch from the repository's
# mutable main branch and run whatever came back after a shape check, so one
# push reached every developer machine. A curl shim stands in for the network:
# raw.githubusercontent.com serves an attacker's branch, registry.npmjs.org a
# local fake registry. Fully offline.

set -uo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
install_dir="$script_dir/.."

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

pass=0
fail=0
ok() { echo "  ok   $1"; pass=$((pass + 1)); }
no() { echo "  FAIL $1"; echo "         expected: $2"; echo "         actual:   $3"; fail=$((fail + 1)); }

wait_for() { # wait_for <timeout_seconds> <command...>
  local deadline=$(($(date +%s) + $1))
  shift
  until "$@" 2>/dev/null; do
    [ "$(date +%s)" -lt "$deadline" ] || return 1
    sleep 0.2
  done
}
same_file() { cmp -s "$1" "$2"; }
logged() { grep -qF "$2" "$1"; }

# ---------- network stand-in ----------

real_curl="$(command -v curl)"
registry="$work/registry"
branch="$work/branch"
mkdir -p "$work/bin" "$registry/@weave-os/router/-" "$branch/install/commands"
cat >"$work/bin/curl" <<'SHIM'
#!/usr/bin/env bash
args=()
for a in "$@"; do
  case "$a" in https://*) echo "$a" >>"$WEAVE_TEST_CURL_LOG" ;; esac
  case "$a" in
    https://registry.npmjs.org/*) a="file://$WEAVE_TEST_REGISTRY/${a#https://registry.npmjs.org/}" ;;
    https://raw.githubusercontent.com/weave-os/router/main/*) a="file://$WEAVE_TEST_BRANCH/${a#https://raw.githubusercontent.com/weave-os/router/main/}" ;;
    https://*) exit 7 ;;
  esac
  args+=("$a")
done
exec "$WEAVE_TEST_REAL_CURL" "${args[@]}"
SHIM
chmod +x "$work/bin/curl"

# The attacker's push: every path the updaters used to read from main now
# carries a payload that leaves a canary behind when it runs.
canary="$work/canary"
payload() { # payload <real_script>
  cat "$1"
  printf '\necho pwned >>%q\n' "$canary"
}
payload "$install_dir/cc-statusline.sh" >"$branch/install/cc-statusline.sh"
payload "$install_dir/codex-status.sh" >"$branch/install/codex-status.sh"
printf '#!/usr/bin/env bash\necho pwned >>%q\n' "$canary" >"$branch/install/uninstall.sh"
for f in "$install_dir/commands"/*.md; do
  # The backticks are the Markdown shell-command syntax the payload uses.
  # shellcheck disable=SC2016
  { cat "$f"; printf '\n!`echo pwned >>%q`\n' "$canary"; } >"$branch/install/commands/$(basename "$f")"
done

# publish <package_dir> [tamper|offsite]: tar package_dir as the latest
# release. `tamper` changes the tarball after its integrity was recorded;
# `offsite` lists a tarball outside the registry.
publish() {
  local pkg="$1" mode="${2:-}" tgz="$registry/@weave-os/router/-/router-9.9.9.tgz" url integrity
  url="https://registry.npmjs.org/@weave-os/router/-/router-9.9.9.tgz"
  tar -czf "$tgz" -C "$pkg" package
  integrity="sha512-$(openssl dgst -sha512 -binary "$tgz" | openssl base64 -A)"
  case "$mode" in
    tamper) printf 'x' >>"$tgz" ;;
    offsite)
      cp "$tgz" "$branch/release.tgz"
      url="https://raw.githubusercontent.com/weave-os/router/main/release.tgz"
      ;;
  esac
  printf '{"name":"@weave-os/router","version":"9.9.9","dist":{"integrity":"%s","tarball":"%s"}}\n' \
    "$integrity" "$url" >"$registry/@weave-os/router/latest"
}

# A release whose files differ from the installed copies by one marker line.
release="$work/release"
mkdir -p "$release/package/commands"
{ cat "$install_dir/cc-statusline.sh"; echo '# release marker'; } >"$release/package/cc-statusline.sh"
{ cat "$install_dir/codex-status.sh"; echo '# release marker'; } >"$release/package/codex-status.sh"
printf '#!/usr/bin/env bash\necho release-uninstall "$@"\n' >"$release/package/uninstall.sh"
cp "$install_dir/commands"/*.md "$release/package/commands/"
printf '%s\n' '---' 'description: release fm.' '---' '' "/force-model \$ARGUMENTS" \
  >"$release/package/commands/fm.md"

run() { # run <case_dir> <cmd...> with the shimmed network and a clean env
  local c="$1"
  shift
  env -i HOME="$c/home" PATH="$work/bin:/usr/bin:/bin" XDG_CACHE_HOME="$c/cache" \
    WEAVE_TEST_REGISTRY="$registry" WEAVE_TEST_BRANCH="$branch" \
    WEAVE_TEST_CURL_LOG="$c/curl.log" WEAVE_TEST_REAL_CURL="$real_curl" "$@"
}

statusline_turn() { # statusline_turn <case_dir>
  echo '{"model":{"id":"claude-opus-4-7"}}' | run "$1" bash "$1/home/.weave/cc-statusline.sh" >/dev/null 2>&1
}

new_statusline_case() { # new_statusline_case <case_dir>
  mkdir -p "$1/home/.weave" "$1/home/.claude/commands" "$1/cache"
  : >"$1/curl.log"
  cp "$install_dir/cc-statusline.sh" "$1/home/.weave/cc-statusline.sh"
  chmod +x "$1/home/.weave/cc-statusline.sh"
}

# Waits until the background refresh has read the release (or would have).
settle() { # settle <case_dir> <url_fragment>
  wait_for 10 logged "$1/curl.log" "$2"
  sleep 1
}

echo "release_channel"

# ---------- statusline ----------

rm -f "$canary"
publish "$release"
c="$work/s1"; new_statusline_case "$c"
statusline_turn "$c"
if wait_for 10 same_file "$c/home/.weave/cc-statusline.sh" "$release/package/cc-statusline.sh"; then
  ok "statusline updates from the published release"
else
  no "statusline updates from the published release" "release copy installed" "$(tail -n 1 "$c/home/.weave/cc-statusline.sh")"
fi
statusline_turn "$c"
if [ -e "$canary" ]; then no "statusline never runs a branch push" "no canary" "payload ran"; else ok "statusline never runs a branch push"; fi
if grep -q "raw.githubusercontent.com" "$c/curl.log"; then no "statusline never reads the repository" "no branch fetch" "$(grep raw.githubusercontent.com "$c/curl.log" | head -n 1)"; else ok "statusline never reads the repository"; fi

for mode in tamper offsite; do
  publish "$release" "$mode"
  c="$work/s-$mode"; new_statusline_case "$c"
  statusline_turn "$c"
  settle "$c" "@weave-os/router/latest"
  if same_file "$c/home/.weave/cc-statusline.sh" "$install_dir/cc-statusline.sh"; then
    ok "statusline refuses an $mode release"
  else
    no "statusline refuses an $mode release" "installed copy unchanged" "replaced"
  fi
done

# ---------- slash-command wrappers ----------

publish "$release"
c="$work/w1"; new_statusline_case "$c"
cmd_dir="$c/home/.claude/commands"
cp "$install_dir/commands/fm.md" "$cmd_dir/fm.md"
baseline="$c/cache/weave-router/commands$(printf '%s' "$(cd "$cmd_dir" && pwd -P)" | tr -c 'A-Za-z0-9._-' '_')"
mkdir -p "$baseline"
cp "$install_dir/commands/fm.md" "$baseline/fm.md"
statusline_turn "$c"
if wait_for 10 grep -q 'release fm.' "$cmd_dir/fm.md"; then
  ok "wrappers update from the published release"
else
  no "wrappers update from the published release" "release fm.md" "$(head -n 3 "$cmd_dir/fm.md")"
fi
if grep -q pwned "$cmd_dir/fm.md"; then no "wrappers never take a branch push" "no payload" "payload installed"; else ok "wrappers never take a branch push"; fi

publish "$release" tamper
c="$work/w2"; new_statusline_case "$c"
cmd_dir="$c/home/.claude/commands"
cp "$install_dir/commands/fm.md" "$cmd_dir/fm.md"
baseline="$c/cache/weave-router/commands$(printf '%s' "$(cd "$cmd_dir" && pwd -P)" | tr -c 'A-Za-z0-9._-' '_')"
mkdir -p "$baseline"
cp "$install_dir/commands/fm.md" "$baseline/fm.md"
statusline_turn "$c"
settle "$c" "@weave-os/router/latest"
if same_file "$cmd_dir/fm.md" "$install_dir/commands/fm.md"; then
  ok "wrappers refuse a tampered release"
else
  no "wrappers refuse a tampered release" "unchanged" "$(head -n 3 "$cmd_dir/fm.md")"
fi

# ---------- Codex status helper ----------

codex_turn() { # codex_turn <case_dir>
  printf '%s\n' '{"session_id":"s","model":"gpt-5.6-terra","last_assistant_message":"hi"}' \
    | run "$1" WEAVE_CODEX_STATUS_TITLE_FILE="$1/title" bash "$1/home/.codex/weave-status.sh" >/dev/null 2>&1
}
new_codex_case() {
  mkdir -p "$1/home/.codex" "$1/cache"
  : >"$1/curl.log"
  cp "$install_dir/codex-status.sh" "$1/home/.codex/weave-status.sh"
  chmod 700 "$1/home/.codex/weave-status.sh"
}

rm -f "$canary"
publish "$release"
c="$work/c1"; new_codex_case "$c"
codex_turn "$c"
if wait_for 10 same_file "$c/home/.codex/weave-status.sh" "$release/package/codex-status.sh"; then
  ok "codex helper updates from the published release"
else
  no "codex helper updates from the published release" "release copy installed" "$(tail -n 1 "$c/home/.codex/weave-status.sh")"
fi
codex_turn "$c"
if [ -e "$canary" ]; then no "codex helper never runs a branch push" "no canary" "payload ran"; else ok "codex helper never runs a branch push"; fi

publish "$release" tamper
c="$work/c2"; new_codex_case "$c"
codex_turn "$c"
settle "$c" "@weave-os/router/latest"
if same_file "$c/home/.codex/weave-status.sh" "$install_dir/codex-status.sh"; then
  ok "codex helper refuses a tampered release"
else
  no "codex helper refuses a tampered release" "unchanged" "replaced"
fi

# ---------- curl-piped uninstall ----------

rm -f "$canary"
publish "$release"
c="$work/u1"; mkdir -p "$c/home" "$c/cache" "$c/piped"; : >"$c/curl.log"
cp "$install_dir/install.sh" "$c/piped/install.sh"
out="$(run "$c" bash "$c/piped/install.sh" --uninstall --yes 2>&1)"
case "$out" in
  *"release-uninstall --yes"*) ok "uninstall runs the published release's uninstaller" ;;
  *) no "uninstall runs the published release's uninstaller" "release-uninstall --yes" "$out" ;;
esac
if [ -e "$canary" ]; then no "uninstall never runs a branch push" "no canary" "payload ran"; else ok "uninstall never runs a branch push"; fi

publish "$release" tamper
c="$work/u2"; mkdir -p "$c/home" "$c/cache" "$c/piped"; : >"$c/curl.log"
cp "$install_dir/install.sh" "$c/piped/install.sh"
if out="$(run "$c" bash "$c/piped/install.sh" --uninstall 2>&1)"; then
  no "uninstall refuses a tampered release" "non-zero exit" "$out"
else
  case "$out" in
    *release-uninstall*) no "uninstall refuses a tampered release" "nothing run" "$out" ;;
    *) ok "uninstall refuses a tampered release" ;;
  esac
fi

# ---------- one fetcher ----------

fetcher() { awk '/^weave_release_fetch\(\) \{$/,/^\}$/' "$1"; }
reference="$(fetcher "$install_dir/cc-statusline.sh")"
if [ -z "$reference" ]; then
  no "updaters share one release fetcher" "weave_release_fetch in cc-statusline.sh" "missing"
elif [ "$(fetcher "$install_dir/codex-status.sh")" = "$reference" ] \
  && [ "$(fetcher "$install_dir/install.sh" | awk '/^weave_release_fetch\(\) \{$/ { n++ } n == 1')" = "$reference" ]; then
  ok "updaters share one release fetcher"
else
  no "updaters share one release fetcher" "identical copies" "drift between cc-statusline.sh, codex-status.sh and install.sh"
fi

echo
echo "  $pass passed, $fail failed"
[ "$fail" -eq 0 ]
