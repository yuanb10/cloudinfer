# Security Policy

## Reporting a Vulnerability

Do not open public issues for suspected vulnerabilities.

Instead:

1. Use GitHub Security Advisories for private reporting when available.
2. If private reporting is unavailable, contact the maintainers directly and
   include reproduction details, impact, and any known mitigations.

## Response Expectations

- Initial triage acknowledgment: within 5 business days
- Status updates during active investigation
- Coordinated disclosure after a fix or mitigation is ready

## Scope

Please report issues involving:

- authentication or debug endpoint exposure
- secret leakage
- unsafe metrics or tracing payload exposure
- request routing isolation failures
- container or manifest security regressions

## Hardening in CI

The repository CI is expected to run:

- `go vet`
- `govulncheck`
- test coverage for metrics, tracing, and drain behavior
