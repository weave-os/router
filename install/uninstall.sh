#!/usr/bin/env bash
#
# Weave Router uninstaller for Claude Code, Codex, opencode, and pi.
#
# Default target is Claude Code: removes the env vars, statusLine, and local
# router auth that install.sh added; leaves the rest of settings.json
# untouched. Pass --codex to strip the managed [model_providers.weave]
# block (and the matching top-level `model_provider`) from Codex's
# config.toml — anything outside the markers is preserved. Pass --opencode
# to strip the `provider.weave` block (and the top-level `model` key when
# it points at the router) from opencode.json; other providers and user
# settings are preserved. Pass --pi to strip the `weave` provider from
# pi's models.json, drop @workweave/router from settings.json, revert the
# weave defaults, and remove the router key file.
#
# Usage:
#   npx @workweave/router --uninstall                            # Claude Code, user scope
#   npx @workweave/router --uninstall --codex                    # Codex, user scope
#   npx @workweave/router --uninstall --opencode                 # opencode, user scope
#   npx @workweave/router --uninstall --pi                       # pi, user scope
#   npx @workweave/router --uninstall --scope project            # run inside the repo
#   npx @workweave/router --uninstall --dir /tmp/test            # --dir alone (user scope, .weave/)
#   npx @workweave/router --uninstall --scope project --dir /tmp # --dir + project scope (.claude/)

set -euo pipefail

scope="user"
scope_explicit="false"
install_dir=""
script_dir="$(cd "$(dirname "$0")" 2>/dev/null && pwd || true)"

# ---------- directive registry (embedded) ----------
#
# uninstall.sh is served standalone too (the npm bin runs it directly, and it
# can be piped), so it cannot rely on a sibling registry.sh. The data and
# helpers are embedded verbatim; install/tests/registry_test.sh asserts they
# never drift from the canonical files. Regenerate with `make embed-registry`.
WEAVE_REGISTRY_DATA=$(cat <<'WEAVE_REGISTRY_EOF'
# Weave Router directive registry
# canonical|aliases|capability|claude|codex|opencode|pi|cursor|adapter
# aliases are comma-separated; client columns are yes/no. adapter is the native asset kind.
force-model|fm|prompt|yes|yes|yes|yes|manual|command,skill
unforce-model|ufm|prompt|yes|yes|yes|yes|manual|command,skill
router-feedback|rf|prompt|yes|yes|yes|no|manual|command,skill
router-off||local-toggle|yes|yes|no|no|manual|command,skill
router-on||local-toggle|yes|yes|no|no|manual|command,skill
router-status||local-toggle|yes|yes|no|no|manual|command,skill
router-session||prompt|yes|no|no|no|manual|command
router-models|models|local-toggle|yes|yes|no|no|manual|command,skill
disable-routing||local-toggle|no|yes|no|no|manual|skill
beta||prompt|yes|no|no|yes|manual|command
WEAVE_REGISTRY_EOF
)

# >>> weave-router registry lib >>>
weave_registry_rows() {
  if [ -n "${WEAVE_REGISTRY_DATA:-}" ]; then
    printf '%s\n' "$WEAVE_REGISTRY_DATA" | awk -F '|' '!/^([[:space:]]*#|[[:space:]]*$)/ { print }'
    return 0
  fi
  local registry="${WEAVE_REGISTRY_FILE:-}"
  if [ -z "$registry" ]; then
    local dir
    dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd -P)" || return 1
    registry="$dir/directives.tsv"
  fi
  [ -f "$registry" ] || return 1
  awk -F '|' '!/^([[:space:]]*#|[[:space:]]*$)/ { print }' "$registry"
}

# weave_registry_names lists every canonical name and alias a client installs.
weave_registry_names() {
  local target="$1" canonical aliases capability claude codex opencode pi cursor adapter
  while IFS='|' read -r canonical aliases capability claude codex opencode pi cursor adapter; do
    case "$target" in
      claude) [ "$claude" = yes ] || continue ;; codex) [ "$codex" = yes ] || continue ;;
      opencode) [ "$opencode" = yes ] || continue ;; pi) [ "$pi" = yes ] || continue ;;
      cursor) [ "$cursor" = yes ] || continue ;;
    esac
    printf '%s\n' "$canonical"
    [ -n "$aliases" ] && printf '%s\n' "$aliases" | tr ',' '\n'
  done <<EOF
$(weave_registry_rows)
EOF
}

# weave_registry_skill_names lists the prompt directives a client exposes as a
# native skill. Local-config toggles are excluded: they mutate config this
# installer owns and are handled per target, not as a generic prompt.
weave_registry_skill_names() {
  local target="$1" canonical aliases capability claude codex opencode pi cursor adapter
  while IFS='|' read -r canonical aliases capability claude codex opencode pi cursor adapter; do
    [ "$capability" = prompt ] || continue
    case "$target" in
      claude) [ "$claude" = yes ] || continue ;; codex) [ "$codex" = yes ] || continue ;;
      opencode) [ "$opencode" = yes ] || continue ;; pi) [ "$pi" = yes ] || continue ;;
      cursor) [ "$cursor" = yes ] || continue ;;
    esac
    printf '%s\n' "$canonical"
    [ -n "$aliases" ] && printf '%s\n' "$aliases" | tr ',' '\n'
  done <<EOF
$(weave_registry_rows)
EOF
}

# weave_registry_skill_assets lists every directive a client ships as a skill
# file, including local-config toggles such as Codex's disable-routing. Install
# and uninstall use this for file management; weave_registry_skill_names is the
# narrower prompt-only set used when generating prompt adapters.
weave_registry_skill_assets() {
  local target="$1" canonical aliases capability claude codex opencode pi cursor adapter
  while IFS='|' read -r canonical aliases capability claude codex opencode pi cursor adapter; do
    case ",$adapter," in *,skill,*) ;; *) continue ;; esac
    case "$target" in
      claude) [ "$claude" = yes ] || continue ;; codex) [ "$codex" = yes ] || continue ;;
      opencode) [ "$opencode" = yes ] || continue ;; pi) [ "$pi" = yes ] || continue ;;
      cursor) [ "$cursor" = yes ] || continue ;;
    esac
    printf '%s\n' "$canonical"
    [ -n "$aliases" ] && printf '%s\n' "$aliases" | tr ',' '\n'
  done <<EOF
$(weave_registry_rows)
EOF
}

# weave_registry_canonical_for resolves a name or alias to its canonical
# directive, and fails when the name is not in the registry at all.
weave_registry_canonical_for() {
  local wanted="$1" canonical aliases capability claude codex opencode pi cursor adapter alias
  while IFS='|' read -r canonical aliases capability claude codex opencode pi cursor adapter; do
    [ "$wanted" = "$canonical" ] && { printf '%s' "$canonical"; return 0; }
    IFS=',' read -ra _aliases <<< "$aliases"
    for alias in ${_aliases[@]+"${_aliases[@]}"}; do
      [ "$wanted" = "$alias" ] && { printf '%s' "$canonical"; return 0; }
    done
  done <<EOF
$(weave_registry_rows)
EOF
  return 1
}
# <<< weave-router registry lib <<<

target="claude"

err()  { printf "\033[31merror:\033[0m %s\n" "$*" >&2; }
warn() { printf "\033[33mwarning:\033[0m %s\n" "$*" >&2; }
info() { printf "\033[36m==>\033[0m %s\n" "$*"; }
ok()   { printf "\033[32m✓\033[0m %s\n" "$*"; }

# Refuse to write/delete through a symlink. Project scope reads paths from the
# user's git repo; a hostile checkout could ship `.claude/settings.json` (or
# `.claude/cc-statusline.sh`) as a symlink to e.g. `~/.ssh/authorized_keys`,
# and the uninstaller's `>` redirect or `rm` would silently follow that link.
refuse_if_symlink() {
  local target="$1"
  if [ -L "$target" ]; then
    err "$target is a symlink (-> $(readlink "$target")). Refusing to operate on it."
    exit 1
  fi
}

while [ $# -gt 0 ]; do
  case "$1" in
    --scope)
      scope="${2:-}"; shift 2
      [ "$scope" = "user" ] || [ "$scope" = "project" ] || { err "--scope must be 'user' or 'project'"; exit 2; }
      scope_explicit="true"
      ;;
    --dir)
      install_dir="${2:-}"; shift 2
      [ -n "$install_dir" ] || { err "--dir requires a path."; exit 2; }
      ;;
    --codex)
      target="codex"; shift
      ;;
    --opencode)
      target="opencode"; shift
      ;;
    --pi)
      target="pi"; shift
      ;;
    --claude)
      # No-op selector for symmetry with --codex / --opencode and install.sh's
      # --claude. Lets `./install.sh --uninstall --claude` (which forwards
      # remaining args here) succeed instead of hitting the unknown-flag
      # catch-all.
      target="claude"; shift
      ;;
    -h|--help)
      # awk avoids GNU `head -n -<N>` (rejected by BSD head on macOS).
      awk 'NR<2 { next } /^set -euo/ { exit } { sub(/^# ?/, ""); print }' "$0"
      exit 0
      ;;
    *)
      err "Unknown flag: $1. Run --help for usage."; exit 2
      ;;
  esac
done

if { [ "$target" = "claude" ] || [ "$target" = "opencode" ] || [ "$target" = "pi" ]; } && ! command -v jq >/dev/null 2>&1; then
  err "jq is required for the $target uninstall path."
  exit 1
fi

# Markers must stay in sync with install.sh. Keep verbatim.
WEAVE_CODEX_BEGIN_MARKER="# >>> weave-router managed (do not edit between markers) >>>"
WEAVE_CODEX_END_MARKER="# <<< weave-router managed <<<"

# strip_codex_block rewrites config.toml without the managed block and any
# top-level `model_provider = "weave"` that lived outside the markers (which
# can happen if the user copy-pasted our key into their own config). Other
# top-level model_provider values are preserved so we don't yank a user back
# into the OpenAI default when they meant to keep their own.
strip_codex_block() {
  local config_file="$1"
  local tmp; tmp="$(mktemp -t weave-codex-uninstall.XXXXXX)"
  awk -v begin="$WEAVE_CODEX_BEGIN_MARKER" -v end="$WEAVE_CODEX_END_MARKER" '
    $0 == begin { skip = 1; next }
    $0 == end   { skip = 0; next }
    skip        { next }
    /^[[:space:]]*\[/ { in_section = 1 }
    !in_section && /^[[:space:]]*model_provider[[:space:]]*=[[:space:]]*"weave"[[:space:]]*$/ { next }
    { print }
  ' "$config_file" >"$tmp"
  mv "$tmp" "$config_file"
}

# ---------- opencode uninstall path ----------

if [ "$target" = "opencode" ]; then
  # Resolve opencode_config_file based on scope/dir. Mirrors install.sh so
  # an `install --opencode --scope project` is exactly reversed by an
  # `uninstall --opencode --scope project`.
  if [ -n "$install_dir" ]; then
    install_dir="$(cd "$install_dir" 2>/dev/null && pwd || echo "$install_dir")"
    opencode_dir="$install_dir"
    refuse_if_symlink "$opencode_dir"
  elif [ "$scope" = "user" ]; then
    opencode_dir="${XDG_CONFIG_HOME:-$HOME/.config}/opencode"
  else
    # Project scope: same prompt + git-root fallback as install.sh.
    project_dir=""
    if [ "$scope_explicit" = "false" ] && [ -r /dev/tty ]; then
      default_project_dir="$(pwd)"
      printf "Project directory to uninstall from [default: %s]: " "$default_project_dir"
      read -r project_dir_choice </dev/tty || project_dir_choice=""
      project_dir="${project_dir_choice:-$default_project_dir}"
      case "$project_dir" in
        "~")    project_dir="$HOME" ;;
        "~/"*)  project_dir="$HOME/${project_dir#~/}" ;;
      esac
      if [ ! -d "$project_dir" ]; then
        err "Directory does not exist: $project_dir."
        exit 1
      fi
      project_dir="$(cd "$project_dir" && pwd)"
    fi
    if [ -n "${project_dir:-}" ]; then
      opencode_dir="$project_dir"
    else
      if ! git_root="$(git rev-parse --show-toplevel 2>/dev/null)"; then
        err "--scope project must be run inside a git repo, or use --dir <path>."
        exit 1
      fi
      opencode_dir="$git_root"
    fi
    refuse_if_symlink "$opencode_dir"
  fi
  opencode_config_file="$opencode_dir/opencode.json"
  refuse_if_symlink "$opencode_config_file"

  # Canonicalize the plugin path exactly as install.sh did (`cd … && pwd`) so
  # the `plugin` array entry matches on removal — a raw "$opencode_dir/…" string
  # can differ (symlinks, trailing slash) and leave the entry behind.
  if [ -d "$opencode_dir" ]; then
    opencode_plugin="$(cd "$opencode_dir" && pwd)/.weave/opencode-weave.ts"
  else
    opencode_plugin="$opencode_dir/.weave/opencode-weave.ts"
  fi
  if [ -f "$opencode_config_file" ]; then
    # Strip every managed provider (`weave`, the login-only `weave-claude`, and
    # the legacy `weave-codex` from pre-upgrade installs), the managed plugin
    # entry from the `plugin` array, and any router-pointing top-level model
    # (the `weave/`, `weave-claude/`, and `weave-codex/` prefixes — otherwise a
    # default survives and points at a deleted provider). Other providers,
    # user-set models that don't reference the router, other plugins, and any
    # unrelated keys are preserved.
    cleaned="$(jq --arg plugin "$opencode_plugin" '
      (if .provider.weave then del(.provider.weave) else . end)
      | (if .provider["weave-claude"] then del(.provider["weave-claude"]) else . end)
      | (if .provider["weave-codex"] then del(.provider["weave-codex"]) else . end)
      | (if (.provider // {}) == {} then del(.provider) else . end)
      | (if (.plugin | type) == "array" then .plugin -= [$plugin] else . end)
      | (if (.plugin | type) == "array" and (.plugin | length) == 0 then del(.plugin) else . end)
      | (if (.model // "" | tostring | (startswith("weave/") or startswith("weave-claude/") or startswith("weave-codex/"))) then del(.model) else . end)
    ' "$opencode_config_file")"
    printf '%s\n' "$cleaned" >"$opencode_config_file"

    # If only the $schema marker remains (or the file is empty), drop the
    # file entirely so we don't leave a one-key artifact.
    remaining_keys="$(jq -r 'del(."$schema") | keys | length' "$opencode_config_file" 2>/dev/null || echo 0)"
    if [ "$remaining_keys" = "0" ]; then
      rm -f "$opencode_config_file"
      ok "Removed empty $opencode_config_file"
    else
      ok "Cleaned $opencode_config_file"
    fi
  else
    info "No opencode config at $opencode_config_file (already uninstalled?)"
  fi

  # Drop the bundled subscription plugin (no secrets; the config holds the key,
  # opencode's own auth store holds the ChatGPT/Claude tokens). Remove the
  # .weave/ dir only if it's left empty so we don't clobber an unrelated user dir.
  if [ -f "$opencode_plugin" ]; then
    refuse_if_symlink "$opencode_plugin"
    rm -f "$opencode_plugin"
    rmdir "$opencode_dir/.weave" 2>/dev/null || true
    ok "Removed $opencode_plugin"
  fi

  # Drop the toggle parked sidecar (holds the parked router model when off).
  opencode_parked="$opencode_dir/.weave-parked.json"
  if [ -f "$opencode_parked" ]; then
    refuse_if_symlink "$opencode_parked"
    rm -f "$opencode_parked"
    ok "Removed $opencode_parked"
  fi

  # Remove slash command wrapper files this installer owns. Install mirrors
  # this split: project scope uses <repo>/.opencode/commands/, while user
  # scope—including a user-style --dir install—uses the global XDG commands
  # directory so opencode discovers the wrappers from any working directory.
  remove_opencode_command_dir() {
    local opencode_cmds_dir="$1" cmd cmd_file
    if [ -d "$opencode_cmds_dir" ]; then
      refuse_if_symlink "$opencode_cmds_dir"
      while IFS= read -r cmd; do
        cmd_file="$opencode_cmds_dir/$cmd.md"
        if [ -f "$cmd_file" ]; then
          if grep -Fq "<!-- weave-router managed command: $cmd -->" "$cmd_file"; then
            refuse_if_symlink "$cmd_file"
            rm -f "$cmd_file"
            ok "Removed $cmd_file"
          else
            warn "Leaving user-owned opencode command at $cmd_file untouched."
          fi
        fi
      done <<EOF
$(weave_registry_names opencode)
EOF
      rmdir "$opencode_cmds_dir" 2>/dev/null || true
    fi
  }

  if [ "$scope" = "project" ]; then
    opencode_commands_dir="$opencode_dir/.opencode/commands"
  else
    opencode_commands_dir="${XDG_CONFIG_HOME:-$HOME/.config}/opencode/commands"
  fi
  remove_opencode_command_dir "$opencode_commands_dir"

  # Also clean the two paths used by earlier installer revisions, but avoid
  # visiting the canonical path twice. This keeps uninstall idempotent for
  # existing project and --dir installations.
  for legacy_dir in "$opencode_dir/commands" "$opencode_dir/.opencode/commands"; do
    [ "$legacy_dir" = "$opencode_commands_dir" ] && continue
    remove_opencode_command_dir "$legacy_dir"
  done

  if [ -n "$install_dir" ]; then
    ok "Weave Router uninstalled from $install_dir (opencode)."
  else
    ok "Weave Router uninstalled (opencode, scope=$scope)."
  fi
  exit 0
fi

# ---------- pi uninstall path ----------

if [ "$target" = "pi" ]; then
  # Resolve the pi agent dir based on scope/dir. Mirrors install.sh: user scope
  # is pi's default ~/.pi/agent; project/--dir scope is a repo-local .pi.
  if [ -n "$install_dir" ]; then
    install_dir="$(cd "$install_dir" 2>/dev/null && pwd || echo "$install_dir")"
    pi_dir="$install_dir/.pi"
    refuse_if_symlink "$pi_dir"
  elif [ "$scope" = "user" ]; then
    pi_dir="$HOME/.pi/agent"
  else
    # Project scope: same prompt + git-root fallback as the opencode path.
    project_dir=""
    if [ "$scope_explicit" = "false" ] && [ -r /dev/tty ]; then
      default_project_dir="$(pwd)"
      printf "Project directory to uninstall from [default: %s]: " "$default_project_dir"
      read -r project_dir_choice </dev/tty || project_dir_choice=""
      project_dir="${project_dir_choice:-$default_project_dir}"
      case "$project_dir" in
        "~")    project_dir="$HOME" ;;
        "~/"*)  project_dir="$HOME/${project_dir#~/}" ;;
      esac
      if [ ! -d "$project_dir" ]; then
        err "Directory does not exist: $project_dir."
        exit 1
      fi
      project_dir="$(cd "$project_dir" && pwd)"
    fi
    if [ -n "${project_dir:-}" ]; then
      pi_dir="$project_dir/.pi"
    else
      if ! git_root="$(git rev-parse --show-toplevel 2>/dev/null)"; then
        err "--scope project must be run inside a git repo, or use --dir <path>."
        exit 1
      fi
      pi_dir="$git_root/.pi"
    fi
    refuse_if_symlink "$pi_dir"
  fi

  pi_models_file="$pi_dir/models.json"
  pi_settings_file="$pi_dir/settings.json"
  pi_key_file="$pi_dir/.weave_router_key"
  refuse_if_symlink "$pi_models_file"
  refuse_if_symlink "$pi_settings_file"
  refuse_if_symlink "$pi_key_file"

  # models.json: drop provider.weave; remove the file if nothing else remains.
  # Other providers/models the user added are preserved.
  if [ -f "$pi_models_file" ]; then
    cleaned="$(jq '
      (if .providers.weave then del(.providers.weave) else . end)
      | (if (.providers // {}) == {} then del(.providers) else . end)
    ' "$pi_models_file")"
    printf '%s\n' "$cleaned" >"$pi_models_file"
    if [ "$(jq -r 'keys | length' "$pi_models_file" 2>/dev/null || echo 0)" = "0" ]; then
      rm -f "$pi_models_file"
      ok "Removed empty $pi_models_file"
    else
      ok "Cleaned $pi_models_file"
    fi
  else
    info "No pi models config at $pi_models_file (already uninstalled?)"
  fi

  # settings.json: drop our package and revert defaults that still point at the
  # router. Leaving defaultProvider="weave" after removing the provider would
  # break pi startup, so reverting is the correct reverse of the install.
  # defaultModel is reverted ONLY when defaultProvider was "weave" (the state
  # install creates): install sets defaultModel only when it was empty, so a user
  # who independently picked claude-sonnet-4-6 with their own provider keeps it.
  if [ -f "$pi_settings_file" ]; then
    cleaned="$(jq '
      (if .packages then .packages -= ["npm:@workweave/router", "npm:@weave-os/router", "npm:@workweave/pi-router"] else . end)
      | (if (.packages // []) == [] then del(.packages) else . end)
      | (if .defaultProvider == "weave"
           then del(.defaultProvider)
                | (if .defaultModel == "claude-sonnet-4-6" then del(.defaultModel) else . end)
           else . end)
    ' "$pi_settings_file")"
    printf '%s\n' "$cleaned" >"$pi_settings_file"
    if [ "$(jq -r 'keys | length' "$pi_settings_file" 2>/dev/null || echo 0)" = "0" ]; then
      rm -f "$pi_settings_file"
      ok "Removed empty $pi_settings_file"
    else
      ok "Cleaned $pi_settings_file"
    fi
  else
    info "No pi settings at $pi_settings_file (already uninstalled?)"
  fi

  if [ -f "$pi_key_file" ]; then
    rm -f "$pi_key_file"
    ok "Removed $pi_key_file"
  fi

  if [ -n "$install_dir" ]; then
    ok "Weave Router uninstalled from $install_dir (pi)."
  else
    ok "Weave Router uninstalled (pi, scope=$scope)."
  fi
  exit 0
fi

# ---------- codex uninstall path ----------

if [ "$target" = "codex" ]; then
  # Resolve codex_config_file based on scope/dir. Mirrors the install path so
  # an `install --codex --scope project` is exactly reversed by an
  # `uninstall --codex --scope project`.
  if [ -n "$install_dir" ]; then
    install_dir="$(cd "$install_dir" 2>/dev/null && pwd || echo "$install_dir")"
    codex_dir="$install_dir/.codex"
    refuse_if_symlink "$codex_dir"
  elif [ "$scope" = "user" ]; then
    codex_dir="$HOME/.codex"
  else
    # Project scope: same prompt + git-root fallback as install.sh.
    project_dir=""
    if [ "$scope_explicit" = "false" ] && [ -r /dev/tty ]; then
      default_project_dir="$(pwd)"
      printf "Project directory to uninstall from [default: %s]: " "$default_project_dir"
      read -r project_dir_choice </dev/tty || project_dir_choice=""
      project_dir="${project_dir_choice:-$default_project_dir}"
      case "$project_dir" in
        "~")    project_dir="$HOME" ;;
        "~/"*)  project_dir="$HOME/${project_dir#~/}" ;;
      esac
      if [ ! -d "$project_dir" ]; then
        err "Directory does not exist: $project_dir."
        exit 1
      fi
      project_dir="$(cd "$project_dir" && pwd)"
    fi
    if [ -n "${project_dir:-}" ]; then
      codex_dir="$project_dir/.codex"
    else
      if ! git_root="$(git rev-parse --show-toplevel 2>/dev/null)"; then
        err "--scope project must be run inside a git repo, or use --dir <path>."
        exit 1
      fi
      codex_dir="$git_root/.codex"
    fi
    refuse_if_symlink "$codex_dir"
  fi
  codex_config_file="$codex_dir/config.toml"
  if [ "$scope" = "user" ] && [ -z "$install_dir" ]; then
    codex_status_file="$HOME/.weave/codex-status.sh"
  else
    codex_status_file="$codex_dir/weave-status.sh"
  fi
  codex_status_disabled_marker="$(dirname "$codex_status_file")/.weave-router-disabled"
  refuse_if_symlink "$codex_config_file"
  refuse_if_symlink "$codex_status_file"
  refuse_if_symlink "$codex_status_disabled_marker"

  if [ -f "$codex_config_file" ]; then
    strip_codex_block "$codex_config_file"
    # If the file now contains only whitespace/comments, leave it: the user
    # may have other comments worth keeping. Truly empty files we delete so
    # we don't leave a zero-byte artifact behind.
    if [ ! -s "$codex_config_file" ]; then
      rm -f "$codex_config_file"
      ok "Removed empty $codex_config_file"
    else
      ok "Cleaned $codex_config_file"
    fi
  else
    info "No Codex config at $codex_config_file (already uninstalled?)"
  fi

  # A user config with an existing [features] table receives the installer
  # hook setting outside the managed block; remove only our tagged line.
  if [ -f "$codex_config_file" ]; then
    tmp="$(mktemp -t weave-codex-features-uninstall.XXXXXX)"
    awk '$0 != "hooks = true # weave-router managed codex hooks" { print }' "$codex_config_file" >"$tmp"
    mv "$tmp" "$codex_config_file"
  fi

  if [ -f "$codex_status_file" ]; then
    if grep -Fq '<!-- weave-router managed codex status -->' "$codex_status_file"; then
      rm -f "$codex_status_file"
      rm -f "$codex_status_disabled_marker"
      ok "Removed $codex_status_file"
      rmdir "$(dirname "$codex_status_file")" 2>/dev/null || true
    else
      warn "Leaving user-owned Codex status helper at $codex_status_file untouched."
    fi
  fi

  # Remove only the prompt files this installer owns; leave any user-authored
  # entries in prompts/ alone. The dir itself is dropped only if empty after.
  codex_prompts_dir="$codex_dir/prompts"
  if [ -d "$codex_prompts_dir" ]; then
    refuse_if_symlink "$codex_prompts_dir"
    while IFS= read -r cmd; do
      cmd_file="$codex_prompts_dir/$cmd.md"
      if [ -f "$cmd_file" ]; then
        refuse_if_symlink "$cmd_file"
        rm -f "$cmd_file"
        ok "Removed $cmd_file"
      fi
    done <<EOF
$(weave_registry_names codex)
EOF
    rmdir "$codex_prompts_dir" 2>/dev/null || true
  fi

  # disable-routing is handled by the registry-driven loop below, alongside
  # every other installer-owned skill.

  # A symlinked skills/ or per-skill directory would let the marker check and
  # the rm below reach a file outside the Codex tree entirely, so refuse the
  # parents before touching anything beneath them — not just SKILL.md itself.
  if [ -L "$codex_dir/skills" ]; then
    warn "$codex_dir/skills is a symlink; leaving Codex skills untouched."
  else
    while IFS= read -r canonical; do
      skill_dir="$codex_dir/skills/$canonical"
      skill_file="$skill_dir/SKILL.md"
      if [ -L "$skill_dir" ]; then
        warn "$skill_dir is a symlink; leaving it untouched."
        continue
      fi
      if [ -L "$skill_file" ]; then
        warn "$skill_file is a symlink; leaving it untouched."
        continue
      fi
      if [ -f "$skill_file" ]; then
        if grep -Fq "<!-- weave-router managed $canonical skill -->" "$skill_file"; then
          rm -f "$skill_file"
          ok "Removed $skill_file"
          # The prompt skills also ship scripts/emit.sh; leaving it behind
          # strands a directory Codex still advertises as a skill.
          emit_file="$skill_dir/scripts/emit.sh"
          if [ ! -L "$skill_dir/scripts" ] && [ ! -L "$emit_file" ] && [ -f "$emit_file" ]; then
            rm -f "$emit_file"
            rmdir "$skill_dir/scripts" 2>/dev/null || true
          fi
        else
          warn "Leaving user-owned Codex skill at $skill_file untouched."
        fi
        rmdir "$skill_dir" 2>/dev/null || true
      fi
    done <<EOF
$(weave_registry_skill_assets codex)
EOF
    # Drop skills/ when nothing is left in it, matching how the prompts and
    # Claude command paths clean up their now-empty directories. rmdir refuses
    # a non-empty dir, so an unrelated user skill keeps it.
    rmdir "$codex_dir/skills" 2>/dev/null || true
  fi

  if [ -n "$install_dir" ]; then
    ok "Weave Router uninstalled from $install_dir (Codex)."
  else
    ok "Weave Router uninstalled (Codex, scope=$scope)."
  fi
  exit 0
fi

# ---------- claude uninstall path ----------

# Resolve the base directory. When --dir is given, use it directly.
if [ -n "$install_dir" ]; then
  install_dir="$(cd "$install_dir" 2>/dev/null && pwd || echo "$install_dir")"
  settings_file="$install_dir/.claude/settings.json"
  local_settings_file=""
  # --dir alone (scope defaults to "user") uses .weave/; --dir --scope project
  # uses .claude/. Match the installer's scope-dependent statusline placement.
  if [ "$scope" = "project" ]; then
    statusline_file="$install_dir/.claude/cc-statusline.sh"
  else
    statusline_file="$install_dir/.weave/cc-statusline.sh"
  fi
  # Symlink containment: --dir paths come from a user-supplied directory that may
  # be hostile. The later `>` redirect on settings_file and `rm -f` on the
  # statusline script would otherwise follow links out of the directory.
  refuse_if_symlink "$install_dir/.claude"
  refuse_if_symlink "$settings_file"
  refuse_if_symlink "$statusline_file"
elif [ "$scope" = "user" ]; then
  settings_file="$HOME/.claude/settings.json"
  local_settings_file=""
  statusline_file="$HOME/.weave/cc-statusline.sh"
else
  # Project scope without --dir: mirror install.sh — directory prompt only when
  # scope_explicit is false (interactive install path); explicit --scope project
  # uses the git root of CWD with no prompt.
  project_dir=""
  if [ "$scope_explicit" = "false" ] && [ -r /dev/tty ]; then
    default_project_dir="$(pwd)"
    printf "Project directory to uninstall from [default: %s]: " "$default_project_dir"
    read -r project_dir_choice </dev/tty || project_dir_choice=""
    project_dir="${project_dir_choice:-$default_project_dir}"
    case "$project_dir" in
      "~")    project_dir="$HOME" ;;
      "~/"*)  project_dir="$HOME/${project_dir#~/}" ;;
    esac
    if [ ! -d "$project_dir" ]; then
      err "Directory does not exist: $project_dir."
      exit 1
    fi
    project_dir="$(cd "$project_dir" && pwd)"
  fi
  if [ -n "${project_dir:-}" ]; then
    settings_base="$project_dir"
  else
    if ! git_root="$(git rev-parse --show-toplevel 2>/dev/null)"; then
      err "--scope project must be run inside a git repo, or use --dir <path>."
      exit 1
    fi
    settings_base="$git_root"
  fi
  settings_file="$settings_base/.claude/settings.json"
  local_settings_file="$settings_base/.claude/settings.local.json"
  statusline_file="$settings_base/.claude/cc-statusline.sh"
  # Symlink containment: paths come from a git repo or user-supplied directory
  # that may be hostile. The later `>` redirect on settings_file and `rm -f` on
  # the scripts would otherwise follow links out of the repo.
  refuse_if_symlink "$settings_base/.claude"
  refuse_if_symlink "$settings_file"
  refuse_if_symlink "$local_settings_file"
  refuse_if_symlink "$statusline_file"
fi

if [ -f "$settings_file" ]; then
  # Only remove keys we actually installed: scrub our env vars, and only
  # delete `statusLine` / `apiKeyHelper` when they point at scripts this
  # installer used in older versions. Otherwise an unrelated user-configured
  # statusLine or apiKeyHelper would be silently clobbered.
  cleaned="$(jq '
    if .env then
      .env |= (del(.ANTHROPIC_BASE_URL, .ANTHROPIC_AUTH_TOKEN, .ANTHROPIC_CUSTOM_HEADERS, .ENABLE_TOOL_SEARCH))
      | (if (.env | length) == 0 then del(.env) else . end)
    else . end
    | (if (.statusLine.command // "" | tostring | endswith("cc-statusline.sh"))
         then del(.statusLine) else . end)
    | (if (.apiKeyHelper // "" | tostring | endswith("weave-key.sh"))
         then del(.apiKeyHelper) else . end)
    | (if ((.attribution.commit == "Co-Authored-By: Weave Router <router@workweave.ai>"
              or .attribution.commit == "Co-Authored-By: Weave Router <noreply@workweave.ai>")
              and .attribution.pr == "🤖 Generated with [Weave Router](https://router.workweave.ai)")
         then del(.attribution) else . end)
  ' "$settings_file")"
  printf '%s\n' "$cleaned" >"$settings_file"
  ok "Cleaned $settings_file"
else
  info "No settings file at $settings_file (already uninstalled?)"
fi

if [ -n "$local_settings_file" ] && [ -f "$local_settings_file" ]; then
  # ANTHROPIC_BASE_URL only lives here when a project install was toggled off
  # (the off path overrides it to Anthropic in the local file). Project installs
  # also keep WEAVE_ROUTER_BASE_URL here as a private endpoint marker beside the
  # router key; scrub both so uninstall fully reverts the local config.
  cleaned="$(jq '
    if .env then
      .env |= (del(.ANTHROPIC_BASE_URL, .ANTHROPIC_AUTH_TOKEN, .ANTHROPIC_CUSTOM_HEADERS, .ENABLE_TOOL_SEARCH, .WEAVE_ROUTER_BASE_URL))
      | (if (.env | length) == 0 then del(.env) else . end)
    else . end
    | del(.apiKeyHelper)
  ' "$local_settings_file")"
  printf '%s\n' "$cleaned" >"$local_settings_file"
  ok "Cleaned $local_settings_file"
fi

# Drop the toggle parked sidecar (carries the router key header when off).
parked_file="$(dirname "$settings_file")/.weave-parked.json"
if [ -f "$parked_file" ]; then
  refuse_if_symlink "$parked_file"
  rm -f "$parked_file"
  ok "Removed $parked_file"
fi

if [ -f "$statusline_file" ]; then
  rm -f "$statusline_file"
  ok "Removed $statusline_file"
fi

# Remove only the slash command files this installer owns; leave any other
# files in commands/ alone. The directory itself stays if it still contains
# unrelated user commands.
commands_dir="$(dirname "$settings_file")/commands"
if [ -d "$commands_dir" ]; then
  refuse_if_symlink "$commands_dir"
  while IFS= read -r cmd; do
    cmd_file="$commands_dir/$cmd.md"
    if [ -f "$cmd_file" ]; then
      refuse_if_symlink "$cmd_file"
      if grep -Fq "<!-- weave-router managed command: $cmd -->" "$cmd_file"; then
        rm -f "$cmd_file"
        ok "Removed $cmd_file"
      else
        warn "Leaving user-owned Claude command at $cmd_file untouched."
      fi
    fi
  done <<EOF
$(weave_registry_names claude)
EOF
  # Clean up the dir only if we left nothing behind.
  rmdir "$commands_dir" 2>/dev/null || true
fi

if [ -n "$install_dir" ]; then
  ok "Weave Router uninstalled from $install_dir."
else
  ok "Weave Router uninstalled (scope=$scope)."
fi
