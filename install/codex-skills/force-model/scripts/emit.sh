#!/usr/bin/env bash
# Print a leading-space /force-model directive. Codex runs this via exec;
# the Weave Router intercepts the tool output and pins the session.
set -euo pipefail
if [ "$#" -eq 0 ] || [ -z "${1:-}" ]; then
  echo "usage: emit.sh <model-id>" >&2
  exit 1
fi
printf ' /force-model %s\n' "$*"
