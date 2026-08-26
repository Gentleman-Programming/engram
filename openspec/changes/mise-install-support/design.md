# Design: mise install support

## Technical Approach

Three independent slices, no shared runtime state:

1. **Pins + guard** — `mise.toml` at the repo root plus `scripts/verify-mise-pins.sh`,
   wired as a step in `ci.yml`'s `unit-tests` job. The guard is the only reader of
   `mise.toml`, so it must fail closed on every missing, duplicated, or disagreeing
   pin site (REQ-MISE-001..003).
2. **Detector** — a new `internal/version/mise.go` in the same package as its only
   consumer, resolving mise's installs root from env precedence and answering whether
   the running binary lives under it (REQ-MISE-004).
3. **Hint + docs** — a mise branch at the top of `updateInstructions()`
   (`internal/version/check.go:150`) and a mise section in `docs/INSTALLATION.md`
   (REQ-MISE-005..006).

No package boundary moves: `internal/version` keeps owning update-check logic and
`cmd/engram` stays a thin stderr printer (`main.go:713`).

## Architecture Decisions

| Decision | Choice | Alternatives considered | Rationale |
|----------|--------|-------------------------|-----------|
| Documented install form | `mise use -g github:Gentleman-Programming/engram@latest` | `ubi:` backend; short name `engram@latest` | Verified end to end: `github:` resolves the goreleaser archive, checksums it, verifies **GitHub artifact attestations and SLSA provenance**, extracts a working binary. `ubi:` prints a deprecation warning and is removed in mise 2027.1.0. The short name needs the pending registry PRs, so docs note it as "once the registry entry lands". |
| Containment check | Minimal unexported `os.SameFile` walk, colocated in `internal/version/mise.go` | Port gentle-ai's whole `pathidentity` package; `filepath.Clean` + string prefix | Prefix matching is wrong exactly where engram ships: case-insensitive APFS/NTFS, symlinked `$HOME`, and Unicode-equivalent names — and needs separator-boundary care so `/x/mise-evil` does not match `/x/mise`. The correct version is ~18 lines and needs no new package. Stakes are low (a wrong hint, never a skipped upgrade), but the cheap option is not cheaper. |
| Detector placement | `internal/version/mise.go`, unexported | New `internal/pathidentity` package | Single consumer in the same package; a package for one predicate earns nothing. Extract later if a second caller appears. |
| Hint ordering | mise check first, then the existing `runtime.GOOS` switch | Add a case inside the switch | Install method is orthogonal to OS — a mise install on darwin must not print `brew upgrade`. A guard clause keeps the GOOS switch byte-identical. |
| Go pin rule | "every occurrence agrees", across `go.mod` + `ci.yml`×2 + `release.yml`×1 | Copy gentle-ai's exactly-one rule | engram legitimately repeats `go-version:` once per job; exactly-one would fail on a clean tree. |
| Node pin rule | Exactly one, in `publish-pi.yml` | `ci.yml` (gentle-ai's source) | `publish-pi.yml:20` is engram's only Node pin; `ci.yml` has none. |

## Data Flow

```
user            cmd/engram              internal/version                env / fs
 │ engram <cmd>     │                          │                            │
 ├─────────────────>│ shouldCheckForUpdates()  │                            │
 │                  ├─ CheckLatest(version) ──>│                            │
 │                  │                          ├─ GET releases/latest       │
 │                  │                          ├─ isNewer? ── no ──> up_to_date (silent)
 │                  │                          │        yes                 │
 │                  │                          ├─ updateInstructions()      │
 │                  │                          │   ├─ runningBinaryIsMiseManaged()
 │                  │                          │   │    ├─ miseInstallsRoot(GOOS) ──> MISE_INSTALLS_DIR
 │                  │                          │   │    │                    ──> MISE_DATA_DIR/installs
 │                  │                          │   │    │                    ──> XDG_DATA_HOME/mise/installs
 │                  │                          │   │    │                    ──> platform default
 │                  │                          │   │    └─ pathContains(root, os.Executable()) ──> os.SameFile
 │                  │                          │   ├─ true  ──> "mise upgrade engram"
 │                  │                          │   └─ false ──> existing GOOS switch (unchanged)
 │<── stderr ───────┤ printUpdateCheckResult() │                            │
```

Every arrow is read-only: no subprocess, no binary mutation, no persisted state.

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `mise.toml` | Create | `go = "1.25.10"`, `node = "24"` |
| `scripts/verify-mise-pins.sh` | Create | Drift guard; first script in the repo (`chmod +x`) |
| `.github/workflows/ci.yml` | Modify | `- name: Verify mise pins` step in `unit-tests`, before `Run unit tests` |
| `internal/version/mise.go` | Create | Installs-root resolution + containment predicate |
| `internal/version/mise_test.go` | Create | Env-precedence table + containment cases |
| `internal/version/check.go` | Modify | Guard clause at the top of `updateInstructions()` (line 150) |
| `docs/INSTALLATION.md` | Modify | mise section (macOS/Linux/Windows); `Go 1.24+` → `Go 1.25.10` at line 167 |
| `README.md` | Modify | One-line mise pointer |

## Interfaces / Contracts

### `internal/version/mise.go`

Adapted from gentle-ai's `internal/update/upgrade/mise.go` (post-CodeRabbit Windows
fix), with `pathidentity.Contains` inlined as an unexported helper.

```go
var currentExecutableFn = os.Executable
var userHomeDirFn = os.UserHomeDir

// miseInstallsRoot follows mise's own precedence. Whitespace-only values are
// unset. goos is a parameter so the Windows branch is testable on Linux CI.
func miseInstallsRoot(goos string) string {
	if root := strings.TrimSpace(os.Getenv("MISE_INSTALLS_DIR")); root != "" {
		return root
	}
	if dataDir := strings.TrimSpace(os.Getenv("MISE_DATA_DIR")); dataDir != "" {
		return filepath.Join(dataDir, "installs")
	}
	if xdgDataHome := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdgDataHome != "" {
		return filepath.Join(xdgDataHome, "mise", "installs")
	}
	if goos == "windows" {
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			return filepath.Join(localAppData, "mise", "installs")
		}
	}
	home, err := userHomeDirFn()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	if goos == "windows" {
		return filepath.Join(home, "AppData", "Local", "mise", "installs")
	}
	return filepath.Join(home, ".local", "share", "mise", "installs")
}

// pathContains reports whether path is root itself or lies beneath it. It climbs
// path's lexical ancestors and asks the OS for directory identity via os.SameFile
// (device+inode) rather than comparing strings, so symlinked ancestors,
// case-insensitive filesystems, and Unicode-equivalent names all answer correctly.
func pathContains(root, path string) bool {
	rootInfo, err := os.Stat(root)
	if err != nil || !rootInfo.IsDir() {
		return false
	}
	current, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for {
		if info, statErr := os.Stat(current); statErr == nil && info.IsDir() && os.SameFile(rootInfo, info) {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}

func runningBinaryIsMiseManaged() bool {
	root := miseInstallsRoot(runtime.GOOS)
	if root == "" {
		return false
	}
	exe, err := currentExecutableFn()
	if err != nil {
		return false
	}
	return pathContains(root, exe)
}
```

### `internal/version/check.go` delta

```go
func updateInstructions() string {
	if runningBinaryIsMiseManaged() {
		return "  mise upgrade engram\n  or: mise upgrade github:Gentleman-Programming/engram"
	}
	switch runtime.GOOS { // unchanged below this line
```

The second line exists because the documented bridge form registers the tool as
`github:Gentleman-Programming/engram`, not `engram` — `mise upgrade engram` only
resolves once the registry entry lands. This satisfies REQ-MISE-005 (the required
string is emitted) using the same `or:` idiom the linux branch already uses; tests
assert with `strings.Contains`, matching `check_test.go`. Drop the second line when
the registry PRs merge.

### `scripts/verify-mise-pins.sh`

House style follows gentle-ai's script (`set -euo pipefail`, `BASH_SOURCE` repo
root, `die()`, fail-closed extraction) — including a fix gentle-ai's own script
only picked up after CodeRabbit caught it there: `extract_one`'s pattern
argument must match the KEY only (e.g. `^[[:space:]]*node-version:`), never the
key *and* an assumed value syntax in one regex. The original gentle-ai script
used a single combined pattern (`^[[:space:]]*node-version: "[0-9]`), so a
second `node-version:` line in an unsupported format (unquoted, or a `${{ }}`
expression) was invisible to the count entirely — "exactly one match" silently
passed against the wrong line instead of catching the duplicate. Engram's
script is written with the fix from day one: `extract_one` counts the key
broadly, then a separate `sed -nE '.../p'` extraction dies explicitly if the
matched line's value doesn't parse. `extract_agreed` is new and handles the Go
case with the same key/value split.

```bash
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

go_mod_line="$(extract_one "go directive" "${go_mod}" '^go [0-9]')"
go_mod_pin="$(awk '{print $2}' <<<"${go_mod_line}")"

workflow_go_pin="$(extract_agreed "go-version pin" \
  '^[[:space:]]*go-version:' \
  's/^[[:space:]]*go-version: "([0-9][^"]*)".*$/\1/p' \
  "${ci_yml}" "${release_yml}")"

pi_node_line="$(extract_one "node-version key" "${publish_pi_yml}" '^[[:space:]]*node-version:')"
pi_node_pin="$(sed -nE 's/^[[:space:]]*node-version: "([0-9][^"]*)".*$/\1/p' <<<"${pi_node_line}")"
[[ -n "${pi_node_pin}" ]] || die "unsupported node-version format in ${publish_pi_yml}: ${pi_node_line}"

mise_go_line="$(extract_one "go pin" "${mise_toml}" '^go = "')"
mise_go_pin="$(sed -E 's/^go = "([^"]*)".*$/\1/' <<<"${mise_go_line}")"

mise_node_line="$(extract_one "node pin" "${mise_toml}" '^node = "')"
mise_node_pin="$(sed -E 's/^node = "([^"]*)".*$/\1/' <<<"${mise_node_line}")"

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
```

Comparing `mise_go_pin` and `workflow_go_pin` each against `go_mod_pin` covers all
three Go sites transitively; `go.mod` is the hub because it is the one source Go
itself reads.

### CI step

```yaml
      - name: Verify mise pins
        run: ./scripts/verify-mise-pins.sh
```

Inserted in `unit-tests` between `Set up Go` (`ci.yml:20-23`) and `Run unit tests`.

## Testing Strategy

`internal/version/mise_test.go` follows `check_test.go`'s idiom: table-driven
subtests, `t.Setenv` for env, and package-var swap with `t.Cleanup` for
`currentExecutableFn` / `userHomeDirFn` (mirroring `withCheckServer`).

| Layer | What to Test | Approach |
|-------|-------------|----------|
| Unit — root precedence | `MISE_INSTALLS_DIR` > `MISE_DATA_DIR/installs` > `XDG_DATA_HOME/mise/installs` > platform default; whitespace-only values fall through; `""` when home is unavailable | Table over `(goos, env, want)` with `t.Setenv`; clear all four vars per case so an inherited `XDG_DATA_HOME` cannot leak |
| Unit — Windows branch | `%LOCALAPPDATA%\mise\installs`, and `~\AppData\Local\mise\installs` fallback | Same table with `goos: "windows"` — `miseInstallsRoot` takes goos as a parameter precisely so ubuntu CI covers this. Compare against `filepath.Join(...)` output, never a hardcoded separator |
| Unit — containment | binary under root → true; root itself → true; sibling `…/mise-evil/engram` → false; symlinked ancestor → true; nonexistent root → false | `t.TempDir()` real dirs + `os.Symlink`; skip the symlink case on Windows |
| Unit — detector wiring | mise-managed → true; `currentExecutableFn` error → false; empty root → false | Swap `currentExecutableFn` with a stub |
| Unit — hint selection | mise-managed → message contains `mise upgrade engram`; non-mise → byte-identical to today's darwin/linux/default strings | Extend `TestUpdateInstructions`; assert with `strings.Contains` |
| Guard — clean tree | Script exits 0 on the real repo | `./scripts/verify-mise-pins.sh` in CI (this is the CI step itself) |
| Guard — fail-closed | drift, missing file, deleted `go-version` line, duplicate disagreeing `node-version` | Manual verification on a scratch copy; document the four commands in the task |

RED first per `strict_tdd`: every unit row above is a failing test before its
production line exists.

## Threat Matrix

`N/A` — no routing, shell-command execution, subprocess, VCS/PR automation,
executable-file classification, or process-integration boundary is introduced.
The detector only *reads* `os.Executable()` and `os.Stat`s directories; it never
executes, resolves, or mutates a binary, and its worst failure is a wrong printed
string. The guard script is CI-invoked, rejects all arguments (`[[ $# -eq 0 ]] ||
die`), and only greps repo-tracked files. Per the matrix reference, no rows are
expanded and no tasks are manufactured.

## Migration / Rollout

No migration. Each of the three slices is independently revertible per the
proposal's rollback plan; `mise.toml` and the guard can ship before the detector
without either depending on the other.

## Open Questions

- [ ] REQ-MISE-005's scenario says `updateInstructions()` "returns
      `mise upgrade engram`". This design returns that line **plus** an
      `or: mise upgrade github:Gentleman-Programming/engram` line, because the
      short name does not resolve until the external registry PRs land. Tests use
      `strings.Contains`. If the spec intends strict equality, the second line must
      be dropped and the pre-registry hint will be wrong for every user who follows
      today's documented install command.
