# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in plancheck, please report it by [opening a GitHub issue](https://github.com/justinstimatze/plancheck/issues/new?template=bug_report.md) with the label "security".

For sensitive issues (e.g., credential exposure), email justin@justinstimatze.com rather than opening a public issue.

## Scope

plancheck runs locally and makes API calls to:
- **Anthropic API** (api.anthropic.com) — for the implementation spike, using your API key
- **Dolt CLI** — local subprocess for reference graph queries
- **go build** — local subprocess for compiler verification

plancheck does not run a network server, accept inbound connections, or transmit your code to any service other than the Anthropic API during spike execution.

## API Key Handling

- API keys are read from `PLANCHECK_API_KEY` or `ANTHROPIC_API_KEY` environment variables
- Keys are never written to disk, logged, or included in check results
- The `.env` file (if used) is gitignored by default
