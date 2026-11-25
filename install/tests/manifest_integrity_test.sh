#!/usr/bin/env bash
#
# Offline integrity tests for every fetched-code path shipped from install/.
#
# Fetched-code surfaces (keep this list in step with the inventory comment in
# install.sh, "uninstall delegation"; every surface below must have cases here):
#   1. cc-statusline.sh weave_self_refresh: downloads cc-statusline.sh and
#      swaps it over the installed copy, which runs on the next turn.
#   2. cc-statusline.sh weave_sync_commands: downloads commands/*.md bodies and
#      overwrites installed slash-command wrappers.
#   3. codex-status.sh weave_self_refresh: downloads codex-status.sh and swaps
#      it over the installed Codex hook.
#   4. install.sh --uninstall without a sibling uninstall.sh: downloads
#      uninstall.sh and runs it with `exec bash -c`.
# uninstall.sh and the npm dispatcher (install/npm/bin.js) run only local
# bytes; a new fetched path must be added here alongside its verification.
#
# Each surface may admit downloaded bytes only after a MANIFEST signed with the
# embedded release key verifies and the body's SHA-256 matches its row. The
# cases replay the default victim path: the scripts run with no WEAVE_*_URL
# override, and a curl shim maps https://raw.githubusercontent.com/ (and a
# stand-in self-hoster host) onto file:// trees under a temp dir, so nothing
# here touches the network. Every key is generated per run in that temp dir and
# removed on exit; no key material is committed. The copies under test carry
# the per-run public key in place of the embedded placeholder.

set -uo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cc_src="$script_dir/../cc-statusline.sh"
codex_src="$script_dir/../codex-status.sh"
installer_src="$script_dir/../install.sh"
for f in "$cc_src" "$codex_src" "$installer_src"; do
  [ -f "$f" ] || { echo "cannot find $f" >&2; exit 1; }
done
for tool in openssl curl jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "FATAL: $tool is required" >&2; exit 1; }
done

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

probe="$work/probe.txt"
echo probe >"$probe"
if ! curl -fsS "file://$probe" >/dev/null 2>&1; then
  echo "FATAL: this curl has no file:// support; cannot run offline integrity tests" >&2
  exit 1
fi

pass=0
fail=0
ok() { echo "  ok   $1"; pass=$((pass + 1)); }
no() { echo "  FAIL $1"; echo "         expected: $2"; echo "         actual:   $3"; fail=$((fail + 1)); }
check() {
  if [ "$2" = "$3" ]; then ok "$1"; else no "$1" "$3" "$2"; fi
}
check_contains() {
  case "$2" in *"$3"*) ok "$1" ;; *) no "$1" "contains '$3'" "$2" ;; esac
}
check_not_contains() {
  case "$2" in *"$3"*) no "$1" "no '$3'" "$2" ;; *) ok "$1" ;; esac
}
wait_for() {
  local deadline=$(( $(date +%s) + $1 )); shift
  while ! "$@"; do
    [ "$(date +%s)" -ge "$deadline" ] && return 1
    sleep 0.1
  done
}
sha() { # sha <file>: lowercase hex SHA-256
  if command -v sha256sum >/dev/null 2>&1; then sha256sum <"$1" | awk '{print $1}'
  else shasum -a 256 <"$1" | awk '{print $1}'; fi
}
file_absent() { [ ! -e "$1" ]; }
log_has() { grep -q -- "$2" "$1" 2>/dev/null; }
no_glob_match() {
  compgen -G "$1" >/dev/null && return 1 || return 0
}

# ---------- keys and signing ----------

keys="$work/keys"
mkdir -p "$keys"
for k in release other; do
  if ! { openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$keys/$k.key" 2>/dev/null \
      && openssl pkey -in "$keys/$k.key" -pubout -out "$keys/$k.pub" 2>/dev/null; }; then
    echo "FATAL: could not generate a test RSA key" >&2; exit 1
  fi
done

# with_key <src> <dest> <pub.pem>: copy <src> with every embedded PEM public
# key block replaced by <pub.pem>.
with_key() {
  awk -v pub="$3" '
    $0 == "-----BEGIN PUBLIC KEY-----" { while ((getline line < pub) > 0) print line; close(pub); skip = 1; next }
    skip && $0 == "-----END PUBLIC KEY-----" { skip = 0; next }
    !skip { print }
  ' "$1" >"$2"
  chmod 755 "$2"
}

# sign <manifest> <key> <out.sig.b64>
sign() {
  openssl dgst -sha256 -sign "$2" -out "$3.bin" "$1" \
    && openssl base64 -A -in "$3.bin" -out "$3" \
    && rm -f "$3.bin"
}

# manifest_row <path-in-manifest> <file>
manifest_row() { printf '%s  %s\n' "$(sha "$2")" "$1"; }

cc_keyed="$work/cc-statusline.keyed.sh"
codex_keyed="$work/codex-status.keyed.sh"
installer_keyed="$work/install.keyed.sh"
with_key "$cc_src" "$cc_keyed" "$keys/release.pub"
with_key "$codex_src" "$codex_keyed" "$keys/release.pub"
with_key "$installer_src" "$installer_keyed" "$keys/release.pub"

current_tag="$(sed -n 's/^WEAVE_RELEASE_TAG="\(router-v[0-9.]*\)"$/\1/p' "$cc_src" | head -n 1)"
[[ "$current_tag" =~ ^router-v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]] \
  || { echo "FATAL: cannot read WEAVE_RELEASE_TAG from cc-statusline.sh" >&2; exit 1; }
next_tag="router-v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.$(( BASH_REMATCH[3] + 1 ))"
(( BASH_REMATCH[3] > 0 )) || { echo "FATAL: the test needs a release tag with a nonzero patch number" >&2; exit 1; }
prev_tag="router-v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.$(( BASH_REMATCH[3] - 1 ))"

# retag <file> <tag>: rewrite the WEAVE_RELEASE_TAG assignment in place.
retag() { sed -i.bak "s/^WEAVE_RELEASE_TAG=\".*\"$/WEAVE_RELEASE_TAG=\"$2\"/" "$1" && rm -f "$1.bak"; }

# with_canary <src> <dest> <canary_name>: a shape-valid copy that records each
# run in $HOME/<canary_name> before doing anything else.
with_canary() {
  awk -v c="$3" '{ print } !done && /^export PATH=/ { print "printf \"ran\\n\" >>\"$HOME/" c "\""; done = 1 }' "$1" >"$2"
  chmod 755 "$2"
}

# ---------- offline publisher ----------

real_curl="$(command -v curl)"
shim_bin="$work/shim-bin"
mkdir -p "$shim_bin"
cat >"$shim_bin/curl" <<'SHIM'
#!/usr/bin/env bash
# Map the publisher hosts onto file:// trees and refuse every other network URL.
args=()
for a in "$@"; do
  case "$a" in
    https://raw.githubusercontent.com/*) a="file://$MIT_PUB_ROOT/raw/${a#https://raw.githubusercontent.com/}" ;;
    https://selfhost.example.test/*) a="file://$MIT_PUB_ROOT/selfhost/${a#https://selfhost.example.test/}" ;;
    http://*|https://*) echo "blocked $a" >>"$MIT_CURL_LOG"; exit 7 ;;
  esac
  case "$a" in file://*) echo "$a" >>"$MIT_CURL_LOG" ;; esac
  args+=("$a")
done
exec "$MIT_REAL_CURL" "${args[@]}"
SHIM
chmod 755 "$shim_bin/curl"

# A PATH that resolves every tool except openssl, for the fail-closed cases.
no_openssl_bin="$work/no-openssl-bin"
mkdir -p "$no_openssl_bin"
IFS=: read -r -a path_dirs <<<"$PATH"
for d in "${path_dirs[@]}"; do
  [ -d "$d" ] || continue
  for exe in "$d"/*; do
    name="${exe##*/}"
    [ "$name" = "openssl" ] && continue
    [ -x "$exe" ] && [ ! -d "$exe" ] && [ ! -e "$no_openssl_bin/$name" ] && ln -s "$exe" "$no_openssl_bin/$name"
  done
done
[ ! -e "$no_openssl_bin/openssl" ] || { echo "FATAL: could not build an openssl-free PATH" >&2; exit 1; }

# A lab is one victim machine (scratch $HOME, cache) plus one publisher tree.
# Paths under the publisher mirror the real URLs.
new_lab() { # new_lab <name>: sets lab, home, pub, log
  lab="$work/$1"; home="$lab/home"; pub="$lab/pub"; log="$lab/curl.log"
  mkdir -p "$home" "$lab/cache" "$pub/raw/weave-os/router/main/install" "$pub/selfhost/install"
  : >"$log"
}
rel_dir() { printf '%s\n' "$pub/raw/weave-os/router/$1/install"; }

# victim_env <path_dir>: the clean environment every victim process runs in.
# Only HOME, cache, PATH and the shim's own variables; no WEAVE_* override.
victim_env() {
  printf '%s\n' env -i "HOME=$home" "XDG_CACHE_HOME=$lab/cache" "TMPDIR=$lab" \
    "PATH=$shim_bin:$1" "MIT_PUB_ROOT=$pub" "MIT_CURL_LOG=$log" "MIT_REAL_CURL=$real_curl"
}

cc_payload() { printf '{"model":{"id":"%s"},"transcript_path":"%s/none.jsonl"}' "${cc_model:-claude-opus-5}" "$lab"; }
codex_payload() { printf '%s' '{"session_id":"s-integrity","model":"gpt-5.6-terra","last_assistant_message":"done"}'; }

# run_tool <tool> <installed> [VAR=value...]: one victim invocation; stderr is
# captured in $lab/stderr. The extra assignments are the case's own overrides.
run_tool() {
  local tool="$1" installed="$2" path_dir="$PATH"; shift 2
  local extra=() a
  for a in "$@"; do
    case "$a" in MIT_PATH=*) path_dir="${a#MIT_PATH=}" ;; *) extra+=("$a") ;; esac
  done
  local env_cmd=()
  mapfile -t env_cmd < <(victim_env "$path_dir")
  if [ "$tool" = "cc" ]; then
    cc_payload | "${env_cmd[@]}" "${extra[@]+"${extra[@]}"}" bash "$installed" >/dev/null 2>"$lab/stderr"
  else
    codex_payload | "${env_cmd[@]}" "WEAVE_CODEX_STATUS_TITLE_FILE=$lab/title" "${extra[@]+"${extra[@]}"}" \
      bash "$installed" >/dev/null 2>"$lab/stderr"
  fi
}

# settle <installed> <url_fragment>: bounded wait for the detached refresh to
# fetch <url_fragment> and then clean up its temp files.
settle() {
  wait_for 20 log_has "$log" "$2" || return 1
  wait_for 20 no_glob_match "$1.tmp.*" || return 1
  sleep 0.3
}

tool_file() { [ "$1" = "cc" ] && echo cc-statusline.sh || echo codex-status.sh; }
tool_keyed() { [ "$1" = "cc" ] && echo "$cc_keyed" || echo "$codex_keyed"; }
tool_url_var() { [ "$1" = "cc" ] && echo WEAVE_STATUSLINE_URL || echo WEAVE_CODEX_STATUS_URL; }
tool_installed() { [ "$1" = "cc" ] && echo "$home/.weave/cc-statusline.sh" || echo "$home/.codex/weave-status.sh"; }

# self_refresh_lab <tool> <name>: install the current release, have main
# advertise $next_tag, and stage a genuine signed release at that tag. Sets
# installed, served (the tag's body), good (the genuine body), release_dir (tag dir).
self_refresh_lab() {
  local tool="$1" file
  file="$(tool_file "$tool")"
  new_lab "$2"
  installed="$(tool_installed "$tool")"
  mkdir -p "$(dirname "$installed")"
  cp "$(tool_keyed "$tool")" "$installed"; chmod 755 "$installed"
  release_dir="$(rel_dir "$next_tag")"; mkdir -p "$release_dir"
  good="$lab/genuine.sh"
  cp "$(tool_keyed "$tool")" "$good"; retag "$good" "$next_tag"
  printf '# genuine %s release body\n' "$next_tag" >>"$good"
  cp "$good" "$pub/raw/weave-os/router/main/install/$file"
  served="$release_dir/$file"
  cp "$good" "$served"
  manifest_row "install/$file" "$good" >"$release_dir/MANIFEST"
  sign "$release_dir/MANIFEST" "$keys/release.key" "$release_dir/MANIFEST.sig.b64"
}

# tamper_served <tool>: serve a shape-valid body with a canary that the signed
# MANIFEST does not list.
tamper_served() {
  with_canary "$good" "$served" canary.txt
}

expect_refused() { # the installed copy still hashes to <before_sha>
  check "$1: installed copy unchanged" "$(sha "$installed")" "$2"
}

# ---------- verification helpers ----------

echo "release manifest helpers"

extract_helpers() { # extract_helpers <script> <dest>
  awk '/^WEAVE_VERIFY_UNAVAILABLE=2$/ { on = 1 } on && /^# ---------- unsigned self-hoster opt-in ----------$/ { exit } on' "$1" >"$2"
}
extract_helpers "$cc_src" "$work/helpers.cc"
extract_helpers "$codex_src" "$work/helpers.codex"
extract_helpers "$installer_src" "$work/helpers.installer"
[ -s "$work/helpers.cc" ] || { echo "FATAL: cannot locate the verification helpers in cc-statusline.sh" >&2; exit 1; }
check "codex-status.sh carries the same verification helpers" "$(sha "$work/helpers.codex")" "$(sha "$work/helpers.cc")"
check "install.sh carries the same verification helpers" "$(sha "$work/helpers.installer")" "$(sha "$work/helpers.cc")"

unit_dir="$work/unit"; mkdir -p "$unit_dir"
printf 'payload\n' >"$unit_dir/body"
manifest_row "install/cc-statusline.sh" "$unit_dir/body" >"$unit_dir/MANIFEST"
sign "$unit_dir/MANIFEST" "$keys/release.key" "$unit_dir/MANIFEST.sig.b64"
sign "$unit_dir/MANIFEST" "$keys/other.key" "$unit_dir/other.sig.b64"
unit() { # unit <PATH> <bash snippet>: run a snippet against the helpers, print its status
  env -i "PATH=$1" "TMPDIR=$unit_dir" bash -c "source '$work/helpers.cc'; $2" >/dev/null 2>&1
  echo "$?"
}
check "a manifest signed with the embedded key verifies" \
  "$(unit "$PATH" "weave_verify_manifest '$unit_dir/MANIFEST' '$unit_dir/MANIFEST.sig.b64' \"\$(cat '$keys/release.pub')\"")" 0
check "a manifest signed with another key is rejected" \
  "$(unit "$PATH" "weave_verify_manifest '$unit_dir/MANIFEST' '$unit_dir/other.sig.b64' \"\$(cat '$keys/release.pub')\"")" 1
check "a missing signature is rejected" \
  "$(unit "$PATH" "weave_verify_manifest '$unit_dir/MANIFEST' '$unit_dir/absent.sig.b64' \"\$(cat '$keys/release.pub')\"")" 1
check "a matching body verifies against its row" \
  "$(unit "$PATH" "weave_verify_file '$unit_dir/MANIFEST' install/cc-statusline.sh '$unit_dir/body'")" 0
printf 'payload!\n' >"$unit_dir/other-body"
check "a body that does not match its row is rejected" \
  "$(unit "$PATH" "weave_verify_file '$unit_dir/MANIFEST' install/cc-statusline.sh '$unit_dir/other-body'")" 1
check "a body is never matched against another file's row" \
  "$(unit "$PATH" "weave_verify_file '$unit_dir/MANIFEST' install/codex-status.sh '$unit_dir/body'")" 1
check "missing openssl reports verification unavailable" \
  "$(unit "$no_openssl_bin" "weave_verify_manifest '$unit_dir/MANIFEST' '$unit_dir/MANIFEST.sig.b64' \"\$(cat '$keys/release.pub')\"")" 2

# ---------- self-refresh (surfaces 1 and 3) ----------

for tool in cc codex; do
  file="$(tool_file "$tool")"
  url_var="$(tool_url_var "$tool")"
  echo "$file self-refresh"

  # Happy path: the default publisher advertises a newer tag whose signed
  # MANIFEST lists the served body.
  self_refresh_lab "$tool" "$tool-happy"
  run_tool "$tool" "$installed"
  if wait_for 20 cmp -s "$installed" "$served"; then
    ok "$tool: a signed release replaces the installed copy"
  else
    no "$tool: a signed release replaces the installed copy" "installed == served" "still the old copy"
  fi
  check "$tool: the replacement is byte-identical to the signed body" "$(sha "$installed")" "$(sha "$served")"
  if bash -n "$installed" 2>/dev/null; then ok "$tool: the replacement still parses"; else no "$tool: the replacement still parses" "valid bash" "syntax error"; fi
  check_contains "$tool: the release was fetched from its immutable tag" "$(cat "$log")" "/raw/weave-os/router/$next_tag/install/$file"

  # Tampered body: shape-valid, served at the tag, absent from the MANIFEST.
  self_refresh_lab "$tool" "$tool-tampered"
  tamper_served
  before="$(sha "$installed")"
  run_tool "$tool" "$installed"
  settle "$installed" "$next_tag/install/$file"
  expect_refused "$tool: a body that does not match the signed MANIFEST is refused" "$before"
  run_tool "$tool" "$installed"
  sleep 0.3
  if file_absent "$home/canary.txt"; then ok "$tool: the refused body never runs"; else no "$tool: the refused body never runs" "no canary" "$(cat "$home/canary.txt")"; fi

  # Missing signature.
  self_refresh_lab "$tool" "$tool-nosig"
  rm -f "$release_dir/MANIFEST.sig.b64"
  before="$(sha "$installed")"
  run_tool "$tool" "$installed"
  settle "$installed" "$next_tag/install/MANIFEST.sig.b64"
  expect_refused "$tool: a release without MANIFEST.sig.b64 is refused" "$before"

  # An attacker's own key signing an attacker's MANIFEST that lists the body.
  self_refresh_lab "$tool" "$tool-otherkey"
  tamper_served
  manifest_row "install/$file" "$served" >"$release_dir/MANIFEST"
  sign "$release_dir/MANIFEST" "$keys/other.key" "$release_dir/MANIFEST.sig.b64"
  before="$(sha "$installed")"
  run_tool "$tool" "$installed"
  settle "$installed" "$next_tag/install/$file"
  expect_refused "$tool: a MANIFEST signed with a different key is refused" "$before"

  # The genuine signature, served beside a different (edited) MANIFEST.
  self_refresh_lab "$tool" "$tool-swapped-manifest"
  tamper_served
  manifest_row "install/$file" "$served" >"$release_dir/MANIFEST"
  before="$(sha "$installed")"
  run_tool "$tool" "$installed"
  settle "$installed" "$next_tag/install/$file"
  expect_refused "$tool: a signature over a different MANIFEST is refused" "$before"

  # The signed MANIFEST lists the served body only under another file's name,
  # and this file's row holds a different digest.
  self_refresh_lab "$tool" "$tool-wrong-row"
  tamper_served
  { manifest_row "install/$file" "$good"; manifest_row "install/uninstall.sh" "$served"; } >"$release_dir/MANIFEST"
  sign "$release_dir/MANIFEST" "$keys/release.key" "$release_dir/MANIFEST.sig.b64"
  before="$(sha "$installed")"
  run_tool "$tool" "$installed"
  settle "$installed" "$next_tag/install/$file"
  expect_refused "$tool: a body listed only under another filename is refused" "$before"

  # The shape checks remain a second line of defence behind the signature.
  self_refresh_lab "$tool" "$tool-shape"
  printf '#!/bin/sh\necho short\n' >"$served"
  manifest_row "install/$file" "$served" >"$release_dir/MANIFEST"
  sign "$release_dir/MANIFEST" "$keys/release.key" "$release_dir/MANIFEST.sig.b64"
  before="$(sha "$installed")"
  run_tool "$tool" "$installed"
  settle "$installed" "$next_tag/install/$file"
  expect_refused "$tool: a signed but malformed body is still refused" "$before"

  # The signature names no release, so an older signed release replayed under a
  # newer tag name is refused by the tag the body itself carries.
  self_refresh_lab "$tool" "$tool-replayed"
  cp "$(tool_keyed "$tool")" "$served"
  printf '# older signed release body\n' >>"$served"
  manifest_row "install/$file" "$served" >"$release_dir/MANIFEST"
  sign "$release_dir/MANIFEST" "$keys/release.key" "$release_dir/MANIFEST.sig.b64"
  before="$(sha "$installed")"
  run_tool "$tool" "$installed"
  settle "$installed" "$next_tag/install/$file"
  expect_refused "$tool: an older signed body served under a newer tag is refused" "$before"

  # No downgrade or re-fetch: main advertising this copy's own tag fetches
  # nothing from any tag.
  self_refresh_lab "$tool" "$tool-current"
  cp "$installed" "$pub/raw/weave-os/router/main/install/$file"
  tamper_served
  before="$(sha "$installed")"
  run_tool "$tool" "$installed"
  settle "$installed" "main/install/$file"
  expect_refused "$tool: main advertising the installed tag swaps nothing" "$before"
  check_not_contains "$tool: main advertising the installed tag fetches no release" "$(cat "$log")" "/$next_tag/"

  # The opt-in is not honored for the default publisher.
  self_refresh_lab "$tool" "$tool-optin-default"
  tamper_served
  rm -f "$release_dir/MANIFEST" "$release_dir/MANIFEST.sig.b64"
  before="$(sha "$installed")"
  run_tool "$tool" "$installed" WEAVE_STATUSLINE_TRUST_UNSIGNED=1 \
    "$url_var=https://raw.githubusercontent.com/weave-os/router/$next_tag/install/$file"
  settle "$installed" "$next_tag/install/MANIFEST"
  expect_refused "$tool: WEAVE_STATUSLINE_TRUST_UNSIGNED=1 is refused for the default publisher" "$before"
  check_not_contains "$tool: no opt-in warning for the default publisher" "$(cat "$lab/stderr")" "skipping signature verification"
  run_tool "$tool" "$installed"
  sleep 0.3
  if file_absent "$home/canary.txt"; then ok "$tool: the unsigned body never runs"; else no "$tool: the unsigned body never runs" "no canary" "$(cat "$home/canary.txt")"; fi

  # A non-default host still verifies unless the user opts in.
  self_refresh_lab "$tool" "$tool-selfhost-signed-required"
  cp "$good" "$pub/selfhost/install/$file"
  before="$(sha "$installed")"
  run_tool "$tool" "$installed" "$url_var=https://selfhost.example.test/install/$file"
  settle "$installed" "selfhost/install/MANIFEST"
  expect_refused "$tool: an overridden host without a signed MANIFEST is refused" "$before"

  # A signed body from an overridden host must still be a newer release.
  self_refresh_lab "$tool" "$tool-selfhost-signed"
  cp "$good" "$pub/selfhost/install/$file"
  cp "$release_dir/MANIFEST" "$release_dir/MANIFEST.sig.b64" "$pub/selfhost/install/"
  run_tool "$tool" "$installed" "$url_var=https://selfhost.example.test/install/$file"
  if wait_for 20 cmp -s "$installed" "$good"; then
    ok "$tool: a signed newer release from an overridden host is swapped in"
  else
    no "$tool: a signed newer release from an overridden host is swapped in" "installed == signed body" "not swapped"
  fi
  self_refresh_lab "$tool" "$tool-selfhost-replayed"
  cp "$(tool_keyed "$tool")" "$pub/selfhost/install/$file"
  printf '# older signed release body\n' >>"$pub/selfhost/install/$file"
  manifest_row "install/$file" "$pub/selfhost/install/$file" >"$pub/selfhost/install/MANIFEST"
  sign "$pub/selfhost/install/MANIFEST" "$keys/release.key" "$pub/selfhost/install/MANIFEST.sig.b64"
  before="$(sha "$installed")"
  run_tool "$tool" "$installed" "$url_var=https://selfhost.example.test/install/$file"
  settle "$installed" "selfhost/install/$file"
  expect_refused "$tool: an older signed body from an overridden host is refused" "$before"

  # The opt-in is honored for an overridden non-default host, with a warning.
  self_refresh_lab "$tool" "$tool-optin-selfhost"
  cp "$good" "$pub/selfhost/install/$file"
  run_tool "$tool" "$installed" WEAVE_STATUSLINE_TRUST_UNSIGNED=1 \
    "$url_var=https://selfhost.example.test/install/$file"
  if wait_for 20 cmp -s "$installed" "$pub/selfhost/install/$file"; then
    ok "$tool: WEAVE_STATUSLINE_TRUST_UNSIGNED=1 swaps an unsigned self-hosted copy"
  else
    no "$tool: WEAVE_STATUSLINE_TRUST_UNSIGNED=1 swaps an unsigned self-hosted copy" "installed == self-hosted copy" "not swapped"
  fi
  check_contains "$tool: the opt-in prints its warning to stderr" "$(cat "$lab/stderr")" \
    "WEAVE_STATUSLINE_TRUST_UNSIGNED=1: skipping signature verification."
  check_not_contains "$tool: the opt-in fetched no MANIFEST" "$(cat "$log")" "MANIFEST"

  # Without openssl the refresh fails closed, even for a genuine release.
  self_refresh_lab "$tool" "$tool-no-openssl"
  before="$(sha "$installed")"
  run_tool "$tool" "$installed" "MIT_PATH=$no_openssl_bin"
  settle "$installed" "$next_tag/install/$file"
  expect_refused "$tool: without openssl a genuine release is not swapped in" "$before"
done

# A price-miss refresh retries a failed download next turn, but a body that
# downloaded and was refused keeps its stamp: the same bytes would come back.
self_refresh_lab cc cc-price-miss-refused
tamper_served
cc_model="unpriced-model-x" run_tool cc "$installed"
settle "$installed" "$next_tag/install/cc-statusline.sh"
if compgen -G "$lab/cache/weave-router/checked-at*.miss.unpriced-model-x" >/dev/null; then
  ok "a refused price-miss refresh keeps its stamp"
else
  no "a refused price-miss refresh keeps its stamp" "stamp kept" "stamp dropped"
fi

# ---------- slash-command refresh (surface 2) ----------

echo "cc-statusline.sh wrapper refresh"

# commands_lab <name>: a user-scope install of the current release with fm and
# rf wrappers, a pre-manifest baseline (what install.sh or an older statusline
# wrote), and a release dir for the current tag. Main advertises the current
# tag so the self-refresh is a no-op. Sets installed, cmd_dir, baseline, release_dir.
commands_lab() {
  new_lab "$1"
  installed="$home/.weave/cc-statusline.sh"
  cmd_dir="$home/.claude/commands"
  mkdir -p "$home/.weave" "$cmd_dir"
  cp "$cc_keyed" "$installed"; chmod 755 "$installed"
  cp "$cc_keyed" "$pub/raw/weave-os/router/main/install/cc-statusline.sh"
  release_dir="$(rel_dir "$current_tag")"; mkdir -p "$release_dir/commands"
  cp "$cc_keyed" "$release_dir/cc-statusline.sh"
  baseline="$lab/cache/weave-router/commands$(printf '%s' "$(cd "$cmd_dir" && pwd -P)" | tr -c 'A-Za-z0-9._-' '_')"
  mkdir -p "$baseline"
  local name
  for name in fm rf; do
    cp "$script_dir/../commands/$name.md" "$cmd_dir/$name.md"
    cp "$script_dir/../commands/$name.md" "$baseline/$name.md"
    cp "$script_dir/../commands/$name.md" "$release_dir/commands/$name.md"
  done
}
sign_commands() { # sign_commands [key]: sign a MANIFEST of the release dir's statusline and wrappers
  local f
  manifest_row "install/cc-statusline.sh" "$release_dir/cc-statusline.sh" >"$release_dir/MANIFEST"
  for f in "$release_dir/commands"/*.md; do manifest_row "install/commands/${f##*/}" "$f" >>"$release_dir/MANIFEST"; done
  sign "$release_dir/MANIFEST" "$keys/${1:-release}.key" "$release_dir/MANIFEST.sig.b64"
}
upstream_wrapper() {
  printf '%s\n' '---' "description: $2" '---' '' "/$1 \$ARGUMENTS" >"$release_dir/commands/$1.md"
}
sync_settle() { # sync_settle <fragment>: bounded wait for the wrapper cycle to finish
  wait_for 20 log_has "$log" "$1" || return 1
  wait_for 20 no_glob_match "$baseline/*.tmp.*" || return 1
  wait_for 20 no_glob_match "$baseline/.MANIFEST.tmp.*" || return 1
  sleep 0.3
}

# One-shot re-seed: with no sentinel the pre-manifest baseline is untrusted, so
# the first verified cycle installs the verified bodies and writes the sentinel.
commands_lab cmd-seed
upstream_wrapper fm "release fm."
sign_commands
printf '%s\n' '<!-- weave-router managed command: rf -->' 'EDITED BEFORE UPGRADE' >"$cmd_dir/rf.md"
if file_absent "$baseline/.manifest-seeded"; then ok "sentinel absent before the first verified cycle"; else no "sentinel absent before the first verified cycle" "absent" "present"; fi
run_tool cc "$installed"
if wait_for 20 test -f "$baseline/.manifest-seeded"; then
  ok "the first verified cycle writes the .manifest-seeded sentinel"
else
  no "the first verified cycle writes the .manifest-seeded sentinel" "present" "absent"
fi
sync_settle "commands/rf.md"
check "re-seed installs the verified wrapper body" "$(cat "$cmd_dir/fm.md")" "$(cat "$release_dir/commands/fm.md")"
check "re-seed records the verified body as the baseline" "$(sha "$baseline/fm.md")" "$(sha "$release_dir/commands/fm.md")"
check "re-seed resets a wrapper edited before the upgrade once" "$(cat "$cmd_dir/rf.md")" "$(cat "$release_dir/commands/rf.md")"

# After the re-seed the baseline comparison preserves local edits again.
printf '%s\n' 'MY OWN FM' >"$cmd_dir/fm.md"
upstream_wrapper fm "release fm, take two."
upstream_wrapper rf "release rf, take two."
sign_commands
rm -f "$lab/cache/weave-router/"checked-at*.commands
: >"$log"
run_tool cc "$installed"
if wait_for 20 grep -q 'take two' "$cmd_dir/rf.md"; then
  ok "a post-seed cycle still refreshes an unedited wrapper"
else
  no "a post-seed cycle still refreshes an unedited wrapper" "refreshed rf" "$(cat "$cmd_dir/rf.md")"
fi
sync_settle "commands/rf.md"
check "a post-seed local edit is preserved across refresh" "$(cat "$cmd_dir/fm.md")" "MY OWN FM"

# A wrapper body that does not match the signed MANIFEST is skipped and its
# baseline is left byte-for-byte as it was.
commands_lab cmd-tampered
: >"$baseline/.manifest-seeded"
upstream_wrapper rf "release rf."
sign_commands
upstream_wrapper fm "tampered fm."
fm_before="$(cat "$cmd_dir/fm.md")"; fm_baseline_before="$(sha "$baseline/fm.md")"
run_tool cc "$installed"
wait_for 20 grep -q 'release rf.' "$cmd_dir/rf.md" || true
sync_settle "commands/rf.md"
check "the same cycle refreshes a wrapper that matches the MANIFEST" "$(grep -c 'release rf.' "$cmd_dir/rf.md")" 1
check "a tampered wrapper body is not installed" "$(cat "$cmd_dir/fm.md")" "$fm_before"
check "a tampered wrapper body leaves the baseline untouched" "$(sha "$baseline/fm.md")" "$fm_baseline_before"

# A MANIFEST signed with another key touches no wrapper, baseline or sentinel.
commands_lab cmd-otherkey
upstream_wrapper fm "other-key fm."
sign_commands other
fm_before="$(cat "$cmd_dir/fm.md")"; fm_baseline_before="$(sha "$baseline/fm.md")"
run_tool cc "$installed"
wait_for 20 log_has "$log" "$current_tag/install/MANIFEST.sig.b64"
wait_for 20 no_glob_match "$baseline/.MANIFEST.tmp.*"
sleep 0.5
check "a wrapper MANIFEST signed with another key installs nothing" "$(cat "$cmd_dir/fm.md")" "$fm_before"
check "a wrapper MANIFEST signed with another key keeps the baseline" "$(sha "$baseline/fm.md")" "$fm_baseline_before"
if file_absent "$baseline/.manifest-seeded"; then ok "a rejected MANIFEST writes no sentinel"; else no "a rejected MANIFEST writes no sentinel" "absent" "present"; fi
check_not_contains "a rejected MANIFEST fetches no wrapper body" "$(cat "$log")" "commands/fm.md"

# The opt-in is not honored for a wrapper source on the default publisher.
commands_lab cmd-optin-default
: >"$baseline/.manifest-seeded"
upstream_wrapper fm "unsigned fm."
fm_before="$(cat "$cmd_dir/fm.md")"
run_tool cc "$installed" WEAVE_STATUSLINE_TRUST_UNSIGNED=1 \
  "WEAVE_COMMANDS_URL_BASE=https://raw.githubusercontent.com/weave-os/router/$current_tag/install/commands"
wait_for 20 log_has "$log" "$current_tag/install/MANIFEST"
wait_for 20 no_glob_match "$baseline/.MANIFEST.tmp.*"
sleep 0.5
check "WEAVE_STATUSLINE_TRUST_UNSIGNED=1 is refused for default-publisher wrappers" "$(cat "$cmd_dir/fm.md")" "$fm_before"

# Without openssl the wrapper cycle fails closed.
commands_lab cmd-no-openssl
: >"$baseline/.manifest-seeded"
upstream_wrapper fm "release fm."
sign_commands
fm_before="$(cat "$cmd_dir/fm.md")"
run_tool cc "$installed" "MIT_PATH=$no_openssl_bin"
wait_for 20 log_has "$log" "$current_tag/install/MANIFEST.sig.b64"
wait_for 20 no_glob_match "$baseline/.MANIFEST.tmp.*"
sleep 0.5
check "without openssl no wrapper is refreshed" "$(cat "$cmd_dir/fm.md")" "$fm_before"

# The re-seed never replaces a command Router does not own: no in-body marker,
# no ownership sidecar and a body that differs from the old baseline.
commands_lab cmd-seed-user-owned
upstream_wrapper fm "release fm."
upstream_wrapper rf "release rf."
sign_commands
printf '%s\n' 'MY OWN FM COMMAND' >"$cmd_dir/fm.md"
run_tool cc "$installed"
wait_for 20 test -f "$baseline/.manifest-seeded" || true
sync_settle "commands/rf.md"
check "re-seed refreshes a wrapper that matches the old baseline" "$(cat "$cmd_dir/rf.md")" "$(cat "$release_dir/commands/rf.md")"
check "re-seed leaves a user-owned command untouched" "$(cat "$cmd_dir/fm.md")" "MY OWN FM COMMAND"
if file_absent "$cmd_dir/fm.md.weave-router"; then ok "re-seed claims no ownership of a user-owned command"; else no "re-seed claims no ownership of a user-owned command" "no sidecar" "sidecar written"; fi
check "re-seed still records the verified baseline for a user-owned name" "$(sha "$baseline/fm.md")" "$(sha "$release_dir/commands/fm.md")"

# A sidecar left behind after the user replaced the wrapper records the old
# body, so it is no proof of ownership.
commands_lab cmd-seed-stale-sidecar
upstream_wrapper fm "release fm."
sign_commands
printf 'weave-router managed command: fm\n%s\n' "$(cat "$cmd_dir/fm.md")" >"$cmd_dir/fm.md.weave-router"
printf '%s\n' 'MY OWN FM COMMAND' >"$cmd_dir/fm.md"
run_tool cc "$installed"
wait_for 20 test -f "$baseline/.manifest-seeded" || true
sync_settle "commands/rf.md"
check "re-seed leaves a user command with a stale sidecar untouched" "$(cat "$cmd_dir/fm.md")" "MY OWN FM COMMAND"

# A host-root URL base has no parent to strip, so MANIFEST must come from the root itself.
commands_lab cmd-host-root
: >"$baseline/.manifest-seeded"
upstream_wrapper fm "host-root fm."
sign_commands
cp "$release_dir/commands/fm.md" "$release_dir/cc-statusline.sh" "$release_dir/MANIFEST" "$release_dir/MANIFEST.sig.b64" "$pub/selfhost/"
run_tool cc "$installed" "WEAVE_COMMANDS_URL_BASE=https://selfhost.example.test/"
if wait_for 20 grep -q 'host-root fm.' "$cmd_dir/fm.md"; then
  ok "a host-root wrapper source verifies MANIFEST at the root"
else
  no "a host-root wrapper source verifies MANIFEST at the root" "refreshed fm" "$(cat "$cmd_dir/fm.md")"
fi
sync_settle "selfhost/rf.md"
check_contains "a host-root wrapper source fetches the root MANIFEST" "$(cat "$log")" "selfhost/MANIFEST"

# An older signed release served from a mutable wrapper source still verifies,
# so the statusline its MANIFEST lists must carry this copy's release or newer.
older_release_lab() { # signed selfhost/install release whose cc-statusline.sh advertises <statusline tag>; the tag is what the refresh checks
  commands_lab "$1"
  : >"$baseline/.manifest-seeded"
  release_dir="$pub/selfhost/install"; mkdir -p "$release_dir/commands"
  cp "$cc_keyed" "$release_dir/cc-statusline.sh"; retag "$release_dir/cc-statusline.sh" "$2"
  cp "$script_dir/../commands/rf.md" "$release_dir/commands/rf.md"
  upstream_wrapper fm "release $2 fm."
  sign_commands
}
older_release_lab cmd-older-release "$prev_tag"
fm_before="$(cat "$cmd_dir/fm.md")"
run_tool cc "$installed" "WEAVE_COMMANDS_URL_BASE=https://selfhost.example.test/install/commands"
wait_for 20 log_has "$log" "selfhost/install/cc-statusline.sh"
wait_for 20 no_glob_match "$baseline/.MANIFEST.tmp.*"
sleep 0.5
check "an older signed release does not roll a wrapper back" "$(cat "$cmd_dir/fm.md")" "$fm_before"
check_not_contains "an older signed release fetches no wrapper body" "$(cat "$log")" "commands/fm.md"

older_release_lab cmd-newer-release "$next_tag"
run_tool cc "$installed" "WEAVE_COMMANDS_URL_BASE=https://selfhost.example.test/install/commands"
if wait_for 20 grep -q "release $next_tag fm." "$cmd_dir/fm.md"; then
  ok "a newer signed release from an overridden wrapper source is installed"
else
  no "a newer signed release from an overridden wrapper source is installed" "refreshed fm" "$(cat "$cmd_dir/fm.md")"
fi
sync_settle "install/commands/rf.md"

# The newer release is now the floor: replaying this copy's own signed release
# from the same source must not roll the wrapper back to its older body.
newer_fm="$(cat "$cmd_dir/fm.md")"
retag "$release_dir/cc-statusline.sh" "$current_tag"
upstream_wrapper fm "release $current_tag fm."
sign_commands
rm -f "$lab/cache/weave-router/"checked-at*.commands
: >"$log"
run_tool cc "$installed" "WEAVE_COMMANDS_URL_BASE=https://selfhost.example.test/install/commands"
wait_for 20 log_has "$log" "selfhost/install/cc-statusline.sh"
wait_for 20 no_glob_match "$baseline/.MANIFEST.tmp.*"
sleep 0.5
check "a release older than the installed wrappers does not roll them back" "$(cat "$cmd_dir/fm.md")" "$newer_fm"
check_not_contains "a release older than the installed wrappers fetches no wrapper body" "$(cat "$log")" "commands/fm.md"

# Command directories whose cache slugs collide share the floor file, so it
# keeps one row per directory: another install's cycle must not erase this
# directory's floor, and another directory's row must not block this one.
other_home="${lab}_home"; other_cmd="$other_home/.claude/commands"
mkdir -p "$other_home/.weave" "$other_cmd"
cp "$cc_keyed" "$other_home/.weave/cc-statusline.sh"
cp "$script_dir/../commands/fm.md" "$script_dir/../commands/rf.md" "$other_cmd/"
rm -f "$lab/cache/weave-router/"checked-at*.commands
: >"$log"
run_tool cc "$other_home/.weave/cc-statusline.sh" "HOME=$other_home" \
  "WEAVE_COMMANDS_URL_BASE=https://selfhost.example.test/install/commands"
sync_settle "install/commands/rf.md"
rm -f "$lab/cache/weave-router/"checked-at*.commands
: >"$log"
run_tool cc "$installed" "WEAVE_COMMANDS_URL_BASE=https://selfhost.example.test/install/commands"
wait_for 20 log_has "$log" "selfhost/install/cc-statusline.sh"
wait_for 20 no_glob_match "$baseline/.MANIFEST.tmp.*"
sleep 0.5
check_not_contains "a colliding install's cycle keeps this directory's release floor" "$(cat "$log")" "commands/fm.md"

printf '%s\t%s\n' "$next_tag" "/elsewhere/.claude/commands" >"$baseline/.manifest-tag"
rm -f "$lab/cache/weave-router/"checked-at*.commands
: >"$log"
run_tool cc "$installed" "WEAVE_COMMANDS_URL_BASE=https://selfhost.example.test/install/commands"
if sync_settle "install/commands/rf.md"; then
  ok "a release floor recorded for another command directory is ignored"
else
  no "a release floor recorded for another command directory is ignored" "wrappers fetched" "$(cat "$log")"
fi

# The floor is recorded before any wrapper moves; a cycle that cannot record it
# installs nothing, so a later replay cannot roll the newer wrappers back.
rm -f "$baseline/.manifest-tag"; mkdir "$baseline/.manifest-tag"
retag "$release_dir/cc-statusline.sh" "$next_tag"
upstream_wrapper fm "release $next_tag fm, unrecorded."
sign_commands
fm_before="$(cat "$cmd_dir/fm.md")"
rm -f "$lab/cache/weave-router/"checked-at*.commands
: >"$log"
run_tool cc "$installed" "WEAVE_COMMANDS_URL_BASE=https://selfhost.example.test/install/commands"
wait_for 20 log_has "$log" "selfhost/install/cc-statusline.sh"
wait_for 20 no_glob_match "$baseline/.MANIFEST.tmp.*"
sleep 0.5
check "a cycle that cannot record its release floor installs no wrapper" "$(cat "$cmd_dir/fm.md")" "$fm_before"
check_not_contains "a cycle that cannot record its release floor fetches no wrapper body" "$(cat "$log")" "commands/fm.md"

# The scope selector recovered from the installed /router-off is unverified, so
# only the shapes the installer writes are copied into verified wrappers.
scope_lab() {
  commands_lab "$1"
  : >"$baseline/.manifest-seeded"
  local off_body on_body
  off_body="$(cat "$script_dir/../commands/router-off.md")"
  on_body="$(cat "$script_dir/../commands/router-on.md")"
  printf '%s\n' "${off_body//"{{SCOPE}}"/"$2"}" >"$cmd_dir/router-off.md"
  cp "$script_dir/../commands/router-on.md" "$baseline/router-on.md"
  printf '%s\n' "${on_body//"{{SCOPE}}"/"$2"}" >"$cmd_dir/router-on.md"
  sed 's#^Turn#Now turn#' "$script_dir/../commands/router-on.md" >"$release_dir/commands/router-on.md"
  sign_commands
}
scope_lab cmd-scope-project " --scope project"
run_tool cc "$installed"
if wait_for 20 grep -q '^Now turn' "$cmd_dir/router-on.md"; then
  ok "an installer-shaped scope selector still refreshes scoped wrappers"
else
  no "an installer-shaped scope selector still refreshes scoped wrappers" "refreshed router-on" "$(cat "$cmd_dir/router-on.md")"
fi
sync_settle "commands/router-on.md"
check_contains "a refreshed scoped wrapper keeps its scope selector" "$(cat "$cmd_dir/router-on.md")" "on --claude --scope project"

mkdir -p "$work/cmd-scope-own-dir/home"
dir_selector=" --dir $(printf '%q' "$(cd "$work/cmd-scope-own-dir/home" && pwd -P)")"
scope_lab cmd-scope-own-dir "$dir_selector"
run_tool cc "$installed"
if wait_for 20 grep -q '^Now turn' "$cmd_dir/router-on.md"; then
  ok "a --dir selector naming this install still refreshes scoped wrappers"
else
  no "a --dir selector naming this install still refreshes scoped wrappers" "refreshed router-on" "$(cat "$cmd_dir/router-on.md")"
fi
sync_settle "commands/router-on.md"
check_contains "a refreshed --dir wrapper keeps its selector" "$(cat "$cmd_dir/router-on.md")" "on --claude$dir_selector"

scope_lab cmd-scope-escaped ' --dir x\;touch\ pwned'
on_before="$(cat "$cmd_dir/router-on.md")"
run_tool cc "$installed"
sync_settle "commands/router-on.md"
check "a --dir selector naming another directory refreshes no scoped wrapper" "$(cat "$cmd_dir/router-on.md")" "$on_before"


scope_lab cmd-scope-injected " --scope project; touch pwned"
on_before="$(cat "$cmd_dir/router-on.md")"
run_tool cc "$installed"
sync_settle "commands/router-on.md"
check "a tampered scope selector refreshes no scoped wrapper" "$(cat "$cmd_dir/router-on.md")" "$on_before"
check_not_contains "a tampered scope selector reaches no verified wrapper" "$(cat "$cmd_dir"/*.md)" "Now turn"

# ---------- fetched uninstaller (surface 4) ----------

echo "install.sh --uninstall"

# uninstall_lab <name>: install.sh with no sibling uninstall.sh, main
# advertising $next_tag, and a genuine signed uninstall.sh at that tag that
# records each run.
uninstall_lab() {
  new_lab "$1"
  mkdir -p "$lab/bin-dir"
  cp "$installer_keyed" "$lab/bin-dir/install.sh"
  cp "$cc_keyed" "$pub/raw/weave-os/router/main/install/cc-statusline.sh"
  retag "$pub/raw/weave-os/router/main/install/cc-statusline.sh" "$next_tag"
  release_dir="$(rel_dir "$next_tag")"; mkdir -p "$release_dir"
  # The body expands $HOME when it runs, not here.
  # shellcheck disable=SC2016
  printf '%s\n' '#!/usr/bin/env bash' 'printf "genuine\n" >>"$HOME/uninstall-ran"' >"$release_dir/uninstall.sh"
  manifest_row "install/uninstall.sh" "$release_dir/uninstall.sh" >"$release_dir/MANIFEST"
  sign "$release_dir/MANIFEST" "$keys/release.key" "$release_dir/MANIFEST.sig.b64"
}
run_uninstall() { # run_uninstall [VAR=value...]: sets rc
  local env_cmd=()
  mapfile -t env_cmd < <(victim_env "$PATH")
  "${env_cmd[@]}" "$@" bash "$lab/bin-dir/install.sh" --uninstall </dev/null >"$lab/stdout" 2>"$lab/stderr"
  rc=$?
}

uninstall_lab un-happy
run_uninstall
check "a signed uninstaller runs" "$rc:$(cat "$home/uninstall-ran" 2>/dev/null)" "0:genuine"

uninstall_lab un-tampered
# shellcheck disable=SC2016
printf '%s\n' 'printf "tampered\n" >>"$HOME/canary.txt"' >>"$release_dir/uninstall.sh"
run_uninstall
check "a tampered uninstaller is refused" "$([ "$rc" -ne 0 ] && echo refused)" refused
check_contains "the refusal names the manifest check" "$(cat "$lab/stderr")" "Refusing to run uninstall.sh"
if file_absent "$home/canary.txt" && file_absent "$home/uninstall-ran"; then
  ok "a tampered uninstaller never runs"
else
  no "a tampered uninstaller never runs" "no run" "it ran"
fi

uninstall_lab un-nosig
rm -f "$release_dir/MANIFEST.sig.b64"
run_uninstall
check "an uninstaller without MANIFEST.sig.b64 is refused" "$([ "$rc" -ne 0 ] && file_absent "$home/uninstall-ran" && echo refused)" refused

uninstall_lab un-otherkey
sign "$release_dir/MANIFEST" "$keys/other.key" "$release_dir/MANIFEST.sig.b64"
run_uninstall
check "an uninstaller MANIFEST signed with another key is refused" "$([ "$rc" -ne 0 ] && file_absent "$home/uninstall-ran" && echo refused)" refused

uninstall_lab un-optin-default
rm -f "$release_dir/MANIFEST" "$release_dir/MANIFEST.sig.b64"
run_uninstall WEAVE_STATUSLINE_TRUST_UNSIGNED=1 \
  "WEAVE_UNINSTALL_URL=https://raw.githubusercontent.com/weave-os/router/$next_tag/install/uninstall.sh"
check "WEAVE_STATUSLINE_TRUST_UNSIGNED=1 is refused for the default-publisher uninstaller" \
  "$([ "$rc" -ne 0 ] && file_absent "$home/uninstall-ran" && echo refused)" refused

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
