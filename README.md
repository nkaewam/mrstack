# mrstack

`mrstack` is a Go CLI for discovering, checking, and safely restacking a strict linear chain of ordinary GitLab merge requests. It targets Agoda's GitLab 18.11 deployment while retaining useful restack, CI-evidence, history, and agent-integration behavior after GitLab 19.1 native stacks arrive.

The repository contains the v1 implementation, its frozen machine-readable contract, deterministic fake-provider tests, real local-Git integration tests, and GitHub Actions CI/release automation.

Canonical source: `github.com/nkaewam/mrstack`. CI/CD and release automation will use GitHub Actions; runtime GitLab access remains local through `glab`.

## Design documents

- [V1 design](docs/DESIGN.md)
- [CLI contract](docs/CLI.md)
- [Agent JSON API](docs/AGENT-API.md)
- [Agent API JSON Schema](docs/schema/mrstack-v1.schema.json)
- [Acceptance contract](docs/ACCEPTANCE.md)
- [Testing guide](docs/TESTING.md)
- [GitHub Actions CI/CD contract](docs/GITHUB-ACTIONS.md)
- [Domain language](CONTEXT.md)
- [Architecture decisions](docs/adr/)

## Core rule

GitLab MR source and target branch relationships are the sole source of truth for live stack membership and order. The local journal records history and durable operation recovery but never defines the chain.

## Build and test

Go 1.24 or newer, `git`, and `glab` are required.

```text
make build
make test
make test-race
```

The binary is written to `bin/mrstack`. All GitLab access goes through the caller's existing `glab` authentication; `mrstack` does not store a GitLab token.
