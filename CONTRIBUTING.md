# Contributing to Engram

Thanks for contributing. Engram enforces a strict **issue-first workflow** — every change starts with an approved issue.

---

## Contribution Workflow

```
Open Issue → Get status:approved → Open PR → Add type:* label → Review & Merge
```

### Step 1: Open an Issue

Use the correct template:
- **Bug Report** — for bugs
- **Feature Request** — for new features or improvements
- **Documentation Improvement** — for missing, outdated, or unclear docs
- **Tracked Question** — for questions requiring maintainer investigation, a repository change, or a durable decision

> ⚠️ Blank issues are disabled. You must use a template.
> General questions and support belong in [Discussions](https://github.com/Gentleman-Programming/engram/discussions).

Fill in all required fields. Your issue will automatically receive the `status:needs-review` label.

If useful for alignment, search existing issues before opening a new one.

### Step 2: Wait for Approval

A maintainer will review the issue and replace `status:needs-review` with `status:approved` if it's accepted for implementation.

**Do not open a PR until the issue is approved.** Automated checks will block PRs that reference unapproved issues.

### Step 3: Open a Pull Request

Once the issue is approved:

1. Fork the repo and create a branch from `main`
2. Implement your change
3. Open a PR using the PR template — **link the approved issue** with `Closes #N`
4. Add exactly **one `type:*` label** to the PR (see label system below)

### Step 4: Automated PR Checks

The active required contexts run automatically on every PR and merge queue group:

| Check | What it verifies |
|-------|-----------------|
| **Check Issue Reference** | PR body contains `Closes #N`, `Fixes #N`, or `Resolves #N` |
| **Check Issue Has status:approved** | The linked issue has the `status:approved` label |
| **Check PR Has type:* Label** | PR labels use the canonical vocabulary and cardinality |
| **Unit Tests** | `go test ./...` — all tests except those tagged with `//go:build e2e`; runs `make deadcode-check` to reject newly unreachable functions |
| **E2E Tests** | `go test -tags e2e ./internal/server/...` — end-to-end integration tests |
| **Plugin Tests** | The Pi plugin test suite runs from a clean checkout |

PR Validation runs on PR opening, edits, label addition/removal, synchronization,
and reopening. Reopening requests a fresh **Check Issue Reference** and
**Check Issue Has status:approved** evaluation under the existing rules.
Changes to labels on a linked issue do not themselves trigger PR Validation;
the approval check fetches issue labels when the workflow runs. PR label events
and linked-issue label events are distinct.

All required checks must pass before a PR can be merged.

> **Repo admin note:** The active `main` ruleset requires exactly these six contexts: `E2E Tests`, `Unit Tests`, `Plugin Tests`, `Check Issue Has status:approved`, `Check Issue Reference`, and `Check PR Has type:* Label`.

Non-required PR checks include the Claude plugin version guard, lint, Windows setup and wrapper coverage, transient-artifact validation, **Policy Helper Tests** (the merge-queue, label-policy, transient-artifacts, and PR-size-notice script suites), and **Obsidian Build** (tests, typecheck, and build after dependency installation). Policy Helper Tests, Obsidian Build, and the Claude plugin version guard also run on merge groups; lint, Windows checks, and transient-artifact validation do not run as merge-group CI jobs. None of these are active `main` required contexts; a maintainer must explicitly add the Claude plugin version guard to the ruleset before it becomes required.

### Merge Queue Activation (administrators)

The `main` merge queue is already active. Contributors submit eligible PRs to the
queue; the six required contexts above also run on each merge group.

The effective queue rule was observed on **2026-10-09** with these settings:

| Setting | Observed value |
|---------|----------------|
| Grouping strategy | `ALLGREEN` |
| Merge method | `SQUASH` |
| Check response timeout | 60 minutes |
| Maximum entries to build (`max_entries_to_build`) | 5 |
| Minimum entries to merge (`min_entries_to_merge`) | 1 |
| Maximum entries to merge (`max_entries_to_merge`) | 5 |
| Minimum-group wait (`min_entries_to_merge_wait_minutes`) | 5 minutes |

This is an observed configuration snapshot, not a recommendation to change
settings or a guarantee that they remain unchanged. Consult the
[effective `main` rules](https://api.github.com/repos/Gentleman-Programming/engram/rules/branches/main)
for current settings; the [audit in #1736](https://github.com/Gentleman-Programming/engram/issues/1736#issuecomment-6089006647)
records the supporting evidence.

Any queue-policy change requires explicit owner/admin approval and an authorized
administrator. If rollback is approved, disable or remove only the queue rule;
retain the existing required contexts and unrelated rules. This documentation
correction does not authorize settings changes.

## Advisory AI Review

CodeRabbit uses the versioned root [`.coderabbit.yaml`](.coderabbit.yaml). Its
baseline is the [resolved Repository UI configuration](https://github.com/Gentleman-Programming/engram/pull/1728#issuecomment-6084792433);
existing path instructions, generated-file exclusions and other settings are
preserved. The only noise adjustments are the balanced `chill` profile and
turning off the numeric docstring coverage warning. Documentation correctness
still matters and remains part of review.

CodeRabbit does not automatically approve PRs (`request_changes_workflow: false`),
skips drafts, and does not replace human review or the required checks above.
Generated outputs are excluded only from AI review, not from generation checks.
Existing manually requested code-generation/fix features remain unchanged.

Changes to this file require normal issue-first review. Run `go test . -run
TestCodeRabbit -count=1` to check YAML parsing, the two adjustments, and baseline
preservation. Deliberate future settings changes must update the baseline test
with their rationale. To pause automatic reviews on one PR, comment
`@coderabbitai pause`; to disable them through configuration, change
`reviews.auto_review.enabled` to `false` in a reviewed PR. Reverting the initial
configuration-file addition restores selection of the existing UI configuration,
provided that UI configuration has not changed.

Installation permissions could not be audited with the available credentials.
Live review behavior and noise reduction must be observed after rollout; local
policy tests do not establish those results.

## Claude Plugin Version Rule

When changing any file under `plugin/claude-code/` (including renames or deletions), increase the semantic version in both `plugin/claude-code/.claude-plugin/plugin.json` and the `engram` entry of `.claude-plugin/marketplace.json` to the same higher version. The PR and merge queue guard compare against the event's base commit; malformed, missing, or inconsistent versions fail. Changes outside the Claude plugin subtree need no version bump. The guard runs the policy from the trusted base whenever it exists. On the first PR introducing the policy, the base has no script, so CI must temporarily run the candidate's policy; this bootstrap cannot prevent that PR from weakening its own guard. After merge, subsequent PRs use the base policy.

## Transient Artifact Policy

PR validation inspects the complete changed-file set. Deleted artifacts are allowed; enforcement applies to added, modified, copied, and renamed destination paths. It rejects the following transient artifacts unless a path is explicitly described as repository-root-only:

| Enforced class | Forbidden paths and variants |
|---|---|
| Agent-tool state | Any directory named `.atl` (at any depth) and `**/engram-dev/**` |
| Generated agent links | Repository-root `.claude/skills/**`, `.codex/skills/**`, `.github/skills/**`, and `.gemini/skills/**` |
| Transient development documents | Repository-root `plan.md`, `agent-report.md`, `agent-handoff.md`, and `handoff.md` |
| Transient process artifacts | Repository-root `openspec/changes/**` and `sdd/changes/**` |
| Release metadata | `**/.release-notes-beta.md` |
| Local data | `**/*.db`, `**/*.db-wal`, `**/*.db-shm`, and `**/engram-export.json` |
| Binaries | Repository-root `engram`, `cmd/engram/main`, `cmd/engram/gentle-creation`, `cmd/engram/engram`, and `**/*.exe` |
| OS metadata | `**/.DS_Store` and `**/Thumbs.db` |
| Editor metadata | `**/.idea/**` and `**/.vscode/**` |
| Editor and backup files | `**/*.swp`, `**/*.swo`, and `**/*~` |

Canonical, reviewable documentation is allowed. For example, `docs/plan.md` and `specs/transient-artifact-policy.md` are documentation, not root transient development documents. The four transient document names above are forbidden only at the repository root; do not use documentation paths to retain ephemeral local notes.

### Quality Ratchets

Every PR and push to `main` runs `make deadcode-check`. It analyzes all module
packages with `golang.org/x/tools/cmd/deadcode@v0.30.0` and compares stable
`file<TAB>symbol` identities with `.deadcode-baseline.txt`. New unreachable
functions fail CI. Removed entries pass and report that the debt tightened;
review and deliberately refresh the baseline with `make deadcode-baseline` in
the same change. Do not update a baseline merely to accept new debt.

The default analyzer runs with command-scoped `GOTOOLCHAIN=go<version>+auto`,
where `<version>` comes from the `go` directive in the repository's `go.mod`,
not its optional `toolchain` directive or your ambient `GOTOOLCHAIN`. Go may
automatically download that toolchain (or a newer required toolchain); the default
analyzer requires a Go 1.21 or newer launcher for toolchain selection. Allow
download access or provide the toolchain locally. Missing or invalid module
minimums and analyzer/toolchain failures stop the check; a failed analyzer never
refreshes the baseline.

`DEADCODE_RATCHET_ANALYZER` bypasses this selection entirely: owners of that
explicit executable override are responsible for its compatible toolchain and
analyzer behavior. `--compare <baseline> <candidate>` only compares identity
files and does not invoke Go.

Patchless minimums from Go 1.21 onward select the `.0` release (for example,
`go 1.24` selects `go1.24.0+auto`). Explicit patches and historical release
names through Go 1.20 are preserved.

### Performance Ratchet

Pushes to `main` compare the store search and scan benchmarks with the exact
previous `main` SHA from the push event on the same runner. This catches
statistically significant slowdowns greater than the configured threshold
without treating timing as a unit-test assertion.

Run `make perf-check` against the committed baseline on a matching local
configuration. To refresh that reviewed baseline deliberately after an accepted
performance tradeoff, run `make perf-baseline` and include the baseline change
with its justification. The baseline records its producing OS, architecture,
and CPU and is only comparable on a matching host configuration; it is not a
cross-host latency budget. CI instead compares the event's previous SHA and the
new revision on one runner.

For a repository's first main push, or when that previous revision has only a
strict subset of the current benchmark suite, CI enters an explicit bootstrap
mode. It verifies that the candidate benchmark names exactly match the versioned
baseline, then deliberately skips a cross-host timing comparison. Later pushes
must pair every benchmark from both revisions; an empty, renamed, partial, or
configuration-split comparison fails.

### Lint Ratchet

CI runs golangci-lint v2.13.2 with the `errcheck`, `staticcheck`, and `unused`
linters. It reports only findings introduced by the pull request or the pushed
main revision, so existing debt does not block adoption while new debt fails
the check. When local lint is warranted by risk or the absence of PR CI, install
golangci-lint v2.13.2 and run `make lint`; the target requires that exact version
on `PATH` and fails before linting if it is missing or different. It reports
findings in staged, unstaged, untracked, and latest committed changes compared
with `HEAD~`.

---

## Label System

### Type Labels (required on every PR — pick exactly one)

| Label | Color | Use for |
|-------|-------|---------|
| `type:bug` | 🔴 | Bug fixes |
| `type:feature` | 🔵 | New features |
| `type:question` | 🟣 | Questions requiring tracked work |
| `type:docs` | 🔵 | Documentation-only changes |
| `type:refactor` | 🟣 | Code refactoring with no behavior change |
| `type:chore` | ⚪ | Maintenance, tooling, dependencies |
| `type:breaking-change` | 🔴 | Breaking changes (requires major version bump) |

### Status Labels (set by maintainers)

| Label | Meaning |
|-------|---------|
| `status:needs-review` | Awaiting maintainer review (auto-applied to new issues) |
| `status:approved` | Approved for implementation — PRs can now be opened |
| `status:in-progress` | Actively being worked on — auto-exempt from stale bot |
| `status:blocked` | Blocked by another issue or external dependency |
| `status:stale` | No activity for 30 days — auto-applied by stale bot |
| `status:wontfix` | Closed without implementation — applied to stale, rejected, or duplicate items |
| `status:possible-duplicate` | Potential duplicate under evaluation |

### Resolution Labels (set after closure)

| Label | Meaning |
|-------|---------|
| `resolution:duplicate` | Confirmed duplicate after closure |

Replace the current `status:*` label with `status:possible-duplicate` while evaluating a report. After confirming and closing the duplicate, replace `status:possible-duplicate` with `status:wontfix` and apply `resolution:duplicate`.

### Priority Labels (set by maintainers)

`priority:critical`, `priority:high`, `priority:medium`, `priority:low`

> Issues with `priority:critical`, `priority:high`, and `status:approved` are never auto-closed by the stale bot.

### Effort Labels (set by maintainers, for contributor guidance)

| Label | Meaning |
|-------|---------|
| `effort:small` | < 1 hour — good starting point for new contributors |
| `effort:medium` | 1–4 hours |
| `effort:large` | > 4 hours or spans multiple files |

### PR Size Exception (maintainers only)

`size:exception` records explicit maintainer approval for a pull request to exceed the 400-line review budget. It applies only to pull requests and does not replace the requirement for a focused, reviewable change.

The advisory **PR Size Notice** counts the current GitHub-reported additions plus
deletions, without file exclusions or logical/authored-line adjustments. Exactly
400 changed lines is within budget; 401 or more produces a warning. For example,
200 additions plus 200 deletions is 400; 200 plus 201 is 401. Deleted files,
generated output, and lockfiles are not excluded. Binary changes have only the
line counts reported by GitHub; this is not a byte-size metric.

The notice respects `size:exception` and never applies it automatically. Label
presence represents the existing maintainer-owned policy; automated validation
does not verify who applied the label. Maintainers remain responsible for grants.

One consistently named informational check writes a fresh job summary on PR
opening, edits (including base-branch retargeting), reopening, synchronization,
and label addition/removal, including drafts.
It refreshes when the diff shrinks or the exception changes; it does not post PR
comments or run on merge groups. Actions retains historical runs, not one mutable
summary across all revisions. Unavailable or invalid current metadata produces
**Size unavailable**, never a claim that the PR is within budget. Summary-write
errors are reported as warnings.

The notice is non-required and non-blocking: a successful job means the advisory
completed, not that the PR is under budget. No required contexts or rulesets are
changed. It reads current PR metadata and runs only trusted base-revision code,
with read-only permissions and no persisted checkout credentials. The workflow
becomes available after it is merged into the base branch; local tests do not
prove live fork events, summaries, or cancellation behavior.

Run `node --test .github/scripts/pr-size-notice.test.mjs` for the boundary,
exception, refresh, unavailable-data, and workflow regression checks.

### Namespace Contract

All label namespaces are owned by maintainers. Their cardinality depends on the GitHub surface:

| Namespace | Issues | Pull requests |
|-----------|--------|---------------|
| `type:*` | Exactly one | Exactly one |
| `status:*` | Exactly one | At most one |
| `priority:*` | At most one | Not applicable |
| `resolution:*` | At most one | Not applicable |
| `effort:*` | Multiple allowed | Not applicable |
| `size:*` | Not applicable | At most one |

The only unnamespaced labels are maintainer-owned protected exceptions: `good first issue` and `help wanted`.

### Deprecated Label Migration (maintainers only)

| Deprecated label | Canonical label | Precedence | Conflicting canonical value |
|------------------|-----------------|------------|-----------------------------|
| `bug` | `type:bug` | Canonical label wins | Stop for manual review |
| `enhancement` | `type:feature` | Canonical label wins | Stop for manual review |
| `question` | `type:question` | Canonical label wins | Stop for manual review |
| `documentation` | `type:docs` | Canonical label wins | Stop for manual review |
| `up for grabs` | `help wanted` | Canonical label wins | Stop for manual review |

Preview a label migration locally before changing an issue or pull request:

```bash
node .github/scripts/label-policy.mjs --migrate --labels-json '["bug","type:bug"]'
```

The command is a dry run: it prints the canonical label list as JSON and never writes to GitHub. A singleton conflict returns a non-zero exit code and leaves the original list unchanged; stop and resolve that ambiguity manually. Otherwise, apply only the reported replacements with `gh issue edit` or `gh pr edit`, then rerun the command until it reports `"changed":false`.

---

## PR Rules

- Keep PR scope focused — one logical change per PR
- Use [conventional commits](https://www.conventionalcommits.org/) format
- For behavior changes, run a focused regression test and the affected package(s) locally before proposing the change; record the commands and outcomes. Use targeted coverage when useful to find missed behavior, without a numeric target or per-PR total module coverage requirement.
- Verification is candidate evidence; GitHub CI is the broad automated execution venue once the candidate is pushed to a PR. CI runs the full unit suite (`go test ./...`), E2E suite (`go test -tags e2e ./internal/server/...`), lint, and applicable platform checks. Do not claim these results until they run.
- For candidates without a PR or not yet pushed, and for justified high-risk changes, run additional applicable checks locally and report any missing CI evidence. Do not duplicate the broad CI suite locally by default when PR CI will run it.
- Update docs in the same PR when behavior changes
- Do not reference endpoints/scripts that do not exist in code
- Do not include `Co-Authored-By` trailers in commits
- Do not include paths prohibited by the [Transient Artifact Policy](#transient-artifact-policy)

### Conventional Commit Format

```
<type>(<scope>): <short description>

[optional body]

[optional footer]
```

**Examples:**

```
feat(cli): add --json flag to session list command

fix(store): prevent duplicate observation insert on retry

docs(contributing): add label system documentation

refactor(internal): extract search query sanitizer

chore(deps): bump github.com/charmbracelet/bubbletea to v0.26

fix!: change session ID format (breaking change)
BREAKING CHANGE: session IDs are now UUIDs instead of integers
```

Types map to labels: `feat` → `type:feature`, `fix` → `type:bug`, `docs` → `type:docs`, `refactor` → `type:refactor`, `chore` → `type:chore`.

---

## npm Dependency Hygiene

When adding npm dependencies to `plugin/pi` or `plugin/obsidian`:

### Use `npq` for inspection

Install once:

```bash
npm i -g npq
```

Then install new deps via:

```bash
npq install <package>
```

`npq` runs pre-flight checks (typosquats, install scripts, known vulns) before delegating to npm.

### Honor the `.npmrc` defaults

The repo `.npmrc` enforces:

- `ignore-scripts=true` — third-party lifecycle scripts do NOT run on install
- `allow-git=none` — git URLs as deps are rejected
- `min-release-age=3` — packages newer than 3 days old are rejected

If you have a legitimate reason to override these for local dev (e.g. `esbuild` postinstall), use a flag for that specific command — DO NOT edit `.npmrc`:

```bash
npm install --ignore-scripts=false esbuild
```

### Consult Snyk before merging

For every new dep added in a PR, paste the Snyk Advisor link in the PR description:

```
https://snyk.io/advisor/npm-package/<name>
```

See [SECURITY.md](./SECURITY.md#vetting-new-dependencies) for the maintainer-side vetting checklist (provenance, transitive deps, install scripts).

---

## Skill Authoring Standard

Repository skills live in `skills/`.

Use a **hybrid format**:

1. Structured base (purpose, when to use, critical rules, checklists)
2. Cookbook section (`If / Then / Example`) for repetitive actions

Why hybrid:
- Structured base protects correctness and architecture intent
- Cookbook improves execution consistency for common flows

---

## Automated Dependency Updates

Dependabot preserves the existing Go, GitHub Actions and Docker coverage and
checks Obsidian's `plugin/obsidian/package.json` and tracked lockfile every
Monday at 09:00 UTC. The npm job allows at most three open version-update PRs
and has a three-day cooldown for new versions. The cooldown does not apply to
security updates and does not replace dependency vetting or npm install safety.
Pi coverage is deferred; its current manifest declares only peer dependencies.

Bot-authored PRs are not exempt from the approved-issue checks:

1. A maintainer creates or reuses an issue covering the specific proposed update
   and approves it with `status:approved`.
2. Add `Closes #N`, `Fixes #N`, or `Resolves #N` to the bot PR body, preserving
   the generated update details. Do not reuse the configuration setup issue as
   authorization for subsequent dependency upgrades.
3. Keep exactly one canonical `type:chore` label. The PR remains blocked until
   the required issue checks pass; CI and human review still decide merge.

No automatic issue approval, check bypass or auto-merge is configured.
The npm-hygiene audit is separate: preserving the root `.npmrc` is not proof
that independent nested-package installs enforce its controls.

Run `go test . -run '^TestDependabot' -count=1` to check the versioned policy
and preservation of the other ecosystems. After merge to the default branch,
inspect Dependabot's update logs and an actual Obsidian update PR for
manifest/lockfile consistency, labels and passing checks. Configuration tests
do not prove vendor-schema validation, bot execution or successful rollout.

---

## Maintainer Triage Cadence

Engram uses a lightweight, regular cadence so contributors know what to expect.

| Activity | Frequency | What Happens |
|----------|-----------|-------------|
| New issue triage | Within 2 days | Maintainer labels + approves or closes |
| PR review | Within 7 days | Maintainer reviews + requests changes or merges |
| Backlog sweep | Weekly (Monday) | Stale bot runs; approved/blocked issues reassessed |
| Label audit | Monthly | Orphan labels removed; accuracy check |
| Dependabot PRs | Weekly | Review merged or deferred |

If you haven't received a response within 7 days on a PR or issue, a single ping comment is welcome.

---

## What Gets Closed Without Merging

- PRs opened without an approved issue
- PRs that fail CI and aren't updated within 30 days
- Issues that are vague, a duplicate, or belong in [Discussions](https://github.com/Gentleman-Programming/engram/discussions)
- Issues with no response to a maintainer question after 14 days

---

## Agent Skill Linking

Run:

```bash
./setup.sh
```

This links repo `skills/*` into project-local:
- `.claude/skills/*`
- `.codex/skills/*`
- `.gemini/skills/*`
