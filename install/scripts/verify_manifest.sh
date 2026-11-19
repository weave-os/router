#!/usr/bin/env bash
#
# Signed-manifest gate for the self-updating client scripts (W-1828).
#
# Source this file and call:
#
#   verify_fetched_file <tag_url_base> <embedded_pubkey> <logical_path> \
#                       <candidate_file> <manifest_cache_dir>
#
# It returns 0 only when <tag_url_base>/MANIFEST carries a valid minisign
# signature from <embedded_pubkey> AND the sha256 of <candidate_file> equals
# the MANIFEST entry for <logical_path>. Every other outcome is non-zero:
# minisign or curl missing, empty pubkey, fetch failure, bad signature,
# missing or duplicate entry, malformed digest, or sha mismatch. It prints
# nothing, since callers run in detached forks feeding a statusline.
#
# minisign must be on PATH. Without it every call refuses; a pure-shell
# Ed25519 verifier is deliberately not provided.
#
# URL layout (immutable release tag, never a mutable branch):
#   <tag_url_base>/MANIFEST           sha256 list
#   <tag_url_base>/MANIFEST.minisig   detached minisign signature of MANIFEST
#   <tag_url_base>/install/<path>     the files the manifest covers
#
# MANIFEST format matches coreutils `sha256sum` output so operators can check
# a release with `sha256sum -c MANIFEST`: one `<64 hex sha256>  <path>` line
# per file (a `*` binary marker before the path is accepted), paths relative
# to the tag root (e.g. `install/cc-statusline.sh`), blank lines and lines
# starting with `#` ignored.
#
# MANIFEST and MANIFEST.minisig are downloaded into <manifest_cache_dir> only
# when not already there, and the signature is checked once per cache dir
# (a `MANIFEST.verified` marker records success). A caller verifying several
# files in one fork passes the same private, fork-scoped cache dir to fetch
# and verify the manifest once; the caller owns cleaning it up.

weave_manifest_sha256() {
  local out
  if command -v sha256sum >/dev/null 2>&1; then
    out="$(sha256sum < "$1" 2>/dev/null)" || return 1
  elif command -v shasum >/dev/null 2>&1; then
    out="$(shasum -a 256 < "$1" 2>/dev/null)" || return 1
  elif command -v openssl >/dev/null 2>&1; then
    out="$(openssl dgst -sha256 -r < "$1" 2>/dev/null)" || return 1
  else
    return 1
  fi
  printf '%s\n' "${out%% *}" | tr 'A-F' 'a-f'
}

weave_manifest_fetch() {
  local url="$1" dest="$2" part
  [ -s "$dest" ] && return 0
  part="${dest}.part.$$"
  if curl -fsSL --max-time 15 "$url" -o "$part" </dev/null >/dev/null 2>&1 \
     && [ -s "$part" ] && mv -f "$part" "$dest" 2>/dev/null; then
    return 0
  fi
  rm -f "$part" 2>/dev/null
  return 1
}

verify_fetched_file() {
  local tag_url_base="${1:-}" pubkey="${2:-}" logical_path="${3:-}"
  local candidate="${4:-}" cache_dir="${5:-}"
  [ -n "$tag_url_base" ] && [ -n "$pubkey" ] && [ -n "$logical_path" ] \
    && [ -n "$candidate" ] && [ -n "$cache_dir" ] || return 2
  [ -f "$candidate" ] || return 2
  case "$pubkey" in *[!A-Za-z0-9+/=]*) return 2 ;; esac

  command -v minisign >/dev/null 2>&1 || return 3
  command -v curl >/dev/null 2>&1 || return 3

  tag_url_base="${tag_url_base%/}"
  local manifest="$cache_dir/MANIFEST"
  local sig="$cache_dir/MANIFEST.minisig"
  local marker="$cache_dir/MANIFEST.verified"

  mkdir -p "$cache_dir" 2>/dev/null || return 4
  weave_manifest_fetch "$tag_url_base/MANIFEST" "$manifest" || return 4
  weave_manifest_fetch "$tag_url_base/MANIFEST.minisig" "$sig" || return 4

  if [ ! -f "$marker" ]; then
    minisign -V -q -P "$pubkey" -x "$sig" -m "$manifest" </dev/null >/dev/null 2>&1 || return 5
    : > "$marker" 2>/dev/null || return 5
  fi

  local expected actual
  expected="$(awk -v want="$logical_path" '
    /^[[:space:]]*(#|$)/ { next }
    {
      line = $0
      sub(/^[ \t]+/, "", line)
      sha = $1
      path = substr(line, length(sha) + 1)
      sub(/^[ \t]+/, "", path)
      sub(/^\*/, "", path)
      sub(/\r$/, "", path)
      if (path == want) { n++; hit = sha }
    }
    END { if (n == 1) print tolower(hit); else exit 1 }
  ' "$manifest" 2>/dev/null)" || return 6
  printf '%s' "$expected" | grep -Eq '^[0-9a-f]{64}$' || return 6

  actual="$(weave_manifest_sha256 "$candidate")" || return 7
  [ "$actual" = "$expected" ] || return 7
  return 0
}

if [ "${BASH_SOURCE[0]:-$0}" = "$0" ]; then
  verify_fetched_file "$@"
fi
