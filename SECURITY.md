# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 0.1.x   | :white_check_mark: |

## Reporting a Vulnerability

We take the security of TokenFuse seriously, especially because it interacts with sensitive **Admin API keys** for AI providers.

### How to Report

Please report security vulnerabilities privately by emailing **security@yourdomain.com** (replace with your contact) or by using GitHub's private vulnerability reporting feature:

1. Go to the [Security tab](https://github.com/angalor/tokenfuse/security/advisories/new) of this repository.
2. Click "Report a vulnerability".
3. Provide as much detail as possible.

### What to Include

- Description of the vulnerability
- Steps to reproduce
- Potential impact (e.g., exposure of admin keys, unauthorized key deactivation)
- Any proof-of-concept or suggested fix

### Response Timeline

- We will acknowledge receipt within **48 hours**.
- We aim to provide an initial assessment within **7 days**.
- Critical vulnerabilities will be prioritized and patched as quickly as possible.
- We will keep you informed of progress.

### Scope

Particularly sensitive areas include:
- Handling of `ANTHROPIC_ADMIN_KEY` and `OPENAI_ADMIN_KEY`
- Any code that could leak credentials in logs, errors, or storage
- Enforcement actions that could be bypassed
- Authentication/authorization in the HTTP server (metrics endpoint)

### Out of Scope

- Issues in dependencies (report to upstream)
- Social engineering attacks
- Denial of service via rate limiting (we have jitter, but this is expected)

Thank you for helping keep TokenFuse and its users secure!