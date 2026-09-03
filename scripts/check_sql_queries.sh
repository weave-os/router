#!/usr/bin/env bash
set -euo pipefail

repository_root=$(git rev-parse --show-toplevel)
query_directory=${1:-"$repository_root/db/queries"}
if [[ ! -d "$query_directory" ]]; then
  echo "error: SQL query directory does not exist: $query_directory" >&2
  exit 2
fi

findings=$(mktemp)
trap 'rm -f "$findings"' EXIT
perl -ne '
  $line = $_; $line =~ s/--.*$//;
  while ($line =~ /\$[1-9][0-9]*\b/g) { print "$ARGV:$.: numbered parameter $&\n"; }
  while ($line =~ /(@[A-Za-z_][A-Za-z0-9_]*|sqlc\.(?:arg|narg)\(\s*\x27[^\x27]+\x27\s*\))/g) {
    $parameter = $1; $remainder = substr($line, pos($line));
    if ($remainder !~ /^\s*::/) { print "$ARGV:$.: parameter has no explicit type cast: $parameter\n"; }
  }
  close ARGV if eof;
' "$query_directory"/*.sql >> "$findings"
awk '
  FNR == 1 { previous_nonblank = "" }
  /^-- name:/ {
    if (previous_nonblank !~ /^-- / || previous_nonblank ~ /^-- name:/) {
      printf "%s:%d: query declaration needs an explanatory comment\n", FILENAME, FNR
    }
  }
  $0 !~ /^[[:space:]]*$/ { previous_nonblank = $0 }
' "$query_directory"/*.sql >> "$findings"
if [[ -s "$findings" ]]; then LC_ALL=C sort "$findings" >&2; exit 1; fi
echo "SQL query conventions passed"
