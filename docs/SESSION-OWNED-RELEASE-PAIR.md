# Session-owned core/Pi pair — unreleased preparation

This is a **draft preparation**, not release approval or a published compatibility claim.
It addresses the distribution gap in #1921 without implementing the coordinator in #1349.

## Proposed pair

| Component | Proposed version | Candidate requirement |
| --- | --- | --- |
| Engram core | `3.5.0` | Real `serve --session-owned` implementation and `engram-session-owned-v1` readiness; version remains injected by a future core tag/build. |
| `gentle-engram` | `0.5.0` | Matching source metadata and actual owned-child client; installer pin `npm:gentle-engram@0.5.0`. |

These independent version streams are deliberately not the lockstep policy proposed in #1349.
The names were observed unused during preparation, not reserved. A maintainer must recheck
availability before approving any release; never reuse or overwrite an immutable npm version.
Freeze the final candidate commit, both proposed tags, core checksums and companion package
identity before artifact validation. The preparation base is `0f2267479bb01aafdc6718fdf9e713cc8f6c492f`;
that base reference is not the eventual release candidate or a delivered artifact.

## Why both sides matter

The published companion `0.4.2` advertises `startupPolicy: 1` but not `sessionOwned: 1`.
The local source tag for core `v3.4.0` lacks the new owned-server implementation.
Current source implements both sides. Editing installed metadata does not add missing code.
Keep readiness, child PID/identity, authentication, loopback endpoint and shutdown guards intact;
never fall back to adopting a detached shared server for a failed owned-child launch.

## Setup transition

The candidate's `engram setup pi` and the companion's `pi-engram init` select companion `0.5.0`.
Existing managed `0.4.2` pins join the previous known-pin migration list. Custom/local/object
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

The [candidate checker](./RELEASE-CANDIDATE.md) remains **rejection-only**. Passing its tests,
metadata checks or package-content inspection cannot certify runtime compatibility or release readiness.
No published-binary handshake, supported Windows bundle upgrade or publisher-permission evidence
is claimed by this preparation. Actual validation results must accompany the eventual release decision.

## Merge and issue disposition

Keep the preparation PR **draft**. Do not merge an installer pin to an unavailable npm target;
publication availability alone is insufficient without artifact pairing and upgrade validation.
Do not push tags, dispatch publishing workflows, install into user environments or publish npm
as part of this PR. Publication remains a separate maintainer decision with its own evidence.
After an authorized release, inspect the actual published metadata, artifact identities and
supported upgrade. Only then resolve #1921. A preparation child may close when its draft-preparation
work is delivered; neither #1921 nor #1349 is resolved by a source version bump.
