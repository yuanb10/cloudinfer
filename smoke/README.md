# Smoke Tests

This folder holds repository-level smoke tests that exercise public HTTP
contracts across package boundaries.

Keep tests here when they verify integrated behavior, such as:

- endpoint availability
- wire-format or content-type contracts
- Prometheus scrape compatibility
- redaction and exposure guardrails at the HTTP boundary

Current smoke checks:

- `go test ./smoke` runs the in-memory HTTP contract smoke tests
- `bash smoke/kind-e2e/verify.sh` runs the Kubernetes rollout smoke test in kind
- `bash smoke/kind-prometheus/verify.sh` runs the optional kind-based Prometheus scrape smoke test
- `bash smoke/perf/verify.sh` runs the short performance smoke and writes artifacts
