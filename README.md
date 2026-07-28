# mrstack

`mrstack` is a Go CLI for discovering, checking, and safely restacking a strict
linear chain of ordinary GitLab merge requests. It targets Agoda's GitLab 18.11
deployment while retaining useful restack, CI-evidence, history, and
agent-integration behavior after GitLab 19.1 native stacks arrive.

Given a chain such as:

```text
main <── feature-a <── feature-b <── feature-c
         MR !101        MR !102        MR !103
```

`mrstack` derives the chain from GitLab, checks exact ancestry and CI, rewrites
only the stale suffix in an isolated worktree, and publishes every affected
branch atomically with explicit leases. It never merges MRs, silently resolves
conflicts, or guesses after an uncertain push.

Start with the [complete user guide](docs/USER-GUIDE.md).

Canonical source: `github.com/nkaewam/mrstack`. CI/CD and release automation
use GitHub Actions; runtime GitLab access remains local through `glab`.

## Installation

`mrstack` is a single CGO-free Go binary. Prebuilt archives are published for
macOS and Linux on AMD64 and ARM64. Git and an authenticated `glab` are
required at runtime.

### Homebrew (recommended)

Install the appropriate prebuilt release binary from the
[`nkaewam/tap`](https://github.com/nkaewam/homebrew-tap) tap:

```sh
brew install nkaewam/tap/mrstack
```

Go is not required. To upgrade later:

```sh
brew update
brew upgrade mrstack
```

Verify that the executable is available:

```sh
command -v mrstack
```

### GitHub Releases

Without Homebrew, download and verify the archive for the current operating
system and CPU architecture from
[GitHub Releases](https://github.com/nkaewam/mrstack/releases). Set `VERSION`
to the release tag to install:

```sh
VERSION=v0.1.0
case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux)  os=linux  ;;
esac
case "$(uname -m)" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|amd64)   arch=amd64 ;;
esac
curl -LO "https://github.com/nkaewam/mrstack/releases/download/${VERSION}/mrstack_${VERSION#v}_${os}_${arch}.tar.gz"
tar -xzf "mrstack_${VERSION#v}_${os}_${arch}.tar.gz"
install -m 0755 "mrstack_${VERSION#v}_${os}_${arch}/mrstack" /usr/local/bin/mrstack
```

## Agent Skill

`mrstack` ships an [Agent Skill](https://agentskills.io) that teaches coding
agents how to operate `mrstack` in machine mode — the check → restack →
recheck loop, interpreting dispositions/findings, executing remediation
actions verbatim, and honoring the safety invariants.

Install it with the [Vercel Skills CLI](https://vercel.com/docs/agent-resources/skills)
(works with Cursor, Claude Code, Codex, Copilot, Goose, OpenCode, and 18+
other agents):

```sh
npx skills add nkaewam/mrstack
```

Install globally (available in every project):

```sh
npx skills add nkaewam/mrstack -g
```

Target a specific agent, or skip prompts in CI:

```sh
npx skills add nkaewam/mrstack -a cursor
npx skills add nkaewam/mrstack -g -y
```

Browse skills at [skills.sh](https://skills.sh). The skill source lives at
[`skills/mrstack/`](skills/mrstack/) and validates against the
[Agent Skills spec](https://agentskills.io/specification.md).

## Quick start

Go 1.24 or newer, Task 3, `git`, and an authenticated `glab` are required.

```text
task build
cd /path/to/your/gitlab-project
/path/to/mrstack/bin/mrstack doctor
/path/to/mrstack/bin/mrstack check
```

Select a stack by current branch, MR IID, or branch name:

```text
mrstack check
mrstack check 102
mrstack check feature-b
```

If a check reports `needs_restack`, use the exact returned snapshot:

```text
mrstack restack 102 --snapshot <snapshot-id>
```

For agents and scripts, pair `--json` with `--no-input`; mutating commands also
require `--yes`:

```text
mrstack --json --no-input --remote origin check 102
mrstack --json --no-input --yes --remote origin \
  restack 102 --snapshot <snapshot-id>
```

## Design documents

- [V1 design](docs/DESIGN.md)
- [User guide](docs/USER-GUIDE.md)
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

```text
task build
task test
task test-race
```

The binary is written to `bin/mrstack`. All GitLab access goes through the caller's existing `glab` authentication; `mrstack` does not store a GitLab token.
