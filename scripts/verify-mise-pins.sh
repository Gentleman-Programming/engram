#!/usr/bin/env bash
# Verify mise.toml's go and node pins stay in lockstep with every other version
# source: go.mod's `go` directive, every `go-version:` across ci.yml and
# release.yml, and publish-pi.yml's `node-version`.
#
# mise.toml is a *third*, independent pin -- nothing else reads it. CI still
# provisions Go and Node from the workflow literals. This guard is the only
# thing keeping the extra pin honest as the others evolve.
#
# Fails closed: a missing pin site, an internally inconsistent one, or an
# unexpected duplicate is an error, never a silent skip.
set -euo pipefail

die() {
  printf 'mise pins: %s\n' "$*" >&2
  exit 1
}

[[ $# -eq 0 ]] || die "takes no arguments"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "${repo_root}"

go_mod="go.mod"
ci_yml=".github/workflows/ci.yml"
release_yml=".github/workflows/release.yml"
publish_pi_yml=".github/workflows/publish-pi.yml"
mise_toml="mise.toml"

for f in "${go_mod}" "${ci_yml}" "${release_yml}" "${publish_pi_yml}" "${mise_toml}"; do
  [[ -f "${f}" ]] || die "missing ${f}"
done

# extract_one prints the single line matching an anchored pattern, or dies -- on
# zero matches (nothing to compare against) or on more than one (an ambiguous
# source; guessing which is authoritative is worse than no guard at all).
extract_one() {
  local description="$1" file="$2" pattern="$3"
  local -a matches
  mapfile -t matches < <(grep -E "${pattern}" "${file}" || true)
  case "${#matches[@]}" in
    1) ;;
    0) die "no ${description} found in ${file}" ;;
    *) die "expected exactly one ${description} in ${file}, found ${#matches[@]} -- update it so only one remains authoritative" ;;
  esac
  printf '%s\n' "${matches[0]}"
}

# extract_agreed prints the one value shared by EVERY match across one or more
# files. Unlike extract_one, repeated occurrences are legitimate here -- ci.yml
# pins go-version once per job -- but they must all agree. Zero matches in any
# listed file is an error, so deleting a pin site fails instead of shrinking the
# comparison set. key_pattern matches the KEY only -- never the value syntax --
# so a same-key line in an unsupported format is still counted (and then dies
# on empty extraction below) instead of silently vanishing from the comparison.
extract_agreed() {
  local description="$1" key_pattern="$2" extract="$3"
  shift 3
  local file entry lineno value agreed="" agreed_at="" found=0
  local -a matches
  for file in "$@"; do
    mapfile -t matches < <(grep -nE "${key_pattern}" "${file}" || true)
    [[ "${#matches[@]}" -gt 0 ]] || die "no ${description} found in ${file}"
    for entry in "${matches[@]}"; do
      lineno="${entry%%:*}"
      value="$(sed -nE "${extract}" <<<"${entry#*:}")"
      [[ -n "${value}" ]] || die "unsupported ${description} format in ${file}:${lineno}: ${entry#*:}"
      if [[ "${found}" -eq 0 ]]; then
        agreed="${value}"
        agreed_at="${file}:${lineno}"
        found=1
        continue
      fi
      [[ "${value}" == "${agreed}" ]] || die "${description} drift -- ${agreed_at} pins \"${agreed}\", ${file}:${lineno} pins \"${value}\". Every ${description} must be identical."
    done
  done
  printf '%s\n' "${agreed}"
}

go_mod_line="$(extract_one "go directive" "${go_mod}" '^go[[:space:]]')"
go_mod_pin="$(sed -nE 's/^go[[:space:]]+([0-9][^[:space:]]*).*$/\1/p' <<<"${go_mod_line}")"
[[ -n "${go_mod_pin}" ]] || die "unsupported go directive format in ${go_mod}: ${go_mod_line}"

workflow_go_pin="$(extract_agreed "go-version pin" \
  '^[[:space:]]*go-version:' \
  's/^[[:space:]]*go-version: "([0-9][^"]*)".*$/\1/p' \
  "${ci_yml}" "${release_yml}")"

pi_node_line="$(extract_one "node-version key" "${publish_pi_yml}" '^[[:space:]]*node-version:')"
pi_node_pin="$(sed -nE 's/^[[:space:]]*node-version: "([0-9][^"]*)".*$/\1/p' <<<"${pi_node_line}")"
[[ -n "${pi_node_pin}" ]] || die "unsupported node-version format in ${publish_pi_yml}: ${pi_node_line}"

mise_go_line="$(extract_one "go pin" "${mise_toml}" '^go[[:space:]]*=')"
mise_go_pin="$(sed -nE 's/^go[[:space:]]*=[[:space:]]*"([^"]*)".*$/\1/p' <<<"${mise_go_line}")"
[[ -n "${mise_go_pin}" ]] || die "unsupported go pin format in ${mise_toml}: ${mise_go_line}"

mise_node_line="$(extract_one "node pin" "${mise_toml}" '^node[[:space:]]*=')"
mise_node_pin="$(sed -nE 's/^node[[:space:]]*=[[:space:]]*"([^"]*)".*$/\1/p' <<<"${mise_node_line}")"
[[ -n "${mise_node_pin}" ]] || die "unsupported node pin format in ${mise_toml}: ${mise_node_line}"

status=0

if [[ "${mise_go_pin}" != "${go_mod_pin}" ]]; then
  printf 'mise pins: go drift -- mise.toml pins "%s", go.mod pins "%s". Update mise.toml'\''s go value to match go.mod'\''s go directive.\n' "${mise_go_pin}" "${go_mod_pin}" >&2
  status=1
fi

if [[ "${workflow_go_pin}" != "${go_mod_pin}" ]]; then
  printf 'mise pins: go drift -- ci.yml/release.yml pin "%s", go.mod pins "%s". Update every go-version line to match go.mod'\''s go directive.\n' "${workflow_go_pin}" "${go_mod_pin}" >&2
  status=1
fi

if [[ "${mise_node_pin}" != "${pi_node_pin}" ]]; then
  printf 'mise pins: node drift -- mise.toml pins "%s", publish-pi.yml pins "%s". Update mise.toml'\''s node value to match publish-pi.yml'\''s node-version.\n' "${mise_node_pin}" "${pi_node_pin}" >&2
  status=1
fi

[[ "${status}" -eq 0 ]] || exit 1

printf 'mise pins: go=%s node=%s agree across go.mod, ci.yml, release.yml, publish-pi.yml and mise.toml\n' "${mise_go_pin}" "${mise_node_pin}"
