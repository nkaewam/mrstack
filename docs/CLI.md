# mrstack CLI

For installation, concepts, walkthroughs, recovery procedures, and
troubleshooting, start with the [user guide](USER-GUIDE.md). This document is
the concise command-behavior contract.

V1 commands must run inside a Git clone or worktree of the selected GitLab project. API-only `--repo` operation is outside the v1 interface.

The global `--gitlab-mode auto|legacy|native` flag defaults to `auto`. A legacy or native override is required when the server version cannot be read. When it can be read, an explicit mode must match detection; a contradiction exits 2 `invalid_arguments` before observation or mutation.

`doctor` is externally read-only and reports each prerequisite as `verified`, `unverified`, or `unsupported`. It never changes an MR or remote/user ref to test permission. Unsupported prerequisites block mutation; unverified write behavior is handled inside the real operation's normal recovery guarantees.

A successful `doctor` exits 0 even when its report contains `unsupported`; a mutation later blocked by that prerequisite exits 3.

## V1 commands

```text
mrstack doctor
mrstack check [<MR-or-branch> | --stack <id>]
mrstack restack [<MR-or-branch>] --snapshot <id> [--allow-signature-loss]
mrstack restack plan [<MR-or-branch>] --snapshot <id> --layer-boundary <mr>=<sha>
mrstack restack --plan <id> [--allow-signature-loss]
mrstack restack continue --session <id> [--drop-current | --keep-empty]
mrstack restack abort --session <id>
mrstack restack recover --session <id>
mrstack restack abandon --session <id> --accept-current-remote
mrstack ci logs --pipeline <id> --job <id>... [--max-bytes <n>]
mrstack history [<MR-or-branch> | --stack <id>] [--limit <n>] [--cursor <opaque>]
mrstack history alias --stack <id> (<alias> | --clear)
mrstack history prune --before <timestamp-or-duration> [--stack <id>]
```

`check`, `ci logs`, and `history` are externally read-only. `restack` starts an explicit history-rewriting session. `restack continue` resumes conflict resolution, retries an all-old `publication_ready` session after revalidation, or completes pending retargeting. `restack abort` is available before remote publication and may also acknowledge disposal of retained edits from a terminal invalidated session.

`check` returns `complete` only when an MR or tracked-stack selector identifies a previously observed chain and GitLab confirms every member merged into the default branch in order. No matching open MR without such identity returns `invalid/no_stack_selected`.

`history alias` changes display metadata only. `history prune` requires confirmation, never removes an unfinished session, active plan, tracked-stack identity, or the newest observation for a tracked stack, and may be used with `--yes` in machine mode.

History pages default to 50 observations and accept at most 200. Cursors are opaque and snapshot the journal ordering used by that query.

Manual layer-boundary recovery is two-step: externally read-only `restack plan` validates the override and returns `data.plan` with the exact ordered commits, source branches, affected refs, sanitized remote identity, override, and snapshot-bound `plan_id`; confirmed `restack --plan <id>` starts it. A topology or revision change invalidates the plan.

When replay produces an empty commit, plain `restack continue` refuses to choose for the caller. Exactly one of `--drop-current` or `--keep-empty` is required to resolve that stop, and the choice is journaled.

If any commit in the affected suffix is signed, `restack` stops before creating the session unless `--allow-signature-loss` is supplied. The override is explicit because rewritten commits cannot retain their original signatures.

`restack` also refuses to start with `human_required/local_work_present` when an affected branch has uncommitted changes or local-only commits in any user-managed worktree. V1 has no override for this preflight failure.

Before a project-scoped operation begins, unfinished restack sessions are reconciled against their durable old/new remote ref maps. An all-old map enters `publication_ready`: `restack continue` revalidates the captured snapshot and retries the same atomic leased publication, while `restack abort` is still safe. An all-new map advances to idempotent retargeting or completion. Mixed or otherwise unknowable publication state returns `human_required/indeterminate_publication` and blocks a new restack.

A session whose snapshot changes before publication becomes terminal `invalidated`: it can never publish or resume and does not block a new session. A clean managed worktree is removed automatically; one containing conflict-resolution edits is retained for inspection until the user explicitly aborts that session to discard it.

`restack recover` only rereads refs and recognizes a complete old or new map; it never repairs remote state. Human-only `restack abandon --accept-current-remote` archives an irreconcilable session without changing refs and is unavailable with `--no-input`.

Every command supports human-readable output. Agent callers use the versioned `--json --no-input` interface. Remote or Git-history mutations additionally require `--yes` and the current snapshot, plan, or session identity. Journal-only alias changes are identity-bound; pruning requires `--yes` but no remote snapshot.

In machine mode, every successfully classified stack outcome exits `0`, including unhealthy and invalid dispositions. Exit `2` is invalid invocation/input, `3` is environment/authentication/transport/prerequisite failure, and `4` is an unexpected internal invariant failure.

Suggested agent actions are objects containing a stable action `kind`, `argv` array, mutation and confirmation flags, and required snapshot or session identity. Machine output never asks an agent to evaluate a shell command string.

`ci logs` never resolves a moving “latest” pipeline or job set. It requires the exact pipeline ID and one to twenty exact job IDs returned by `check`. `--max-bytes` is the total source-byte budget for the response, defaults to 524288, and has a hard maximum of 4194304. The budget is divided evenly in request order and keeps the newest tail of each trace; truncation is reported.

Commands accept `--remote <name>`. Without it, only the current branch's configured upstream remote may be selected, and only when it maps unambiguously to the authenticated GitLab project. Observation validates its fetch URL; mutation also requires its effective push URL to map to the same project. Ambiguity returns `invalid/ambiguous_remote`.

A mutation snapshot binds the GitLab project, the selected remote name and credential-free canonical fetch/push project identities, default branch and exact base tip, plus every participating MR's IID, state, source, target, author, and exact source and resolved-target revisions. Every emitted mutation action repeats `--remote <captured-name>`. `mrstack` revalidates all of it before replay, push, and target update; a topology-only or remote-selection change is as stale as a moved branch.
