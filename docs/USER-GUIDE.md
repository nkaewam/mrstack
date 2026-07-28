# mrstack user guide

## What is mrstack?

`mrstack` is a command-line tool for working with a linear stack of dependent
GitLab merge requests.

A normal merge request targets the project's default branch:

```text
feature-a ──MR !101──> main
```

In a stack, each later merge request targets the branch immediately before it:

```text
main <── feature-a <── feature-b <── feature-c
         MR !101        MR !102        MR !103
```

This lets reviewers examine one logical layer at a time. It also creates work
that GitLab does not completely automate: when `main` or an earlier branch
moves, the dependent branches may need to be replayed onto their new
predecessors.

`mrstack` provides four things:

- **Discovery** — derives the live stack from GitLab MR source/target
  relationships. No manifest or local configuration defines membership.
- **Health checks** — reports topology, alignment, mergeability, and exact
  current CI evidence for the whole stack.
- **Safe restacking** — rewrites only the first unaligned layer and its
  successors, then publishes all affected branches with one atomic, leased
  push.
- **Recovery and history** — keeps a local SQLite journal and durable restack
  sessions so conflicts, process crashes, ambiguous push outcomes, and legacy
  GitLab retarget failures can be handled without guessing.

It is designed for both people and coding agents. Human mode prints concise
text. Machine mode returns one versioned JSON document containing findings,
evidence, remediation instructions, and safe argument arrays.

## What mrstack does not do

`mrstack` intentionally does not:

- merge or approve merge requests;
- use the journal as the source of truth for the current stack;
- support forks, cycles, cross-project stacks, or branching MR graphs;
- rewrite a branch implicitly during `check`;
- choose a conflict resolution or silently drop an empty commit;
- guess whether a partially observable push succeeded;
- bypass local-only commits or dirty worktrees;
- execute shell command strings supplied by GitLab or an agent;
- store a GitLab access token.

All GitLab access runs through the caller's existing `glab` authentication.

## Supported stack shape

Version 1 accepts a chain of one to ten merge requests in one GitLab project.
The first active MR—called the **front**—must target the project's current
default branch. Every successor must target its predecessor's source branch.

For this example:

| Position | MR | Source | Target |
|---|---:|---|---|
| Front | `!101` | `feature-a` | `main` |
| Successor | `!102` | `feature-b` | `feature-a` |
| Last | `!103` | `feature-c` | `feature-b` |

The change unique to each MR is its **layer**. When an ancestry edge is stale,
`mrstack` identifies the **affected suffix**: the first stale layer and every
successor after it. An aligned prefix is left byte-for-byte unchanged.

## Safety model

Restacking rewrites Git history, so `mrstack` binds every mutation to an exact
observed snapshot.

A snapshot includes:

- the GitLab host and project;
- the selected Git remote and sanitized fetch/push identities;
- the default branch and its complete object ID;
- every participating MR's IID, lifecycle state, source and target;
- exact source and resolved-target revisions;
- member authorship and the selected GitLab operating mode.

The snapshot is revalidated before replay, before push, and before a legacy
target update. If any captured relationship, identity, or revision moves,
publication stops with `remote_changed`.

Affected refs are published using one `git push --atomic` command with an
explicit `--force-with-lease` expectation for every branch. There is no
sequential fallback. Before replay, `mrstack` also refuses to proceed if an
affected branch has:

- staged, unstaged, or untracked changes in a registered worktree; or
- local-only commits, even when that branch is not currently checked out.

Replay occurs in a CLI-managed worktree, never in your current checkout.

## Requirements

- Go 1.24 or newer when building from source
- [Task](https://taskfile.dev/) 3 when using repository development commands
- Git
- [`glab`](https://gitlab.com/gitlab-org/cli) authenticated to the target host
- a local clone or worktree of the GitLab project
- a Git remote whose fetch and push URLs identify that same project
- server support for atomic push when publishing more than one rewritten ref

Check the local tools:

```text
go version
task --version
git --version
glab --version
glab auth status
```

If needed, authenticate `glab` to your GitLab host:

```text
glab auth login --hostname gitlab.example.com
```

## Installation

### Build from this repository

```text
git clone https://github.com/nkaewam/mrstack.git
cd mrstack
task build
```

The binary is written to `bin/mrstack`. Copy it to a directory on `PATH`, or
invoke it directly:

```text
./bin/mrstack --version
```

### Install with Go

```text
go install github.com/nkaewam/mrstack/cmd/mrstack@latest
```

Make sure the Go binary directory is on `PATH`:

```text
export PATH="$(go env GOPATH)/bin:$PATH"
```

## First use

Run commands from the Git clone or one of its worktrees:

```text
cd /path/to/your/gitlab-project
mrstack doctor
```

`doctor` checks repository context, remote resolution, Git, `glab`, GitLab
authentication, server mode, and the local SQLite journal. It does not perform
a test push or mutate an MR just to prove permissions, so some capabilities
may legitimately be reported as `unverified`.

Then check the stack containing the current branch:

```text
mrstack check
```

You can select the same stack by MR IID or branch:

```text
mrstack check 102
mrstack check feature-b
```

If the current branch does not have an unambiguous upstream remote, select one
explicitly:

```text
mrstack --remote origin check 102
```

Global options may appear before or after the command, although placing them
first is usually easiest to read.

## Understanding a check

`check` is externally read-only. It may fetch Git objects into private refs and
record an observation locally, but it never changes an MR, remote branch, user
branch, or user worktree.

The result has one **disposition**:

| Disposition | Meaning |
|---|---|
| `ready` | The stack is valid, aligned, and green under the observed CI policy. This does not assert approvals or every GitLab merge policy. |
| `complete` | A previously tracked stack is proven merged into the default branch in dependency order. |
| `action_required` | A deterministic next action exists, such as restacking, repairing CI, resolving a conflict, or refreshing a local checkout. |
| `waiting` | External state is still changing or incomplete, such as a running pipeline or GitLab native retargeting. |
| `human_required` | Proceeding needs judgment or evidence the CLI cannot safely infer. |
| `invalid` | The selected MR relationships do not form a supported stack. |

Common findings include:

| Finding | Typical response |
|---|---|
| `needs_restack` | Start a restack using the returned snapshot ID. |
| `pipeline_failed` | Inspect the exact pipeline/job IDs and repair CI. |
| `pipeline_running` | Wait and run `check` again. |
| `missing_required_pipeline` | Start or wait for CI, then recheck. |
| `merge_conflict` | Restack to reproduce the conflict in a managed worktree. |
| `mergeability_checking` | Wait for GitLab to finish its mergeability calculation. |
| `local_work_present` | Commit/push, stash, or move the local work before restacking. |
| `foreign_authored_member` | Ask the member author or restructure the workflow; there is no override. |
| `ambiguous_layer_boundary` | Create a read-only manual restack plan with exact boundary evidence. |
| `remote_changed` | Discard the stale intent, run a fresh check, and use its new snapshot. |
| `local_checkout_stale` | Publication succeeded, but refresh the reported local branch manually. |

Machine output contains more detail than the human summary, including stable
finding IDs, evidence, exact affected layers, and allowed actions.

## The normal restack workflow

### 1. Check and capture a snapshot

For scripting, machine mode makes the snapshot ID easy to capture:

```text
mrstack --json --no-input check 102 > /tmp/mrstack-check.json
jq -r '.stack.snapshot_id' /tmp/mrstack-check.json
```

Review the findings and proposed remediation before mutating anything:

```text
jq '{disposition, findings, remediations}' /tmp/mrstack-check.json
```

### 2. Start the restack

Use the exact returned snapshot:

```text
mrstack restack 102 --snapshot <snapshot-id>
```

In non-interactive machine mode, mutations also require explicit confirmation:

```text
mrstack --json --no-input --yes \
  restack 102 --snapshot <snapshot-id>
```

The operation:

1. revalidates the project, topology, remote, and revisions;
2. calculates the smallest affected suffix;
3. resolves the exact commit range for each affected layer;
4. checks authorship, signatures, local refs, and worktrees;
5. creates a durable session and managed worktree;
6. replays every affected commit in order;
7. records complete old/new ref maps;
8. revalidates the snapshot;
9. atomically publishes every affected branch with explicit leases;
10. safely updates eligible local refs;
11. performs legacy successor retargeting when required.

No intermediate rewritten prefix is published.

### 3. Run a fresh check

After completion:

```text
mrstack check 102
```

Use the new snapshot for any later mutation. Snapshot IDs are intentionally
not reusable after the observed state changes.

## Conflict recovery

If replay conflicts, the remote remains unchanged and the session pauses in a
managed worktree. The result provides:

- the session ID;
- managed worktree path;
- current layer and original commit;
- conflicted paths;
- structured continue and abort actions.

Resolve the files in the reported worktree and stage the resolution yourself:

```text
cd <managed-worktree>
git status
# edit files
git add <resolved-files>
```

Then continue the same session from any worktree in the project:

```text
mrstack --remote origin restack continue --session <session-id>
```

`mrstack` never stages conflict resolutions for you. Continuing with unresolved
or merely unstaged work is rejected. A single session can pause and continue
multiple times before one final atomic publication.

To discard a session before publication:

```text
mrstack --remote origin restack abort --session <session-id>
```

## Empty commits

A replayed commit can become empty when its change already exists in the new
ancestry. `mrstack` pauses rather than silently choosing an outcome.

Drop the current commit:

```text
mrstack restack continue --session <session-id> --drop-current
```

Or preserve it as an explicitly empty commit:

```text
mrstack restack continue --session <session-id> --keep-empty
```

The decision is recorded in the journal.

## Signed commits

Rewriting a signed commit changes its identity and cannot preserve the original
signature. Preflight therefore blocks with `signed_commits`.

After reviewing the exact signed commits, explicitly authorize signature loss:

```text
mrstack restack 102 \
  --snapshot <snapshot-id> \
  --allow-signature-loss
```

The authorization is bound to the restack operation and journaled. `mrstack`
does not re-sign another author's commit.

## Manual layer boundaries

If GitLab and Git cannot establish one exact layer boundary, `mrstack` refuses
to fall back to merge-base or patch-ID guesses. Supply a complete 40- or
64-character object ID through a two-step plan.

First create a read-only plan:

```text
mrstack restack plan 102 \
  --snapshot <snapshot-id> \
  --layer-boundary 102=<full-object-id>
```

Repeat `--layer-boundary` when more than one layer requires an override. Review
the returned commits, refs, and boundary evidence. Then start exactly that
plan:

```text
mrstack restack --plan <plan-id>
```

The plan is bound to the snapshot. Any topology, revision, default-branch, or
remote-identity change invalidates it.

## Crash and publication recovery

Restack state is stored under:

```text
.git/mrstack/
```

The SQLite journal is `.git/mrstack/journal.sqlite`. Managed worktrees and
durable session metadata also live under the CLI-owned state area. Do not edit
these files while an operation is active.

After a process interruption, use the session ID returned earlier or reported
by the next project-scoped command:

```text
mrstack restack recover --session <session-id>
```

`recover` is read-only with respect to remote refs. It compares every affected
branch with the durable old/new maps:

- **all old** — the session can safely return to publication-ready or be
  aborted;
- **all new** — publication succeeded and the session can complete or retry
  only a pending legacy target update;
- **mixed or unexpected** — publication is indeterminate and no automated
  repair is attempted.

For an all-old publication-ready session:

```text
mrstack restack continue --session <session-id>
```

This revalidates the captured state before retrying the same atomic leased
publication. It is also still safe to abort.

If a session becomes `indeterminate_publication`, inspect the remote refs and
the returned evidence. When a human has decided to accept the current remote
as the new starting point, archive the session without changing any remote ref:

```text
mrstack restack abandon \
  --session <session-id> \
  --accept-current-remote
```

Abandonment is intentionally unavailable with `--no-input`.

## CI evidence and logs

`check` considers only CI bound to each MR's exact captured source revision.
A branch name or older green pipeline is insufficient. Merged-results
pipelines additionally require a matching MR association and the exact
source/target parent pair.

When a finding identifies a failed pipeline and jobs, fetch only those exact
logs:

```text
mrstack ci logs \
  --pipeline 123456 \
  --job 901 \
  --job 902
```

The default total response budget is 524,288 source bytes. Set a smaller or
larger budget up to 4,194,304:

```text
mrstack ci logs \
  --pipeline 123456 \
  --job 901 \
  --max-bytes 1048576
```

The budget is divided evenly across at most twenty jobs in request order. The
newest tail of each log is kept, invalid UTF-8 is replaced, and truncation is
reported. Raw traces are returned to the caller but are not stored in SQLite.

## History

Every successful `check` records an observation. Repeated unchanged findings
retain their identity and first-seen timestamp; resolution closes that interval,
and recurrence creates a new finding identity.

Show history for the currently selected stack:

```text
mrstack history
mrstack history 102
mrstack history --stack <stack-id>
```

History defaults to 50 observations and accepts up to 200:

```text
mrstack history --stack <stack-id> --limit 100
```

Machine output may return an opaque next cursor:

```text
mrstack --json --no-input history \
  --stack <stack-id> \
  --limit 50 \
  --cursor <opaque-cursor>
```

Add a human-friendly alias:

```text
mrstack history alias --stack <stack-id> checkout-refactor
```

Clear it:

```text
mrstack history alias --stack <stack-id> --clear
```

Prune old observations using an RFC 3339 timestamp or duration:

```text
mrstack history prune --before 720h
mrstack history prune --before 2026-01-01T00:00:00Z
mrstack history prune --before 720h --stack <stack-id>
```

Machine-mode pruning requires `--yes`. Pruning preserves unfinished sessions,
active plans, tracked-stack identity, and the newest observation for each
targeted stack.

## GitLab legacy and native modes

The default global mode is `auto`.

- GitLab versions below 19.1 use **legacy** mode. After a front MR merges,
  `mrstack` can prove the merged predecessor, replay the active suffix, publish
  branches, and retarget the new front. If retargeting fails after publication,
  the durable session becomes `retarget_pending`; `continue` retries only the
  target update.
- GitLab 19.1 and later use **native** mode. GitLab owns automatic successor
  retargeting. `mrstack` waits for that state instead of racing it with a manual
  update.

When server version detection is unavailable, choose explicitly:

```text
mrstack --gitlab-mode legacy doctor
mrstack --gitlab-mode native check 102
```

If GitLab's readable version contradicts the explicit mode, the command exits
as invalid input before observation or mutation.

## Machine mode for agents and scripts

Machine mode requires both flags:

```text
--json --no-input
```

Example:

```text
mrstack --json --no-input --remote origin check 102
```

The command emits exactly one `mrstack/v1` JSON document on standard output and
does not prompt. Operational errors are also returned as one JSON document.

Mutating machine commands require `--yes`:

```text
mrstack --json --no-input --yes --remote origin \
  restack 102 --snapshot <snapshot-id>
```

Do not construct shell strings from remediation output. Each suggested action
contains:

- a stable action kind;
- a literal `argv` array;
- an explicit working directory;
- mutation and confirmation flags;
- required snapshot, plan, session, member, or worktree identity.

Validate output against
[`docs/schema/mrstack-v1.schema.json`](schema/mrstack-v1.schema.json) and fail
closed on unknown action kinds or enum values before executing a mutation.

### Exit codes

| Exit | Meaning |
|---:|---|
| `0` | The command produced an authoritative result. This includes unhealthy, waiting, human-required, and invalid stack dispositions. |
| `2` | Invocation or selector input is invalid. |
| `3` | A dependency, authentication, transport, journal, or prerequisite is unavailable. |
| `4` | An unexpected internal invariant failed. |

Do not use the process exit code as stack health. On exit `0`, inspect
`.disposition` and `.findings`.

## Global options

| Option | Purpose |
|---|---|
| `--remote <name>` | Select the mutation remote explicitly. Without it, only the current branch's unambiguous upstream remote may be used. |
| `--gitlab-mode auto\|legacy\|native` | Detect or explicitly select version behavior. |
| `--json` | Emit the versioned JSON envelope; must be paired with `--no-input`. |
| `--no-input` | Disable prompts; must be paired with `--json`. |
| `--yes` | Confirm a mutation in machine mode. |
| `-h`, `--help` | Print human-readable command help. |
| `--version` | Print version, commit, and build timestamp. |

## Command reference

```text
mrstack doctor
mrstack check [<MR-or-branch> | --stack <id>]
mrstack restack [<MR-or-branch>] --snapshot <id> [--allow-signature-loss]
mrstack restack plan [<MR-or-branch>] --snapshot <id> \
  --layer-boundary <mr>=<full-sha>
mrstack restack --plan <id> [--allow-signature-loss]
mrstack restack continue --session <id> [--drop-current | --keep-empty]
mrstack restack abort --session <id>
mrstack restack recover --session <id>
mrstack restack abandon --session <id> --accept-current-remote
mrstack ci logs --pipeline <id> --job <id>... [--max-bytes <n>]
mrstack history [<MR-or-branch> | --stack <id>] \
  [--limit <1-200>] [--cursor <opaque>]
mrstack history alias --stack <id> (<alias> | --clear)
mrstack history prune --before <timestamp-or-duration> [--stack <id>]
```

## Troubleshooting

### `not_git_repository`

Run `mrstack` inside the project's Git clone or one of its worktrees.

### `ambiguous_remote`

Inspect remotes and upstream configuration:

```text
git remote -v
git branch -vv
```

Then select the intended remote explicitly:

```text
mrstack --remote origin check 102
```

The selected fetch and effective push URLs must resolve to the same authenticated
GitLab project.

### `authentication_failed`

Check the exact host and renew `glab` authentication:

```text
glab auth status
glab auth login --hostname gitlab.example.com
```

### `server_mode_undetermined`

If you know the GitLab version family, pass `--gitlab-mode legacy` or
`--gitlab-mode native`. Do not guess solely to bypass the check.

### `prerequisite_unsupported` for atomic push

No affected ref was published. Confirm that the Git server supports atomic
push. `mrstack` deliberately has no sequential-push fallback.

### `local_work_present`

Inspect every affected branch and registered worktree:

```text
git worktree list
git status
git log --oneline origin/<branch>..<branch>
```

Commit and push, stash, or relocate the work. There is no force override.

### `remote_changed`

The captured snapshot is stale. Run a fresh `check`, review the new result, and
start a new operation with the new snapshot or plan. Never substitute a new
snapshot ID into an old remediation packet.

### `operation_in_progress`

Only one mutation session may be active per project. Use the reported session
ID with `restack continue`, `abort`, or `recover`.

### `indeterminate_publication`

Do not push guessed repairs. Run `restack recover`, inspect the old/new ref map
evidence, and involve a human. Use `restack abandon` only after consciously
accepting the current remote state.

### A successful push reports `local_checkout_stale`

Remote publication succeeded, but `mrstack` did not move a branch that was
checked out or had moved locally. Inspect it, then update it using your normal
Git workflow.

## Recommended operating practice

- Run `doctor` after changing Git, `glab`, credentials, remotes, or GitLab host.
- Run a fresh `check` immediately before every restack.
- Keep `--remote` explicit in automation and multi-remote repositories.
- Treat snapshot, plan, and session IDs as opaque, single-purpose identities.
- Review every `human_required` result instead of scripting around it.
- Preserve `.git/mrstack` while any session is unfinished.
- Back up or retain the clone if durable recovery state matters.
- Never parse human output for automation; use `--json --no-input`.
- Never execute an action whose kind or required identities are unknown.
- Run `check` again after publication, retargeting, or manual recovery.

## Further reading

- [CLI contract](CLI.md)
- [Machine API contract](AGENT-API.md)
- [JSON Schema](schema/mrstack-v1.schema.json)
- [System design and safety invariants](DESIGN.md)
- [Acceptance scenarios](ACCEPTANCE.md)
- [Testing guide](TESTING.md)
- [GitHub Actions and release process](GITHUB-ACTIONS.md)
- [Domain terminology](../CONTEXT.md)

