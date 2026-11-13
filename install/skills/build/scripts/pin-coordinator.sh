#!/usr/bin/env bash
set -euo pipefail
printf ' /force-parent %s\n' "${WEAVE_BUILD_COORDINATOR_MODEL:-claude-opus-5-5}"
