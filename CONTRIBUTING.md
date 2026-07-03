# Contributing to TokenFuse

Thank you for your interest in contributing to TokenFuse! This document provides guidelines and instructions for contributing.

## Code of Conduct

By participating in this project, you agree to maintain a respectful and inclusive environment. Please be considerate of others.

## How Can I Contribute?

### Reporting Bugs

- Use the [Bug Report template](.github/ISSUE_TEMPLATE/bug_report.md)
- Include steps to reproduce, expected vs actual behavior, environment details
- Check existing issues first to avoid duplicates

### Suggesting Enhancements

- Use the [Feature Request template](.github/ISSUE_TEMPLATE/feature_request.md)
- Clearly describe the problem and proposed solution
- Consider whether it fits the "out-of-band only" design philosophy

### Pull Requests

1. Fork the repository
2. Create a feature branch from `main` (`git checkout -b feature/amazing-feature`)
3. Make your changes
4. Ensure tests pass: `go test ./...`
5. Run linter: `golangci-lint run`
6. Commit using [Conventional Commits](https://www.conventionalcommits.org/) (e.g., `feat:`, `fix:`, `docs:`)
7. Push and open a Pull Request

## Development Setup

```bash
git clone https://github.com/angalor/tokenfuse.git
cd tokenfuse
go mod download
go test ./...
```

### Running Locally

```bash
# With dry-run (recommended for development)
tokenfuse run --dry-run --config tokenfuse.example.yaml

# With metrics
tokenfuse run --dry-run --metrics-addr :9090
```

## Project Structure

- `cmd/tokenfuse/` - CLI entrypoint
- `internal/` - Private packages (config, detect, store, provider, etc.)
- `provider/` - Integrations for Anthropic and OpenAI
- Tests live alongside code (`*_test.go`)

## Design Principles

When contributing, keep these core principles in mind:

- **Out-of-band only**: Never proxy requests. TokenFuse must not sit in the request path.
- **Fail safe on money**: Unknown models are never priced at $0.
- **Admin keys are radioactive**: Never log, store, or leak them.
- **--dry-run is first class**: Every new feature must work in dry-run mode.
- **Single static binary**: Keep dependencies minimal. No CGO.

## Commit Guidelines

We use [Conventional Commits](https://www.conventionalcommits.org/):

- `feat:` New feature
- `fix:` Bug fix
- `docs:` Documentation only
- `chore:` Maintenance tasks
- `test:` Adding or updating tests

Example: `feat(openai): add support for actual costs endpoint`

## Questions?

Feel free to open an issue with the `question` label or start a discussion.

Thank you for helping make TokenFuse better!