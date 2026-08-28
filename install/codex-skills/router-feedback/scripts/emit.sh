#!/usr/bin/env bash
# Print a leading-space /router-feedback directive. Codex runs this via exec;
# the Weave Router intercepts the tool output and records the feedback.
set -euo pipefail
if [ "$#" -eq 0 ] || [ -z "${1:-}" ]; then
  echo "usage: emit.sh <feedback>" >&2
  exit 1
fi
printf ' /router-feedback %s\n' "$*"
