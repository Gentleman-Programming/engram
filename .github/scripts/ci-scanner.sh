#!/usr/bin/env bash
# Preserve scanner output/status while distinguishing findings from tool failures.
set -u
if (( $# < 2 )) || [[ "$1" != govulncheck && "$1" != actionlint ]]; then
  printf '%s\n' 'Usage: bash .github/scripts/ci-scanner.sh {govulncheck|actionlint} EXECUTABLE [ARGS...]' >&2
  exit 2
fi
kind=$1
shift
"$@"
status=$?
category='execution failed'
if (( status == 0 )); then
  category=clean
elif [[ "$kind:$status" == govulncheck:3 || "$kind:$status" == actionlint:1 ]]; then
  category=findings
fi
printf '%s: %s (exit %s)\n' "$kind" "$category" "$status" >&2
exit "$status"
