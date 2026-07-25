# GitHub Actions CI/CD contract

## Separation of systems

The source and release project lives on GitHub:

```text
github.com/nkaewam/mrstack
```

The product operates on GitLab:

```text
local mrstack -> local glab authentication -> Agoda GitLab
```

GitHub Actions verifies the implementation but does not become a bridge into the Agoda GitLab instance. Standard workflows contain no corporate GitLab token, cookie, SSH key, host certificate, project data, or CI log.

## Pull-request and main CI

Triggers:

```yaml
on:
  pull_request:
  push:
    branches: [main]
```

Required jobs:

1. **Contract**
   - validate `docs/schema/mrstack-v1.schema.json`;
   - validate checked-in JSON fixtures against the schema;
   - run runtime envelope invariants for finding precedence, cross-reference integrity, exact IDs, per-command data, and evidence redaction;
   - check local Markdown links and generated command/help snapshots;
   - fail when public enums, finding codes, or fixtures drift without an intentional contract update.
2. **Go quality**
   - verify `gofmt`;
   - run `go vet ./...`;
   - run the selected static analyzer;
   - run `go test ./...` with deterministic ordering and cancellation coverage.
3. **Race**
   - run `go test -race ./...` on Linux;
   - race testing may enable CGO in CI even though release binaries remain CGO-free.
4. **Integration**
   - use a scripted fake `glab`;
   - use real temporary Git repositories, linked worktrees, and a local bare remote;
   - exercise atomic publication, leases, conflict continuation, crash injection, SQLite WAL, and exact JSON output;
   - never call the Agoda GitLab host.
5. **Build**
   - compile smoke binaries for Linux and macOS on AMD64 and ARM64 with `CGO_ENABLED=0`;
   - run native smoke tests where the hosted runner architecture permits;
   - verify the binary has no unexpected runtime dependency.

Jobs should cancel superseded runs on the same pull request. Caches are keyed by operating system, Go version, and dependency checksums; cached output is never trusted as a test result.

## Live GitLab contract suite

The live suite covers only claims that a fake cannot establish:

- GitLab 18.11 and 19.1+ version formats;
- MR diff-version fields and historical object fetchability;
- merged-results pipeline association and temporary commit parents;
- project CI-required policy visibility;
- target-update permissions and idempotence;
- native successor retarget behavior.

It is a separate command, excluded from ordinary `go test ./...`, and is skipped without explicit environment configuration. It may run:

- manually from the developer's authenticated workstation; or
- on a company-approved internal runner whose repository and secret access have been reviewed.

It must not run for forked pull requests, Dependabot pull requests, or arbitrary GitHub event payloads. Logs and artifacts must redact hostnames, project paths, tokens, job traces, and repository content before upload.

## Release workflow

An immutable `v*` tag starts release CI:

1. rerun the complete non-live verification suite;
2. build CGO-free archives for:
   - `darwin/amd64`;
   - `darwin/arm64`;
   - `linux/amd64`;
   - `linux/arm64`;
3. embed version, commit, and build timestamp through linker metadata;
4. generate SHA-256 checksums and an SBOM;
5. generate build provenance using GitHub's artifact-attestation mechanism;
6. publish archives, checksums, SBOM, and release notes to one GitHub Release.

Release jobs use GitHub's short-lived token with only the permissions required to create the release and attest artifacts. They do not use a long-lived personal access token. A failed matrix leg prevents publication of a partial release.

## Supply-chain controls

- Pin every action to a full commit SHA; a version comment may document the human-readable release.
- Grant permissions per job, defaulting the workflow to `contents: read`.
- Keep pull-request workflows read-only and avoid `pull_request_target` for code execution.
- Enable automated update proposals for Go modules and GitHub Actions, but require normal CI and review.
- Do not execute repository-provided scripts with secrets available.
- Retain build artifacts only as long as needed; GitHub Releases are the durable distribution channel.

## Initial workflow files

Implementation should create:

```text
.github/workflows/ci.yml
.github/workflows/release.yml
.github/dependabot.yml
```

The workflow YAML is implementation, not generated from this document. Exact action SHAs and Go versions are selected and reviewed when the files are introduced.
