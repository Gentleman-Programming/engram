# Security Policy

## Supported Versions

Only the latest stable release receives security fixes.

| Version | Supported |
|---------|-----------|
| latest  | ✅        |
| older   | ❌        |

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Report security issues privately via one of these channels:

1. **GitHub Security Advisories** (preferred): [Report a vulnerability](https://github.com/Gentleman-Programming/engram/security/advisories/new)
2. **Email**: Contact the maintainers directly through the GitHub profile if the advisory flow is unavailable.

### What to Include

- A clear description of the vulnerability
- Steps to reproduce
- The potential impact (data exposure, privilege escalation, denial of service, etc.)
- Any suggested mitigations you've identified

### Response Timeline

- **Acknowledgement**: within 48 hours of receiving your report
- **Initial assessment**: within 5 business days
- **Fix target**: within 30 days for critical/high severity, best effort for lower severity
- **Disclosure**: coordinated with you after a fix is available

### Scope

Engram is a local-first CLI tool that writes to a local SQLite database. The attack surface is intentionally small:

- **In scope**: privilege escalation, data corruption, path traversal, injection in MCP/HTTP API inputs, memory leaks exposing sensitive data
- **Out of scope**: issues requiring physical access to the machine where engram is installed, or issues that require the attacker to already have access to the user's home directory

## Maintainer Policies

### npm Account Security

All maintainers with publish access to any npm package owned by this project (`gentle-engram`, future packages) MUST:

- Enable **`auth-and-writes` two-factor authentication** on their npm account: `npm profile enable-2fa auth-and-writes`
- Use **trusted publishing (OIDC)** via GitHub Actions for all releases — never publish from a local machine with a long-lived token
- Verify trusted-publisher configuration at `https://www.npmjs.com/package/<name>/access` before each release

### Vetting New Dependencies

Before adding any npm dependency to `plugin/pi` or `plugin/obsidian`:

1. Check the package on Snyk Advisor: `https://snyk.io/advisor/npm-package/<name>`
2. Review the package's `package.json` on the registry — flag any `postinstall`, `preinstall`, or `prepare` scripts
3. Confirm the maintainer publishes with provenance (look for the green provenance badge on npmjs.com)
4. Prefer packages with zero or minimal transitive dependencies

See [CONTRIBUTING.md](./CONTRIBUTING.md#npm-dependency-hygiene) for the day-to-day contributor workflow (`npq`, `.npmrc` defaults, Snyk Advisor links in PRs).

## Recognition

We recognize responsible disclosures in the release notes of the version that contains the fix.
