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

## Related policies

- [Security policy](../SECURITY.md) for vulnerability reporting and supported-version security fixes.
- [Installation guide](./INSTALLATION.md) for installation methods.
- [Engram Cloud quickstart](./engram-cloud/quickstart.md) for Cloud deployment guidance.
