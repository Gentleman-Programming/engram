# Mise Toolchain Support Specification

## Purpose

Give engram a first-class [mise](https://mise.jdx.dev) install path: a repo-declared toolchain pin, a CI guard that keeps that pin honest against every other version source, and an update hint that tells a mise-managed install to use mise instead of Homebrew or `go install`.

## Requirements

### Requirement: REQ-MISE-001 Repository Toolchain Pins

The system MUST provide a `mise.toml` at the repository root declaring `go = "1.25.10"` and `node = "24"`.

#### Scenario: Pins match authoritative sources

- GIVEN a fresh checkout of the repository
- WHEN `mise.toml` is read
- THEN its `go` value equals `go.mod`'s `go` directive and its `node` value equals `publish-pi.yml`'s `node-version`

### Requirement: REQ-MISE-002 CI Drift Guard Fails Closed

The system MUST provide `scripts/verify-mise-pins.sh`, which MUST fail when any of the following five pin sites disagree with each other, is missing, or has an unexpected duplicate occurrence: `mise.toml`'s `go` pin, `mise.toml`'s `node` pin, `go.mod`'s `go` directive, every `go-version:` occurrence across `ci.yml` and `release.yml` (all MUST be identical), and `publish-pi.yml`'s `node-version` line. The guard MUST pass on a clean tree.

#### Scenario: Clean tree passes

- GIVEN all five pin sites agree with each other
- WHEN `scripts/verify-mise-pins.sh` runs
- THEN it exits successfully with no output indicating drift

#### Scenario: Version drift fails closed

- GIVEN `mise.toml`'s `go` pin differs from `go.mod`'s `go` directive
- WHEN the guard runs
- THEN it exits non-zero and names the disagreeing files and their conflicting values

#### Scenario: Missing pin fails closed

- GIVEN one of the five pin sites cannot be found (e.g. `mise.toml` is absent or a `go-version:` line is deleted from `release.yml`)
- WHEN the guard runs
- THEN it exits non-zero rather than silently skipping that source

#### Scenario: Duplicated pin fails closed

- GIVEN a pin site that expects a single value contains more than one conflicting occurrence (e.g. two `node-version:` lines in `publish-pi.yml` with different values)
- WHEN the guard runs
- THEN it exits non-zero identifying the duplicate

### Requirement: REQ-MISE-003 Drift Guard Runs as a Required CI Check

The drift guard MUST run as a step in `ci.yml`'s `unit-tests` job, making it part of a required check per `CONTRIBUTING.md`.

#### Scenario: Guard runs on every PR

- GIVEN a pull request triggers the `unit-tests` job
- WHEN the job executes
- THEN `scripts/verify-mise-pins.sh` runs as one of its steps

#### Scenario: Guard failure blocks the required check

- GIVEN the guard detects drift
- WHEN the `unit-tests` job runs
- THEN the job fails and the PR's required check fails with it

### Requirement: REQ-MISE-004 Mise-Managed Install Detection

`internal/version` MUST detect whether the running binary lives under a mise-managed installs root, resolved in this order: `$MISE_INSTALLS_DIR`, then `$MISE_DATA_DIR/installs`, then `$XDG_DATA_HOME/mise/installs`, then the platform default (`%LOCALAPPDATA%\mise\installs` on Windows, `~/.local/share/mise/installs` elsewhere). Blank or whitespace-only environment values MUST be treated as unset and fall through to the next source in the order.

#### Scenario: Higher-precedence variable wins

- GIVEN both `MISE_INSTALLS_DIR` and `MISE_DATA_DIR` are set to different paths
- WHEN detection resolves the installs root
- THEN it uses the `MISE_INSTALLS_DIR` value

#### Scenario: Blank value falls through

- GIVEN `MISE_INSTALLS_DIR` is set to an empty or whitespace-only string and `XDG_DATA_HOME` is set
- WHEN detection resolves the installs root
- THEN it skips `MISE_INSTALLS_DIR` and `MISE_DATA_DIR` and uses `$XDG_DATA_HOME/mise/installs`

#### Scenario: Platform default when nothing is set

- GIVEN none of `MISE_INSTALLS_DIR`, `MISE_DATA_DIR`, or `XDG_DATA_HOME` are set
- WHEN detection runs on Windows
- THEN it resolves to `%LOCALAPPDATA%\mise\installs`; on other platforms it resolves to `~/.local/share/mise/installs`

### Requirement: REQ-MISE-005 Update Instructions Reflect Mise-Managed Installs

`updateInstructions()` MUST include the line `mise upgrade engram` when the running binary's path is contained within the resolved mise installs root. Because the short `mise upgrade engram` form only resolves once the tool is registered under the `engram` short name (pending external aqua-registry/jdx-mise registry PRs), the output MAY also include a second line naming the registry-independent form (`mise upgrade github:Gentleman-Programming/engram`) as an alternative. When the running binary is not mise-managed, `updateInstructions()` MUST preserve today's unchanged behavior (Homebrew, `go install`, or fallback instructions by OS).

#### Scenario: Mise-managed binary gets a mise hint

- GIVEN the running binary's path is under the resolved mise installs root
- WHEN `updateInstructions()` is called
- THEN its output includes the line `mise upgrade engram`

#### Scenario: Non-mise binary is unaffected

- GIVEN the running binary's path is not under any resolved mise installs root
- WHEN `updateInstructions()` is called
- THEN it returns the same Homebrew, `go install`, or fallback instructions it returned before this change

### Requirement: REQ-MISE-006 Documentation Coverage

`docs/INSTALLATION.md` MUST document installing engram via mise for macOS, Linux, and Windows, and MUST correct its Requirements section to state `Go 1.25.10` instead of the outdated `Go 1.24+`. `README.md` MUST include a one-line pointer to the mise install method.

#### Scenario: Installation doc covers all three platforms

- GIVEN `docs/INSTALLATION.md`
- WHEN its mise section is read
- THEN it includes install instructions for macOS, Linux, and Windows

#### Scenario: Go version requirement matches go.mod

- GIVEN `docs/INSTALLATION.md`'s Requirements section
- WHEN it is read
- THEN it states `Go 1.25.10`, matching `go.mod`'s `go` directive

#### Scenario: README points to mise

- GIVEN `README.md`
- WHEN its install section is read
- THEN it contains a one-line reference to installing via mise
