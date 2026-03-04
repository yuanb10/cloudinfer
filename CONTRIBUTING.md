# Contributing

## Development Loop

1. Make changes in small, reviewable commits.
2. Run `go test ./...` before opening a PR.
3. Run `gofmt -w` on any modified Go files.
4. Prefer updating tests and docs in the same change.

## CI Expectations

PRs are expected to keep these green:

- unit and integration tests
- protocol conformance tests for chat and responses streaming
- drain behavior tests
- metrics contract tests

## Scope Discipline

- Keep Kubernetes manifests `kubectl apply -k` first-class.
- Keep the Helm chart minimal and close to the base manifests.
- Avoid adding high-cardinality metrics labels or prompt-bearing debug output.

## Pull Requests

- Describe the user-facing or operator-facing behavior change.
- Call out risk, migration concerns, and any new config or environment variables.
- Include test evidence for protocol, operational, or release-process changes.
