#!/usr/bin/env bash
# Prevent newly unreachable functions while allowing the reviewed debt baseline
# to tighten as existing entries are removed.
set -euo pipefail

export LC_ALL=C

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
baseline="${DEADCODE_RATCHET_BASELINE:-${repo_root}/.deadcode-baseline.txt}"
analyzer_version="v0.30.0"

usage() {
	cat <<'EOF'
Usage: scripts/deadcode-ratchet.sh [--update | --compare <baseline> <candidate>]

--update deliberately replaces the reviewed baseline with the current analyzer output.
--compare compares normalized file<TAB>symbol identity files without running the analyzer.
EOF
}

run_analyzer() {
	if [[ -n "${DEADCODE_RATCHET_ANALYZER:-}" ]]; then
		"${DEADCODE_RATCHET_ANALYZER}" "$@"
		return
	fi
	go run "golang.org/x/tools/cmd/deadcode@${analyzer_version}" "$@"
}

normalize() {
	local raw="$1"
	local output="$2"
	local pattern='^.+:[0-9]+:[0-9]+: unreachable func: .+$'
	if grep -qvE "${pattern}" "${raw}"; then
		printf 'deadcode emitted unrecognized output; refusing an incomplete comparison:\n' >&2
		grep -vE "${pattern}" "${raw}" >&2
		return 1
	fi
	sed -nE 's#^(.+):[0-9]+:[0-9]+: unreachable func: (.+)$#\1\t\2#p' "${raw}" |
		tr '\134' '/' | sort -u >"${output}"
}

normalize_eol() {
	sed 's/\r$//' "$1" >"$2"
}

compare() {
	local old="$1"
	local new="$2"
	local old_normalized new_normalized additions removals result=0
	if [[ ! -f "${old}" ]]; then
		printf 'missing %s; run scripts/deadcode-ratchet.sh --update deliberately\n' "${old}" >&2
		return 1
	fi
	old_normalized="$(mktemp)"
	new_normalized="$(mktemp)"
	normalize_eol "${old}" "${old_normalized}"
	normalize_eol "${new}" "${new_normalized}"
	if ! sort -cu "${old_normalized}"; then
		printf '%s must contain sorted, unique file<TAB>symbol identities\n' "${old}" >&2
		rm -f "${old_normalized}" "${new_normalized}"
		return 1
	fi
	if ! sort -cu "${new_normalized}"; then
		printf '%s must contain sorted, unique file<TAB>symbol identities\n' "${new}" >&2
		rm -f "${old_normalized}" "${new_normalized}"
		return 1
	fi
	additions="$(mktemp)"
	removals="$(mktemp)"
	comm -13 "${old_normalized}" "${new_normalized}" >"${additions}"
	comm -23 "${old_normalized}" "${new_normalized}" >"${removals}"
	if [[ -s "${additions}" ]]; then
		printf 'NEW UNREACHABLE FUNCTIONS (update reachability or review a baseline change):\n' >&2
		cat "${additions}" >&2
		result=1
	elif [[ -s "${removals}" ]]; then
		printf 'dead-code debt tightened: %s baseline entries were removed; review and update .deadcode-baseline.txt deliberately.\n' "$(wc -l <"${removals}" | tr -d ' ')"
	fi
	if [[ "${result}" -eq 0 ]]; then
		printf 'no newly unreachable functions\n'
	fi
	rm -f "${old_normalized}" "${new_normalized}" "${additions}" "${removals}"
	return "${result}"
}

analyze_and_compare() {
	local raw candidate
	raw="$(mktemp)"
	candidate="$(mktemp)"
	trap 'rm -f "${raw}" "${candidate}"' RETURN
	if ! run_analyzer ./... >"${raw}"; then
		printf 'deadcode analyzer failed; refusing a vacuous comparison\n' >&2
		return 1
	fi
	normalize "${raw}" "${candidate}"
	compare "${baseline}" "${candidate}"
}

cd "${repo_root}"
case "${1:-}" in
	--update)
		if [[ $# -ne 1 ]]; then usage >&2; exit 2; fi
		raw="$(mktemp)"
		trap 'rm -f "${raw}"' EXIT
		run_analyzer ./... >"${raw}"
		normalize "${raw}" "${baseline}"
		printf 'updated %s with deadcode %s output\n' "${baseline}" "${analyzer_version}"
		;;
	--compare)
		if [[ $# -ne 3 ]]; then usage >&2; exit 2; fi
		compare "$2" "$3"
		;;
	"")
		analyze_and_compare
		;;
	*)
		usage >&2
		exit 2
		;;
esac
