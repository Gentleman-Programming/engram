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

## Recognition

We recognize responsible disclosures in the release notes of the version that contains the fix.
