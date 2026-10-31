#!/usr/bin/env bash
# Refreshes the bundled Codex catalog that /v1/codex/models serves to API-key logins.
set -euo pipefail

version="${1:?usage: $0 <codex-version>}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
codex_home="$(mktemp -d)"
trap 'rm -rf "$codex_home"' EXIT
CODEX_HOME="$codex_home" npx --yes "@openai/codex@$version" debug models --bundled \
  | jq '.' >"$root/internal/proxy/codex_bundled_models.json.tmp"
jq -e '.models | length > 0' "$root/internal/proxy/codex_bundled_models.json.tmp" >/dev/null
mv "$root/internal/proxy/codex_bundled_models.json.tmp" "$root/internal/proxy/codex_bundled_models.json"
