# Release Channels and Support Policy

This policy is the canonical source for choosing an Engram release and planning an upgrade or rollback.

## Choose a release channel

| Channel | Intended use | Security support | Production guidance |
| --- | --- | --- | --- |
| Latest stable | General and production use | Receives security fixes | Recommended for production |
| Release candidate (RC) | Prerelease validation and feedback | Not guaranteed | Not universally suitable for production |
| Older release | Legacy or temporary compatibility needs | Does not receive security fixes | Upgrade to the latest stable release when practical |

Release candidates may expose functionality not yet available in the latest stable release. Operators who need that functionality accept prerelease risk, including the absence of guaranteed security support and universal production suitability.

## Upgrade deliberately

Before upgrading:

1. Choose the release channel deliberately.
2. Read the release notes and any migration notes for the target release.
3. Back up relevant state and configuration.
4. Validate compatibility with your environment and integrations.
5. Retain a known-good installation or artifact until the upgrade is accepted.

## Rollback expectations

Rollback means restoring the known-good release and configuration, plus any required backup, using the documented procedure for the affected component. It does not promise an automated rollback path or behavior beyond that component's documentation. Feature or data migrations can constrain rollback; consult the release-specific notes before upgrading.

## Check release lag

The [stable release gap audit](../.github/workflows/release-gap-audit.yml) runs weekly and can be dispatched manually. It compares npm's `gentle-engram/latest` to the matching `pi-v*` tag and the latest stable GitHub Release to its `v*` tag. Changes on `main` to Pi plugin or Go runtime/build inputs after those tags produce a failing run and an actionable step summary. Documentation-only changes do not trigger a gap. Path changes are only a signal to review release need; they do not identify fixes or guarantee an artifact is broken. Unavailable metadata or missing tags fail the run rather than implying the channel is current. Maintainers should inspect the summary and changes, decide whether publication is appropriate, and use the existing tag-driven release workflows; the audit never publishes or edits issues.

The Pi npm publish workflow also supports manual dispatch, but it only publishes when the selected GitHub ref is the full `refs/tags/pi-v<package version>` tag matching `plugin/pi/package.json`. Select that tag for a manual run: dispatching from a branch, even one named `pi-v<package version>`, fails the release preflight.

## Candidate-first publication and manual promotion

Preparation is not publication authorization. The bounded artifact-staging work is tracked in
[#1928](https://github.com/Gentleman-Programming/engram/issues/1928); local workflow tests
are not GitHub Actions, registry, runtime, or publisher-permission proof. Public repository
CI artifacts are not private/encrypted. Raw staging binaries differ from final GoReleaser
archives/signatures; source/build provenance does not establish runtime compatibility.

For a separately authorized release:

1. Review the exact source commit, source identity and artifact digests. Tag a **new reviewed
   commit containing these guards and the #1924 preparation after review of the combined candidate**. Tag-triggered
   workflows come from the tagged commit: these changes do not retroactively gate older tags
   or the old #1924 commit. Keep core and Pi SemVer streams independent.
2. Publish Pi through the existing guarded `publish-pi.yml`, which retains provenance/public
   access and explicitly uses `--tag candidate`. It neither moves `latest` nor promotes itself.
3. Before core GoReleaser, `release-companion.mjs` reads the actual
   `piGentleEngramPackage` installer pin, requires its name/version to match `plugin/pi/package.json`,
   then makes one bounded, unauthenticated public npm metadata GET for that exact version.
   Missing versions, transport/HTTP/JSON failures and mismatched required `startupPolicy` or
   declared `sessionOwned` metadata stop core publication. There are no retries, redirects,
   private npm config reads or configurable registry endpoints. Startup-only source contracts
   remain supported when `sessionOwned` is absent. Current source and installer pin select 0.5.1
   and require owned metadata; public 0.4.2 lacks that contract and is not a compatible substitute.
   Source preparation does not establish public availability of 0.5.1.
   This is rejection-only availability evidence, **not** protocol, source-byte identity,
   published-content integrity or permission approval.
4. Core GoReleaser still performs its existing release and automatic Homebrew tap behavior.
   Homebrew is **not newly transactional or automatically deferred** by this guard. Candidate
   staging alone does not move Homebrew, but an authorized core release can do so.
5. Only after inspecting the real published pair, source/artifact identity, runtime/upgrade
   results and promotion rights, manually promote the exact Pi version to `latest`.
   npm CLI rights/2FA or separate OIDC dist-tag capability (npm >=11.21) remain unverified;
   the existing direct-publish npm minimum is unchanged. No automatic dist-tag job is added.
   Shell upgrade remains unresolved/user-deferred; no actual publication is claimed here.

If any gate or partial publication fails, stop promotion and inspect immutable npm versions,
existing assets, source commits and digests before any later authorized action. Never overwrite
an existing npm version or retarget a published Git tag. Fix forward; there is no automatic
resume, coordinator or atomic transaction across npm, GitHub and Homebrew. Restoring channels
is not a downgrade of installed users or their database. Preserve backups and standalone/runtime
ownership constraints; downgrade to the old 3.4 database was not tested.

## Related policies

- [Rejection-only candidate checker](./RELEASE-CANDIDATE.md) for opt-in metadata validation and current Pi blockers; it cannot approve a release and does not implement the full candidate gate.
- [Security policy](../SECURITY.md) for vulnerability reporting and supported-version security fixes.
- [Installation guide](./INSTALLATION.md) for installation methods.
- [Engram Cloud quickstart](./engram-cloud/quickstart.md) for Cloud deployment guidance.
