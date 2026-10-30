# Candidate staging (preparation only)

`stage-release-candidate.yml` is manual `workflow_dispatch` only. It must first
be available on the default branch; select reviewed trusted workflow code, not
an arbitrary candidate workflow ref. No dispatch is authorized by this change.
Supply a reviewed full lowercase 40-hex source SHA and a core `major.minor.patch`
version (without `v`). Validation precedes candidate checkout and build.

## Trust boundary and artifact acceptance

`github.workflow_sha` identifies the selected workflow revision, not its approval.
The `policy` checkout records that revision but is not used by later steps and
is not an independent trust check. This workflow does not enforce a trusted-ref
restriction outside its editable YAML. A modified workflow can remove an in-file
guard or an environment reference; neither prevents every other artifact upload.

Before a separately authorized dispatch, independently review and approve the
exact workflow revision. Before accepting any output as staging evidence:

1. Use GitHub's [workflow-run API](https://docs.github.com/en/rest/actions/workflow-runs)
   to corroborate the expected repository, workflow ID/path, `workflow_dispatch`
   event, reviewed workflow revision, run ID/attempt and successful completion.
2. Use the [artifact API](https://docs.github.com/en/rest/actions/artifacts) to
   verify the exact artifact ID, its association with that run and its digest.
   Bind the manifest's candidate SHA and per-file hashes to the reviewed candidate
   and downloaded files. An archive digest is not a per-file checksum.
3. Reject missing or mismatched evidence. Artifact names, a mutable branch name
   or a manifest written by the producing workflow cannot establish approval.

These are required operator/consumer checks, not implemented automatic enforcement
or verified repository/organization policy. They do not authorize dispatch or
establish runtime compatibility, final release provenance or publication rights.

## What it produces

The trusted workflow and frozen candidate have separate sibling checkouts.
The manifest records workflow SHA, actual candidate SHA, run ID/attempt, core
version, independent Pi package version, sizes, SHA256 and SHA512 SRI per file.
Go 1.26.10 builds six Linux/macOS/Windows amd64/arm64 raw unsigned binaries with
CGO disabled, readonly modules, trimpath and embedded version/VCS information.
Each binary's inspected build metadata must match the source revision and report
`vcs.modified=false`; source cleanliness is checked before and after packaging.
All outputs use `${{ github.workspace }}/staging`, outside the sibling candidate
and policy checkouts. The `github` context is available in job-level `env`;
`runner` is not. Candidate binaries are not run.

Pi is packed locally with `npm pack --ignore-scripts`; no installation, lifecycle
hook, candidate JavaScript, registry publication, tag movement or Homebrew update
runs. Trusted Python reads npm JSON, checks tarball integrity and archive member
safety, then creates the manifest. Only the enumerated binaries, build metadata,
Pi tarball, npm pack metadata and manifest enter the success-only artifact.
The candidate's package contents still need review for confidential material;
name checks cannot classify arbitrary embedded secrets. Never stage sensitive code.

The workflow uses read-only contents permission, no publishing secrets/OIDC and
no persisted checkout credentials. It follows existing checkout/setup-go pinned
versions; upload-artifact v7 is pinned to the official immutable revision
`cf430e030ddbb5b0abf93d22962f4752f3646cd9`, for GitHub-hosted Ubuntu, not GHES.
Missing outputs fail; upload names include run
and attempt, overwrite is disabled, retention is seven days. GitHub's artifact
container digest is **not** a per-file checksum. Download and compare manifest
checksums separately. ZIP transport may normalize executable permissions.

## Limits and later release order

Public-repository CI artifacts are not private or encrypted distribution.
These are not final GoReleaser archives, signed bytes, npm provenance, installed
runtime/protocol compatibility, publisher permissions or positive release approval.
Structural Go tests inspect workflow text only: no live Actions/API, registry,
platform execution or toolchain/build proof is implied. No workflow was dispatched
as preparation evidence. Module downloads during a future Go build may use the
network; npm packing is local, not a registry permission probe.

Before any separately authorized publication, integrate and review both the
publisher guards and #1924 preparation. Tag a **new reviewed commit containing
those changes**, never old `ed9b9367`, which lacks the new workflows/guards.
A tag-triggered publisher loads workflow code from the tagged commit. A staging
artifact does not approve a later rebuild, tag, or different source/byte set.

Keep core and Pi SemVer streams independent. Under separate publication consent,
verify the exact published Pi candidate companion before core, then verify actual
published pair/source/digests, publisher/tap rights and installed upgrade behavior
before manual `latest` promotion. This workflow does not establish any of those
gates. npm Trusted Publisher, promotion and tap rights remain unverified; Shell
upgrade is user-deferred, not waived. Preserve backups, standalone operation and
runtime ownership constraints; old 3.4 database downgrade was not tested.

If any gate fails, stop promotion. Inspect existing immutable versions, assets,
source and digests before any later authorized retry. Do not replace npm versions
or retarget published tags. Moving a channel back does not undo installed-user or
database changes. There is no automatic retry, rollback, lockstep coordinator or
resume protocol here.
