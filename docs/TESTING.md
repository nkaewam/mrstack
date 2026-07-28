# Testing mrstack

The default suite is hermetic: it does not contact GitLab or GitHub and does not need credentials.

```text
go test -shuffle=on -count=1 ./...
go test -race -shuffle=on -count=1 ./...
go vet ./...
test -z "$(gofmt -l .)"
CGO_ENABLED=0 go build -trimpath ./cmd/mrstack
```

`task test-all` runs the same local release gate.

## Test layers

| Layer | Mechanical claims |
|---|---|
| CLI parser/process | Every documented command form, global options at arbitrary positions, required and mutually exclusive options, exact exit classes, one JSON document, no prompts in agent mode, help/version, literal metacharacter arguments |
| Public agent API | Envelope invariants, disposition precedence, exact IDs, action bindings, cross-references, full object IDs, state/publication combinations, per-kind evidence allowlists, credential/source/diff/trace redaction |
| Stack domain | Selector equivalence, strict order, forks, cycles, ambiguity, cross-project members, depth, missing branches, closed/merged lifecycle, legacy/native advancement, affected suffix, exact-current CI, policy/status handling |
| Git adapter | Credential-free remote parsing, full OIDs, ancestry, registered worktree dirtiness, local-only commits, argv-only atomic pushes, explicit per-ref leases |
| Real Git integration | Multi-ref publication, receive-hook rejection without partial refs, stale-lease rejection, isolated worktrees, ordered replay, author/message preservation, merge-commit refusal, conflict retention, explicit empty-commit stop |
| Journal | SQLite WAL, one active session per project under contention, optimistic transitions, state-machine edges, terminal slot release, crash/reopen persistence, exact old/new/indeterminate reconciliation |
| GitLab adapter | Allowlisted `glab api` argv, provider decoding, decimal IDs, target-update argument safety, bounded tail-preserving CI logs, invalid UTF-8 reporting |
| Contract/docs | The frozen JSON Schema compiles, real CLI failure output validates, unsafe producer drift is rejected, full-OID schema remains enforced, local Markdown links resolve |
| Build/release | CGO-free Linux/macOS amd64/arm64 builds, native smoke test, checksums, SBOMs, build provenance, immutable GitHub Release |

Tests use temporary directories and repositories. They never modify a developer-managed worktree or remote.

## Fuzzing

Go fuzz targets cover remote URL sanitization, CI log budgets, relationship discovery, disposition precedence, GitLab version parsing, and CI currentness. Run a longer campaign with:

```text
go test -fuzz=. -fuzztime=30s ./internal/stack
go test -fuzz=. -fuzztime=30s ./internal/gitexec
go test -fuzz=. -fuzztime=30s ./internal/gitlab
```

Go runs one fuzz target per command. To campaign every target, select each target by name from `go test -list Fuzz` and run it separately.

## Live GitLab contract

Claims that a fake provider cannot prove are isolated behind the `livegitlab` build tag and explicit opt-in:

```text
MRSTACK_LIVE_CONTRACT=1 \
MRSTACK_CONTRACT_HOST=gitlab.example.com \
MRSTACK_CONTRACT_PROJECT=team/service \
go test -tags=livegitlab -count=1 ./internal/livecontract
```

This suite uses the workstation's existing `glab` authentication. It is excluded from ordinary GitHub-hosted workflows, and its output must not be uploaded as an artifact.
