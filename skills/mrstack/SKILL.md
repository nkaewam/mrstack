---
name: mrstack
description: >-
  Drive the `mrstack` CLI to check and safely restack user-curated linear GitLab
  MR stacks. Use when working with a GitLab MR stack/chain (each MR targets the
  previous branch), managing named stacks (`stack create/add/list`), checking
  stack health/CI, restacking after base movement, or fixing stale ancestry.
  Also use when an agent must operate `mrstack` in machine mode (`--json --no-input`).
license: MIT
compatibility: >-
  Requires the `mrstack` binary on PATH, `git`, and an authenticated `glab`
  (GitLab CLI). Run inside a Git clone or worktree of the GitLab project.
metadata:
  author: nkaewam
  version: "0.3.0"
  source: https://github.com/nkaewam/mrstack
  schema: https://github.com/nkaewam/mrstack/blob/main/docs/schema/mrstack-v1.schema.json
---

# mrstack

`mrstack` checks and safely restacks **user-curated** linear stacks of GitLab
merge requests. You register stack membership once (`stack create` / `stack add`);
`check` and `view` derive chain order from live GitLab source/target
relationships at read time. Restack replays only the affected suffix in an
isolated worktree and publishes rewritten refs with one atomic leased push per
branch. It never merges, never silently resolves conflicts, and never guesses
after an uncertain push.

A stack looks like:

```text
main <── feature-a <── feature-b <── feature-c
         MR !101        MR !102        MR !103
```

Register it as a **named stack** (e.g. `web-migration`) with IIDs `101 102 103`.
The first MR (the **front**) targets the default branch; every successor targets
its predecessor's source branch. v1 supports one chain of 1–10 open MRs in one
project, no forks/cycles/cross-project.

## When to use this skill

Activate when the task involves a GitLab MR stack and any of:

- creating or editing named stack membership;
- checking stack health / alignment / CI;
- viewing stack status (live or cached);
- restacking after `main` or an earlier branch moved;
- interpreting `mrstack` JSON output (dispositions, findings, remediations);
- recovering a paused restack (conflict, empty commit, crash, indeterminate push);
- fetching exact CI logs for a failed pipeline in the stack.

Do **not** use this skill to merge/approve MRs — `mrstack` never does that.

## Prerequisites

Run from the project's Git clone or a worktree. Verify tools:

```bash
mrstack --version
git --version
glab --version
glab auth status
```

If `mrstack` is not installed: `brew install nkaewam/tap/mrstack` (or see
`https://github.com/nkaewam/mrstack`). If `glab` is not authenticated:
`glab auth login --hostname <gitlab-host>`.

Then confirm environment:

```bash
mrstack doctor
```

`doctor` is read-only and never mutates an MR to test permissions. Some
capabilities may legitimately be `unverified`; a later mutation blocked by a
known prerequisite exits `3` (`prerequisite_unsupported`).

## Named stacks

Stacks are persisted under `~/.mrstack/stacks/<name>.json`, bound to the GitLab
host/project where they were created. Stack names are lowercase letters, digits,
and hyphens (1–64 chars).

```bash
# from a clone of the target project
mrstack stack create web-migration
mrstack stack add web-migration 101 102 103
mrstack stack list
mrstack stack remove web-migration 101   # drop one IID
mrstack stack delete web-migration      # requires --yes in machine mode
```

`stack list --all` shows stacks across every project. Membership is only IIDs;
order is derived at `check`/`view` time from MR target/source branches.

**Legacy integrated-predecessor stacks:** when the front MR targets a merged
predecessor branch, include the merged predecessor's IID in the named stack so
`check` can fetch integration evidence, even though only open MRs form the
active component.

## Agent operating mode

Agents and scripts always use machine mode. Both flags are required together:

```bash
mrstack --json --no-input [--remote origin] <command>
```

Every invocation emits exactly one `mrstack/v1` JSON document on stdout
(including failures). Diagnostics go to stderr; there are no prompts.

**Mutations** (`stack delete`, `restack`, `continue`, `abort`, `history prune`)
additionally require `--yes`:

```bash
mrstack --json --no-input --yes --remote origin restack --snapshot <id>
```

Keep `--remote` explicit in automation and multi-remote repos.

### Exit codes are NOT health

| Exit | Meaning |
|---:|---|
| `0` | Authoritative domain result — **includes unhealthy/invalid dispositions**. Inspect `.disposition` and `.findings`. |
| `2` | Invalid invocation/selector (`invalid_input`). |
| `3` | Environment/auth/transport/journal/prerequisite unavailable (`unavailable`). |
| `4` | Unexpected internal invariant failure (`internal`). |

Never treat exit `0` as "healthy". Branch on `.outcome.status`, `.disposition`,
and `.findings[].code`, not on the exit code alone.

## Core loop

### 1. Register (once per stack)

```bash
mrstack --json --no-input --remote origin stack create web-migration
mrstack --json --no-input stack add web-migration 101 102 103
```

### 2. Check and capture a snapshot

```bash
mrstack --json --no-input --remote origin check web-migration > /tmp/check.json
```

`check` requires a **named stack** (`check <name>`) or tracked completion
reconciliation (`check --stack <stack-id>`). Bare `check`, MR IID, and branch
autodiscovery are not supported.

From the result, read:

- `.disposition` — the one control-flow classification (see table below);
- `.stack.snapshot_id` — **opaque, single-use** identity for the next mutation;
- `.stack.selector` — `{ "kind": "named_stack", "value": "<name>" }` for curated stacks;
- `.findings` — typed conditions with stable `code`s;
- `.remediations[].actions[]` — executable transitions as literal `argv` arrays.

A successful `check <name>` also writes a view cache at
`~/.mrstack/view/<name>.json` for offline `view --all`.

### 3. Interpret the disposition

| Disposition | Agent action |
|---|---|
| `ready` | Nothing to do. (Does not assert approvals/merge policy.) |
| `complete` | Stack merged in order. Nothing to do. |
| `action_required` | A deterministic next action exists (restack, repair CI, resolve conflict, refresh checkout). Execute the remediation action. |
| `waiting` | External state still moving (pipeline running, native retarget). Wait and recheck. |
| `human_required` | Needs human judgment/evidence the CLI cannot infer. **Stop and hand off.** Do not script around it. |
| `invalid` | MR relationships do not form a supported stack. |

Precedence when multiple findings coexist:
`invalid > action_required > human_required > waiting > ready > complete`.

### 4. Execute a remediation action (if `action_required`)

Each `actions[]` object contains a literal `argv` array, an explicit `cwd`,
`mutates`/`confirmation_required` flags, `preconditions`, and `requires`
(snapshot/session/plan/pipeline/job identities).

**Rules:**

- Run the `argv` array **verbatim** from the action's `cwd`. Do not construct a
  shell string from remediation output, do not interpolate, do not `cd` then
  run — `cwd` is explicit and `argv[0]` is the executable.
- Add `--json --no-input --yes` for a mutating action (the `argv` already
  includes these when `confirmation_required: true`).
- **Fail closed**: if the action `kind`, a `precondition`, or a required
  identity is unknown, do **not** invoke it. Treat unknown enum/finding codes as
  unsafe for mutation.
- Reuse the exact `snapshot_id` / `session_id` / `plan_id` from the packet that
  produced them. Never substitute a new ID into an old remediation. If
  `remote_changed`, discard the stale packet and run a fresh `check <name>`.
- `recheck` actions run `check <stack-name>` — use the stack name from
  `.stack.selector.value` when `kind` is `named_stack`.

Common action kinds: `start_restack`, `start_planned_restack`,
`continue_restack`, `continue_drop_current`, `continue_keep_empty`,
`abort_restack`, `recover_restack`, `fetch_ci_logs`, `recheck`.

### 5. Recheck

After any publication, retarget, or manual recovery, run a fresh check on the
**same named stack** and use the new snapshot for any further mutation. Snapshot
IDs are not reusable after the observed state changes.

```bash
mrstack --json --no-input --remote origin check web-migration
```

## View

```bash
mrstack view                          # live status for stacks in this repo
mrstack view web-migration            # live status for one stack
mrstack view --all                    # all stacks; cached status when available
mrstack view --all --refresh          # live fetch across every project
```

Without `--refresh`, `view --all` shows membership plus the last `check`
snapshot per stack (note includes `cached from <timestamp>`). Stacks never
checked show membership only with a note to run `check <name>` or `--refresh`.

## Restack workflow

```bash
# capture
mrstack --json --no-input --remote origin check web-migration > /tmp/check.json
SNAP=$(jq -r '.stack.snapshot_id' /tmp/check.json)

# start (only if disposition == action_required and a start_restack action exists)
mrstack --json --no-input --yes --remote origin restack --snapshot "$SNAP"

# after completion, recheck with a fresh snapshot
mrstack --json --no-input --remote origin check web-migration
```

`restack` revalidates the snapshot before replay and before push; rewrites
only the affected suffix in a CLI-managed worktree (never your current
checkout); publishes every affected ref with one `git push --atomic` +
`--force-with-lease` per branch. There is no sequential-push fallback.

### Paused sessions

Replay may pause instead of guessing. Resume with the **same session ID** from
any worktree in the project:

- **Conflict** (`rebase_conflict`): resolve files in the reported managed
  worktree, `git add` the resolutions yourself (`mrstack` never stages for
  you), then `restack continue --session <id>`. Continuing with unresolved or
  unstaged work is rejected.
- **Empty commit** (`empty_commit`): exactly one of `--drop-current` or
  `--keep-empty` is required with `continue`.
- **Signed commits** (`signed_commits`): blocked before the session starts;
  authorize signature loss with `restack ... --allow-signature-loss` (bound to
  the operation, journaled).
- **Crash / uncertain push**: `restack recover --session <id>` is read-only —
  it compares remote refs to the durable old/new map. `all_old` → `continue`
  (revalidates then retries the same atomic push) or `abort`; `all_new` →
  complete/pending retarget; mixed/unexpected → `human_required/
  indeterminate_publication` — do not push guessed repairs. A human may
  `restack abandon --session <id> --accept-current-remote` (unavailable with
  `--no-input`).
- **Discard a session**: `restack abort --session <id>` (before publication).

### Ambiguous layer boundaries

If GitLab/Git cannot establish one exact boundary, `mrstack` refuses to guess.
Two-step manual plan:

```bash
mrstack --json --no-input --remote origin restack plan web-migration \
  --snapshot "$SNAP" --layer-boundary 102=<full-40-or-64-char-sha>
# review data.plan, then:
mrstack --json --no-input --yes --remote origin restack --plan <plan-id>
```

A topology/revision/remote change invalidates the plan.

## CI logs

`check` only honors CI bound to each MR's exact captured source revision. When a
finding pins a failed pipeline and jobs, fetch only those exact IDs (returned
by `check`):

```bash
mrstack --json --no-input ci logs --pipeline 123456 --job 901 --job 902
# optional budget (default 524288, hard max 4194304): --max-bytes 1048576
```

`ci logs` never resolves a moving "latest" pipeline; it requires the exact
pipeline ID and 1–20 exact job IDs. The budget is split evenly across jobs in
request order, keeping the newest tail of each.

## History

```bash
mrstack --json --no-input history 102 --limit 50 [--cursor <opaque>]
mrstack history alias --stack <id> <alias>   # display metadata only
mrstack --json --no-input --yes history prune --before 720h [--stack <id>]
```

History still accepts MR IID, branch, or `--stack <id>` selectors. Pruning
requires `--yes` in machine mode and never removes an unfinished session, active
plan, tracked stack identity, or the newest observation for a tracked stack.

`check --stack <id>` reconciles a **fully merged** tracked stack from journal
history; use it only for completion verification, not for live health checks.

## GitLab modes

Default `--gitlab-mode auto`. GitLab <19.1 = **legacy** (mrstack can retarget
successors after the front merges; a failed retarget after publication becomes
`retarget_pending`, retried via `continue`). GitLab ≥19.1 = **native** (GitLab
owns successor retargeting; mrstack waits). If the server version can't be
read, pass `--gitlab-mode legacy|native` explicitly; a contradiction exits `2`
before observation/mutation.

## Safety invariants (always honor)

- `mrstack` never merges, approves, or auto-resolves conflicts.
- Snapshot, plan, and session IDs are **opaque and single-use**. Never reuse
  across state changes; never fabricate one.
- Only one mutation session may be active per project (`operation_in_progress`).
- `mrstack` refuses to start with `local_work_present` (uncommitted or
  local-only commits on affected branches in any worktree). There is **no**
  v1 override — commit/push, stash, or relocate the work first.
- The authenticated user must have authored every active member
  (`foreign_authored_member`) — there is no override.
- Never parse human text output for automation; always use `--json --no-input`.
- Never execute an action whose `kind`/`precondition`/required identity is
  unknown — fail closed.
- Preserve `.git/mrstack/` while any session is unfinished.

## Reference

For the full finding catalog, action-kind identity rules, session states, the
JSON envelope, and the complete command reference, see
[references/REFERENCE.md](references/REFERENCE.md). Validate machine output
against the [mrstack/v1 JSON Schema](https://github.com/nkaewam/mrstack/blob/main/docs/schema/mrstack-v1.schema.json).
