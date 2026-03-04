# Release Process

## Versioning

Use semver-style tags such as `v0.3.0-alpha`.

## Pre-release Checklist

1. Ensure `main` is green.
2. Run `go test ./...`.
3. Run the kind e2e smoke and perf smoke checks.
4. Confirm docs reflect any new config, endpoints, or operational behavior.
5. Review dependency and security results.

## Release Workflow

The `release.yml` workflow is responsible for:

- building binaries
- building and pushing the container image
- packaging the `deploy/` manifests
- packaging the minimal Helm chart
- attaching artifacts to the GitHub release

## Artifacts

Expected release artifacts:

- platform-specific `cloudinfer` binaries
- a tagged OCI image in GHCR
- a tarball of the `deploy/` manifests
- a packaged Helm chart

## Post-release

1. Verify published artifacts can be pulled and started.
2. Verify the release notes reflect operator-facing changes.
3. Open follow-up issues for deferred release work.
