#!/usr/bin/env bash
# Detect statistically significant store-benchmark regressions. The default
# comparison uses the reviewed baseline; --against compares two revisions on
# one machine, which is the mode used by CI. --bootstrap validates the current
# benchmark suite against the reviewed baseline without comparing host-specific
# timing data.
set -euo pipefail

export LC_ALL=C

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
baseline="${repo_root}/.perf-baseline.txt"
count="${PERF_RATCHET_COUNT:-10}"
threshold="${PERF_RATCHET_THRESHOLD:-25}"
benches="${PERF_RATCHET_BENCHES:-^(Benchmark(Search|SearchContext)_|BenchmarkScanProject_Page5000$)}"
pkg="${PERF_RATCHET_PKG:-./internal/store}"
benchstat_version="v0.0.0-20250909190841-7e13e04d9366"
allow_bootstrap="${PERF_RATCHET_BOOTSTRAP:-0}"

usage() {
	cat <<'EOF'
Usage: scripts/perf-ratchet.sh [--update | --bootstrap | --against <git-ref> | --compare <baseline> <candidate>]

Environment:
  PERF_RATCHET_COUNT       benchmark samples per case (default: 10)
  PERF_RATCHET_THRESHOLD   allowed significant slowdown percentage (default: 25)
  PERF_RATCHET_BENCHES     go test -bench expression
  PERF_RATCHET_PKG         package to benchmark (default: ./internal/store)
  PERF_RATCHET_BOOTSTRAP   allow --against to fall back only when its reference
                           has a strict subset of the current benchmark suite
EOF
}

if ! [[ "${count}" =~ ^[1-9][0-9]*$ ]]; then
	printf 'PERF_RATCHET_COUNT must be a positive integer, got %q\n' "${count}" >&2
	exit 2
fi
if ! awk -v value="${threshold}" 'BEGIN { exit !(value ~ /^[0-9]+([.][0-9]+)?$/) }'; then
	printf 'PERF_RATCHET_THRESHOLD must be a non-negative number, got %q\n' "${threshold}" >&2
	exit 2
fi
if [[ "${allow_bootstrap}" != "0" && "${allow_bootstrap}" != "1" ]]; then
	printf 'PERF_RATCHET_BOOTSTRAP must be 0 or 1, got %q\n' "${allow_bootstrap}" >&2
	exit 2
fi

run_benchstat() {
	if [[ -n "${PERF_RATCHET_BENCHSTAT:-}" ]]; then
		"${PERF_RATCHET_BENCHSTAT}" "$@"
		return
	fi
	go run "golang.org/x/perf/cmd/benchstat@${benchstat_version}" "$@"
}

run_benches() {
	local output="$1"
	local workdir="$2"
	(
		cd "${workdir}"
		go test -run '^$' -bench "${benches}" -benchtime=1s -count "${count}" "${pkg}" >"${output}"
	)
}

normalize_benchfmt() {
	# Package path is intentionally the only normalized configuration. A module
	# path migration must not create separate benchstat tables, while changes to
	# architecture, OS, CPU, or any other metadata must remain visible.
	sed -E 's|^pkg: github.com/Gentleman-Programming/engram(/v2)?/internal/store$|pkg: github.com/Gentleman-Programming/engram/v2/internal/store|' "$1" >"$2"
}

benchmark_names() {
	awk '/^Benchmark/ { name = $1; sub(/-[0-9]+$/, "", name); sub(/^Benchmark/, "", name); print name }' "$1" | sort -u
}

config_headers() {
	awk '/^[[:alnum:]_.-]+: / { print }' "$1" | sort -u
}

require_matching_inputs() {
	local old="$1"
	local new="$2"
	local old_names new_names old_headers new_headers
	local result=0
	old_names="$(mktemp)"
	new_names="$(mktemp)"
	old_headers="$(mktemp)"
	new_headers="$(mktemp)"

	if [[ ! -s "${old}" || ! -s "${new}" ]]; then
		printf 'perf ratchet requires non-empty baseline and candidate benchmark output\n' >&2
		result=1
	elif ! benchmark_names "${old}" >"${old_names}" || ! benchmark_names "${new}" >"${new_names}" || [[ ! -s "${old_names}" || ! -s "${new_names}" ]]; then
		printf 'perf ratchet found no benchmark rows in baseline or candidate output\n' >&2
		result=1
	elif ! cmp -s "${old_names}" "${new_names}"; then
		printf 'perf ratchet benchmark sets do not match; refusing a vacuous comparison\n' >&2
		diff -u "${old_names}" "${new_names}" >&2 || true
		result=1
	elif ! config_headers "${old}" >"${old_headers}" || ! config_headers "${new}" >"${new_headers}" || ! cmp -s "${old_headers}" "${new_headers}"; then
		printf 'perf ratchet benchmark configurations do not match after package normalization; refusing separate benchstat tables\n' >&2
		diff -u "${old_headers}" "${new_headers}" >&2 || true
		result=1
	fi
	rm -f "${old_names}" "${new_names}" "${old_headers}" "${new_headers}"
	return "${result}"
}

require_paired_report() {
	local expected="$1"
	local report="$2"
	local paired
	paired="$(mktemp)"
	awk '/\(p=/ { name = $1; sub(/-[0-9]+$/, "", name); sub(/^Benchmark/, "", name); print name }' "${report}" | sort -u >"${paired}"
	if ! cmp -s "${expected}" "${paired}"; then
		printf 'benchstat report does not pair every expected benchmark; refusing a partial or vacuous comparison\n' >&2
		diff -u "${expected}" "${paired}" >&2 || true
		rm -f "${paired}"
		return 1
	fi
	rm -f "${paired}"
}

compare() {
	local old_raw="$1"
	local new_raw="$2"
	local old new report expected verdict
	old="$(mktemp)"
	new="$(mktemp)"
	report="$(mktemp)"
	expected="$(mktemp)"
	normalize_benchfmt "${old_raw}" "${old}"
	normalize_benchfmt "${new_raw}" "${new}"
	if ! require_matching_inputs "${old}" "${new}"; then
		rm -f "${old}" "${new}" "${report}" "${expected}"
		return 1
	fi
	benchmark_names "${old}" >"${expected}"

	if ! run_benchstat "${old}" "${new}" >"${report}"; then
		printf 'benchstat failed while comparing benchmark output:\n' >&2
		cat "${report}" >&2
		rm -f "${old}" "${new}" "${report}" "${expected}"
		return 1
	fi
	if ! require_paired_report "${expected}" "${report}"; then
		rm -f "${old}" "${new}" "${report}" "${expected}"
		return 1
	fi
	if verdict="$(awk -v threshold="${threshold}" '
		/\(p=/ {
			matched++
			name = $1
			delta = ""
			p = ""
			for (i = 2; i <= NF; i++) {
				if ($i ~ /^\+[0-9][0-9.]*%$/) delta = $i
				if ($i ~ /^\(p=/) p = $i
			}
			if (delta != "" && p != "") {
				gsub(/^\+|%$/, "", delta)
				gsub(/^\(p=|\)$/, "", p)
				if (delta + 0 > threshold && p + 0 <= 0.05) {
					printf "  %-48s +%s%% (p=%s)\n", name, delta, p
					regressed = 1
				}
			}
		}
		END { exit regressed ? 1 : 0 }
	' "${report}")"; then
		:
	else
		case $? in
			1)
				printf 'PERFORMANCE REGRESSIONS beyond +%s%%:\n%s\n' "${threshold}" "${verdict}" >&2
				rm -f "${old}" "${new}" "${report}" "${expected}"
				return 1
				;;
		esac
	fi
	printf 'no statistically significant performance regression beyond +%s%%\n' "${threshold}"
	rm -f "${old}" "${new}" "${report}" "${expected}"
}

validate_bootstrap_candidate() {
	local candidate="$1"
	local source="$2"
	local baseline_names candidate_names
	baseline_names="$(mktemp)"
	candidate_names="$(mktemp)"
	if [[ ! -s "${baseline}" || ! -s "${candidate}" ]]; then
		printf 'bootstrap requires non-empty versioned baseline and candidate benchmark output\n' >&2
		rm -f "${baseline_names}" "${candidate_names}"
		return 1
	fi
	benchmark_names "${baseline}" >"${baseline_names}"
	benchmark_names "${candidate}" >"${candidate_names}"
	if [[ ! -s "${baseline_names}" || ! -s "${candidate_names}" ]]; then
		printf 'bootstrap found no benchmark rows in the versioned baseline or candidate output\n' >&2
		rm -f "${baseline_names}" "${candidate_names}"
		return 1
	fi
	if ! cmp -s "${baseline_names}" "${candidate_names}"; then
		printf 'bootstrap benchmark suite does not match the versioned baseline\n' >&2
		diff -u "${baseline_names}" "${candidate_names}" >&2 || true
		rm -f "${baseline_names}" "${candidate_names}"
		return 1
	fi
	rm -f "${baseline_names}" "${candidate_names}"
	printf 'bootstrap: %s lacks the complete benchmark suite; validated its successor against the versioned baseline and intentionally skipped cross-host timing comparison\n' "${source}"
}

reference_needs_bootstrap() {
	local old="$1"
	local new="$2"
	local old_names new_names
	old_names="$(mktemp)"
	new_names="$(mktemp)"
	benchmark_names "${old}" >"${old_names}"
	benchmark_names "${new}" >"${new_names}"
	if [[ ! -s "${new_names}" ]]; then
		printf 'perf ratchet found no benchmark rows in candidate output\n' >&2
		rm -f "${old_names}" "${new_names}"
		return 2
	fi
	if [[ -s "${old_names}" ]] && comm -23 "${old_names}" "${new_names}" | grep -q .; then
		rm -f "${old_names}" "${new_names}"
		return 1
	fi
	if comm -13 "${old_names}" "${new_names}" | grep -q .; then
		rm -f "${old_names}" "${new_names}"
		return 0
	fi
	rm -f "${old_names}" "${new_names}"
	return 1
}

cd "${repo_root}"
case "${1:-}" in
	--update)
		if [[ $# -ne 1 ]]; then usage >&2; exit 2; fi
		raw="$(mktemp)"
		trap 'rm -f "${raw}"' EXIT
		run_benches "${raw}" "${repo_root}"
		normalize_benchfmt "${raw}" "${baseline}"
		printf 'updated %s with %s samples per benchmark\n' "${baseline}" "${count}"
		;;
	--against)
		if [[ $# -ne 2 ]]; then usage >&2; exit 2; fi
		reference_tree="$(mktemp -d "${repo_root}/.perf-ratchet-XXXXXX")"
		old="$(mktemp)"
		new="$(mktemp)"
		cleanup() {
			git worktree remove --force "${reference_tree}" >/dev/null 2>&1 || rm -rf "${reference_tree}"
			rm -f "${old}" "${new}"
		}
		trap cleanup EXIT
		rmdir "${reference_tree}"
		git worktree add --detach "${reference_tree}" "$2" >/dev/null
		run_benches "${old}" "${reference_tree}"
		run_benches "${new}" "${repo_root}"
		if [[ "${allow_bootstrap}" == "1" ]] && reference_needs_bootstrap "${old}" "${new}"; then
			validate_bootstrap_candidate "${new}" "reference ${2}"
			exit $?
		fi
		compare "${old}" "${new}"
		;;
	--bootstrap)
		if [[ $# -ne 1 ]]; then usage >&2; exit 2; fi
		candidate="$(mktemp)"
		trap 'rm -f "${candidate}"' EXIT
		run_benches "${candidate}" "${repo_root}"
		validate_bootstrap_candidate "${candidate}" "the push event has no previous revision"
		;;
	--compare)
		if [[ $# -ne 3 ]]; then usage >&2; exit 2; fi
		compare "$2" "$3"
		;;
	"")
		if [[ ! -f "${baseline}" ]]; then
			printf 'missing %s; run scripts/perf-ratchet.sh --update deliberately\n' "${baseline}" >&2
			exit 1
		fi
		candidate="$(mktemp)"
		trap 'rm -f "${candidate}"' EXIT
		run_benches "${candidate}" "${repo_root}"
		compare "${baseline}" "${candidate}"
		;;
	*)
		usage >&2
		exit 2
		;;
esac
