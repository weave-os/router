#!/usr/bin/env bash
#
# `--all`: one command that sets up the Weave Router and the Weave telemetry
# plugins for every coding agent found on this machine.
#
#   npx @weave-os/router --all --token <enrollment token>
#
# The flow is: detect harnesses locally, plan what to install, exchange the
# enrollment token for exactly the credentials the plan needs, then run the
# existing per-harness installers. Nothing harness-specific lives here beyond
# the table below; adding a harness is one row.
#
# Re-running is safe and is how a newly installed agent gets set up: the
# backend replaces the keys it issued to this machine earlier.

set -euo pipefail
export PATH="$PATH:$HOME/.weave/bin"

# id|label|router_target|plugin_kind|dirs|bins|apps
#   router_target  install.sh flag (without --); empty when the router has no target.
#   plugin_kind    Weave plugin kind sent to the exchange; empty when there is no plugin.
#   dirs/apps      comma-separated; a leading ~ is $HOME. bins are looked up on PATH.
# A harness is present when any of its dirs, bins or apps exists.
# >>> weave-router harnesses >>>
WEAVE_HARNESSES=$(cat <<'WEAVE_HARNESSES_EOF'
claude_code|Claude Code|claude|claude_code|~/.claude|claude|
codex|Codex|codex|codex|~/.codex|codex|
copilot|Copilot CLI|copilot|copilot|~/.copilot|copilot|
opencode|opencode|opencode||~/.config/opencode|opencode|
pi|pi|pi||~/.pi|pi|
cursor|Cursor||cursor|~/.cursor|cursor,cursor-agent|/Applications/Cursor.app
grok_build|Grok Build||grok_build|~/.grok|grok|
WEAVE_HARNESSES_EOF
)
# <<< weave-router harnesses <<<

DEFAULT_APP_URL="https://app.weaveos.com"
INSTALLER_URL="${WEAVE_INSTALLER_URL:-https://raw.githubusercontent.com/weave-os/router/main/install/install.sh}"

err() { printf 'error: %s\n' "$1" >&2; }
info() { printf '%s\n' "$1"; }

usage() {
  cat <<'USAGE'
Set up the Weave Router and Weave telemetry plugins for every coding agent on this machine.

  npx @weave-os/router --all --token <enrollment token>

Options
  --token <token>    enrollment token from the Weave setup page (or WEAVE_INSTALL_TOKEN)
  --app-url <url>    Weave app to enroll with (default https://app.weaveos.com)
  --dry-run          show what was found and what would be installed, then stop
USAGE
}

# ---------- detect (reads only $HOME and PATH) ----------

harness_present() {
  local dirs="$1" bins="$2" apps="$3" candidate
  local IFS=','
  for candidate in $dirs $apps; do
    # shellcheck disable=SC2088 # The table spells $HOME as a literal ~.
    case "$candidate" in
      "~/"*) candidate="$HOME/${candidate#"~/"}" ;;
    esac
    [ -e "$candidate" ] && return 0
  done
  for candidate in $bins; do
    command -v "$candidate" >/dev/null 2>&1 && return 0
  done
  return 1
}

# ---------- plan (pure: table x detection) ----------

found_labels=()
missing_labels=()
router_targets=()
plugin_kinds=()

plan() {
  local id label router_target plugin_kind dirs bins apps
  while IFS='|' read -r id label router_target plugin_kind dirs bins apps; do
    [ -n "$id" ] || continue
    if ! harness_present "$dirs" "$bins" "$apps"; then
      missing_labels+=("$label")
      continue
    fi
    found_labels+=("$label")
    [ -z "$router_target" ] || router_targets+=("$router_target")
    [ -z "$plugin_kind" ] || plugin_kinds+=("$plugin_kind")
  done <<<"$WEAVE_HARNESSES"
}

join_by() {
  local separator="$1" joined=""
  shift
  local item
  for item in "$@"; do
    joined="${joined}${joined:+$separator}${item}"
  done
  printf '%s' "$joined"
}

# ---------- adapters ----------

# The machine id tells the backend which earlier keys this run replaces, so a
# member's other machines keep theirs. It is generated once and kept.
machine_id() {
  local id_file="$HOME/.weave/machine-id" id=""
  if [ -f "$id_file" ]; then
    id="$(tr -cd 'A-Za-z0-9_-' <"$id_file" | cut -c1-64)"
  fi
  if [ -z "$id" ]; then
    id="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
    mkdir -p "$HOME/.weave"
    printf '%s\n' "$id" >"$id_file"
    chmod 600 "$id_file"
  fi
  printf '%s' "$id"
}

# The token goes to curl on stdin, never in argv, so it does not show in `ps`.
exchange() {
  printf 'header = "Authorization: Bearer %s"\n' "$2" \
    | curl -sS --max-time 60 --config - \
        -H 'Content-Type: application/json' \
        -d "$3" -w '\n%{http_code}' "$1/api/install/exchange"
}

resolve_installer() {
  local script_path="${BASH_SOURCE[0]:-$0}" sibling_dir=""
  if [ -f "$script_path" ]; then
    sibling_dir="$(cd "$(dirname "$script_path")" 2>/dev/null && pwd)"
  fi
  if [ -n "$sibling_dir" ] && [ -f "$sibling_dir/install.sh" ]; then
    printf '%s' "$sibling_dir/install.sh"
    return 0
  fi
  local fetched="$work_dir/install.sh"
  if ! curl -fsSL --max-time 60 "$INSTALLER_URL" -o "$fetched" || [ "$(head -c 2 "$fetched")" != "#!" ]; then
    err "Failed to fetch install.sh from $INSTALLER_URL."
    return 1
  fi
  printf '%s' "$fetched"
}

# ---------- main ----------

token="${WEAVE_INSTALL_TOKEN:-}"
app_url="${WEAVE_APP_URL:-$DEFAULT_APP_URL}"
dry_run="false"

while [ $# -gt 0 ]; do
  case "$1" in
    --all) shift ;;
    --token)
      [ $# -ge 2 ] || { err "--token needs a value."; exit 2; }
      token="$2"; shift 2 ;;
    --app-url)
      [ $# -ge 2 ] || { err "--app-url needs a value."; exit 2; }
      app_url="$2"; shift 2 ;;
    --dry-run) dry_run="true"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) err "Unknown flag for --all: $1."; usage >&2; exit 2 ;;
  esac
done

plan

if [ "${#found_labels[@]}" -eq 0 ]; then
  info "No supported coding agents found on this machine."
  info "Looked for: $(join_by ', ' "${missing_labels[@]}")."
  info "Install one, then re-run this command."
  exit 1
fi

info "Found: $(join_by ', ' "${found_labels[@]}")"
[ "${#missing_labels[@]}" -eq 0 ] || info "Not found: $(join_by ', ' "${missing_labels[@]}")"
info "Router: $(join_by ', ' "${router_targets[@]+"${router_targets[@]}"}")"
info "Plugins: $(join_by ', ' "${plugin_kinds[@]+"${plugin_kinds[@]}"}")"

if [ "$dry_run" = "true" ]; then
  info "Dry run: nothing was installed."
  exit 0
fi

case "$token" in
  "") err "An enrollment token is required: pass --token, or set WEAVE_INSTALL_TOKEN. Get one from the Weave setup page."; exit 2 ;;
  *[!A-Za-z0-9_-]*) err "The enrollment token has characters a token never contains. Copy the command again from the Weave setup page."; exit 2 ;;
esac
app_url="${app_url%/}"
case "$app_url" in
  https://*|http://localhost|http://localhost:*|http://127.0.0.1|http://127.0.0.1:*) ;;
  *) err "--app-url must be https (http is allowed only for localhost)."; exit 2 ;;
esac
case "$app_url" in
  *[!A-Za-z0-9:/._-]*) err "--app-url has characters a URL host never contains."; exit 2 ;;
esac
for required in curl jq; do
  command -v "$required" >/dev/null 2>&1 || { err "$required is required but not installed."; exit 1; }
done

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

need_router_key="false"
[ "${#router_targets[@]}" -eq 0 ] || need_router_key="true"
request_json="$(jq -cn --arg machine_id "$(machine_id)" --argjson need_router_key "$need_router_key" \
  '{machine_id: $machine_id, need_router_key: $need_router_key, plugins: $ARGS.positional}' \
  --args "${plugin_kinds[@]+"${plugin_kinds[@]}"}")"

if ! exchange_output="$(exchange "$app_url" "$token" "$request_json")"; then
  err "Could not reach $app_url. Nothing was installed and the token was not used."
  exit 1
fi
http_status="${exchange_output##*$'\n'}"
exchange_body="${exchange_output%$'\n'*}"
if [ "$http_status" != "200" ]; then
  refusal="$(jq -r '.message // empty' <<<"$exchange_body" 2>/dev/null || true)"
  err "Weave refused the enrollment (HTTP $http_status). ${refusal:-Copy a new command from the Weave setup page and re-run.}"
  exit 1
fi

installed=()
failed=()

router_key="$(jq -r '.router_key // empty' <<<"$exchange_body")"
# A key the hosted app issued is only ever sent to the hosted router, even when
# this shell points WEAVE_ROUTER_URL at another deployment.
# The comparison ignores case, the default port and the root dot, so every
# spelling of the hosted app counts; the URL that was called is left as given.
normalized_app_url="$(printf '%s' "$app_url" | tr '[:upper:]' '[:lower:]')"
normalized_app_url="${normalized_app_url%:443}"
[ "${normalized_app_url%.}" != "$DEFAULT_APP_URL" ] || unset WEAVE_ROUTER_URL
if [ "${#router_targets[@]}" -gt 0 ]; then
  # A missing installer fails the router targets only; plugins still install.
  installer="$(resolve_installer)" || installer=""
  for target in "${router_targets[@]}"; do
    if [ -n "$installer" ] && [ -n "$router_key" ] && WEAVE_ROUTER_KEY="$router_key" bash "$installer" "--$target" --non-interactive; then
      installed+=("router for $target")
    else
      failed+=("router for $target")
    fi
  done
fi

# Walks what was requested, not what came back, so a plugin the response leaves
# out is reported as failed instead of silently dropped.
for kind in "${plugin_kinds[@]+"${plugin_kinds[@]}"}"; do
  if jq -e --arg kind "$kind" '[.unsupported[]?] | index($kind) != null' <<<"$exchange_body" >/dev/null; then
    continue
  fi
  install_command="$(jq -r --arg kind "$kind" '[.plugins[]? | select(.kind == $kind) | .install_command // empty][0] // empty' <<<"$exchange_body")"
  # The command comes from the Weave app the user chose to enroll with and
  # carries this member's plugin key, so it is run but never printed.
  if [ -n "$install_command" ] && bash -c "$install_command"; then
    installed+=("$kind plugin")
  else
    failed+=("$kind plugin")
  fi
done
skipped="$(jq -r '[.unsupported[]?] | join(", ")' <<<"$exchange_body")"

info ""
[ "${#installed[@]}" -eq 0 ] || info "Installed: $(join_by ', ' "${installed[@]}")"
[ -z "$skipped" ] || info "No one-command plugin install yet: $skipped"
[ "${#missing_labels[@]}" -eq 0 ] || info "Not found: $(join_by ', ' "${missing_labels[@]}"). Re-run this command after installing one to add it."
if [ "${#failed[@]}" -gt 0 ]; then
  err "Failed: $(join_by ', ' "${failed[@]}"). Fix the errors shown above, then copy a new command from the Weave setup page and re-run."
  exit 1
fi
