# Session-owned core/Pi pair — unreleased preparation

This is **unreleased preparation**, not release approval or a published compatibility claim.
It addresses the distribution gap in #1921 without implementing the coordinator in #1349.

## Proposed pair

| Component | Proposed version | Candidate requirement |
| --- | --- | --- |
| Engram core | `3.5.4` | Real `serve --session-owned` implementation and `engram-session-owned-v1` readiness; version remains injected by a future core tag/build. |
| `gentle-engram` | `0.5.1` | Matching source metadata and actual owned-child client; installer pin `npm:gentle-engram@0.5.1`. |

These independent version streams are deliberately not the lockstep policy proposed in #1349.
The names were observed unused during preparation, not reserved. A maintainer must recheck
availability before approving any release; never reuse or overwrite an immutable npm version.
Freeze the final candidate commit, both proposed tags, core checksums and companion package
identity before artifact validation. The historical 3.5.0/0.5.0 preparation base was
`0f2267479bb01aafdc6718fdf9e713cc8f6c492f`. The 3.5.3/0.5.1 preparation starts from
`ab461b90b03264c6cb009d5ca8406219a2dc0545`. Neither base reference is the eventual
release candidate or a delivered artifact.

## Why both sides matter

The published companion `0.4.2` advertises `startupPolicy: 1` but not `sessionOwned: 1`.
The local source tag for core `v3.4.0` lacks the new owned-server implementation.
Current source implements both sides. Editing installed metadata does not add missing code.
Keep readiness, child PID/identity, authentication, loopback endpoint and shutdown guards intact;
never fall back to adopting a detached shared server for a failed owned-child launch.

## Setup transition

The candidate's `engram setup pi` and the companion's `pi-engram init` select companion `0.5.1`.
Existing managed `0.4.2` and `0.5.0` pins join the previous known-pin migration list. Custom/local/object
entries, unknown/future pins, unrelated settings and MCP configuration remain unchanged.
Repeated setup is idempotent. A newer companion does not change an older installed core's pin.
Back up relevant user configuration and retain known-good artifacts before any real upgrade;
this preparation runs setup regressions only against isolated fixture settings.

## Gates before publication or upgrade

- Verify focused setup migration, prior managed-pin controls, malformed-input behavior and Pi
  package/tag/pin/help contracts. Record exact command outcomes and unavailable checks.
- Build and inspect the exact core and companion artifacts, not just their source manifests.
- Exercise the real pair in an isolated environment, especially Windows: owned-child startup,
  authentication, protocol/PID/identity/endpoint, session operations and exact-child shutdown.
  Check incompatible-core rejection and unchanged standalone behavior without weakening guards.
- Validate the supported downstream bundle upgrade; local source tests do not establish it.
- Confirm publisher authority, trusted-publisher workflow identity and any Homebrew credentials.
- Obtain explicit maintainer approval of publication order, staging/channel behavior and partial
  delivery recovery. Existing core/Pi workflows are independent and do not provide atomic pairing.
  No publication sequence, rollback guarantee or coordinator is introduced by this document.

The combined candidate includes the [release policy's candidate-first guards](./RELEASE-POLICY.md#candidate-first-publication-and-manual-promotion).
They check the actual `0.5.1` installer pin and required public companion metadata before core
publication; source preparation alone does not prove that version is publicly available.
Pi publication uses `candidate`; promotion to `latest` remains manual and rights-gated.
Core's existing Homebrew behavior remains non-atomic. These guards do not transfer earlier
artifact/runtime evidence to a new source tree or prove official release bytes or bundle upgrades.

The [candidate checker](./RELEASE-CANDIDATE.md) remains **rejection-only**. Passing its tests,
metadata checks or package-content inspection cannot certify runtime compatibility or release readiness.
No published-binary handshake, supported Windows bundle upgrade or publisher-permission evidence
is claimed by this preparation. Actual validation results must accompany the eventual release decision.

## Merge and issue disposition

The preparation PR may move to **Ready for review** only after explicit maintainer authorization.
Review readiness does not authorize merge, queue admission, publication or installation.
Do not merge an installer pin to an unavailable npm target;
publication availability alone is insufficient without artifact pairing and upgrade validation.
Do not push tags, dispatch publishing workflows, install into user environments or publish npm
as part of this PR. Publication remains a separate maintainer decision with its own evidence.
After an authorized release, inspect the actual published metadata, artifact identities and
supported upgrade. Only then resolve #1921. A preparation child may close when its preparation
work is delivered; neither #1921 nor #1349 is resolved by a source version bump.
