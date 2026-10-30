# CI scanners

Two independent read-only CI jobs run on PRs, merge groups and main pushes:
**Go Vulnerability Check** and **Workflow Validation**. Initial rollout is
non-required, but both jobs fail honestly. The existing six required contexts,
issue approval checks and merge queue remain unchanged. Local evidence is not
hosted execution or required-gate proof.

## Reproduce locally

Use Node.js 24 and Go **1.26.10** (the release/CI toolchain). Install pinned tools
in isolated scratch, not the repository module. These Bash commands run from the
repository root; Go's normal toolchain/module caches may also be populated.

```bash
mkdir -p .repro-2019/bin
printf '*\n' > .repro-2019/.gitignore
export GOBIN="$PWD/.repro-2019/bin"
# Git Bash on Windows requires a native absolute GOBIN:
if command -v cygpath >/dev/null; then export GOBIN="$(cygpath -w "$GOBIN")"; fi
GOTOOLCHAIN=go1.26.10 go install golang.org/x/vuln/cmd/govulncheck@v1.7.0
GOTOOLCHAIN=go1.26.10 go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
```

Put Go 1.26.10's `bin` directory on `PATH` (CI uses `actions/setup-go`), then:

```bash
export GOTOOLCHAIN=local
go version                         # must report go1.26.10
"$GOBIN/govulncheck" -version       # v1.7.0
"$GOBIN/actionlint" -version        # 1.7.12
CI_SCANNER_BIN_DIR="$GOBIN" node --test .github/scripts/ci-scanners.integration.test.mjs
node --test .github/scripts/ci-scanners.test.mjs
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOFLAGS=-mod=readonly \
  bash .github/scripts/ci-scanner.sh govulncheck "$GOBIN/govulncheck" -format=text -scan=symbol ./...
shopt -s nullglob
bash .github/scripts/ci-scanner.sh actionlint "$GOBIN/actionlint" \
  -shellcheck= -pyflakes= .github/workflows/*.yml .github/workflows/*.yaml
```

On Windows, use the installed `.exe` files (Git Bash resolves the commands above).
`CI_SCANNER_BIN_DIR` must be an absolute native path; configured missing tools or
wrong versions fail the integration suite. Unconfigured integration tests explicitly
skip: a policy-only run is not real-scanner evidence. Tests generate dependency-free
local fake-module code and a synthetic v1 OSV database under ignored `.repro-2019/`;
no real vulnerable dependency is downloaded. Fixtures are retained for inspection.
The suite checks clean, called-symbol finding, malformed database, package-load
failure and actionlint clean/malformed/invocation outcomes, including streams and
module immutability on rejection. Its child Go commands use `GOTOOLCHAIN=local`,
offline module settings and read-only module mode. Put the correct Go on `PATH`
rather than relying on automatic toolchain selection during the tests.

The wrapper tests use fake executables to verify stream/status preservation;
they do not establish scanner behavior. The integration suite invokes real CLIs.
Linux-target Go analysis from Windows is not Linux execution. Production scanning
uses default build tags and excludes test code; it does not cover every target or
optional build configuration.

## Interpret results

| Tool | Clean | Findings | Execution/configuration failure |
|---|---|---|---|
| govulncheck, text symbol scan | 0 | 3 | Other nonzero status |
| actionlint | 0 | 1 | 2 (or other unexpected nonzero status) |

The Bash wrapper preserves stdout, stderr and exit status and adds a category on
stderr. Never replace a failure with `|| true`, `continue-on-error`, or a success
claim based only on parseable JSON: govulncheck JSON output does not have the same
finding-exit contract as this text invocation. A broken database or package load
is not a clean security result.

The public vulnerability database is mutable: pinned scanner/toolchain versions
do not pin advisory contents. Record scan date, versions, target and findings.
Review findings individually; do not silently upgrade dependencies or add blanket
suppressions. Any reviewed exception must be explicitly tracked by a maintainer
with scope and rationale; no exceptions are introduced here.

Actionlint's optional shellcheck and pyflakes integrations are explicitly off;
this job validates workflow YAML and Actions semantics, not those language linters.
Existing findings should be reported, not repaired as unrelated rollout work.

## Verification ownership

CI runs the wrapper/policy regressions automatically. The real integration suite
is an explicit local command requiring both installed scanners and Node.js 24;
it is not currently wired into CI. Each scanner job installs only its own tool.
The strict workflow baseline test remains in place; its hash changes only after
checking that the existing workflow is preserved apart from the scanner additions.
Local integration, repository scans and policy reruns are separate evidence;
none establishes hosted fork, merge-group or main-push behavior.

### Initial local baseline

On 2026-10-24, tools built with Go 1.25.10 on Windows/amd64 produced:

- actionlint v1.7.12: all repository workflows clean after correcting the new
  jobs' installation-step environment; exit 0, optional integrations disabled.
- govulncheck v1.7.0: Linux/amd64, CGO disabled, default-tag production scan
  completed with exit 3: 24 reachable findings across two modules and the Go
  standard library. This is not a clean vulnerability result. Seven additional
  imported-package and six required-module findings were reported as not called.

This snapshot depends on public advisory data at scan time and is not a permanent
count. Remediation and any exception decisions require separate review; this
rollout does not change dependencies, suppress findings or activate required gates.
