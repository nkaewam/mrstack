# Stacked Merge Requests

This context defines the language for coordinating dependent GitLab merge requests as one unit of work.

## Language

**Stack**:
A strictly ordered, linear chain of one to ten open merge requests from one GitLab project in which each merge request depends on the one immediately before it. Forks, cycles, cross-project members, and longer chains are not stacks.
_Avoid_: Branching stack, merge-request graph

**Base**:
The project's GitLab default branch and the target branch of the front merge request. A non-default terminal target is not a base in v1.
_Avoid_: Trunk, root branch

**Predecessor**:
The merge request immediately before another merge request in a stack; its source branch is the latter merge request's target branch. The first merge request has no predecessor.
_Avoid_: Parent MR, base MR

**Merged Predecessor**:
A former predecessor MR whose source branch is still the target of an open successor after the predecessor has merged. Legacy-mode advancement follows it only when GitLab identifies one unambiguous MR, its merge result is present in the current base, and the successor's historical target head is exactly recoverable even if the old branch was deleted.
_Avoid_: Journal predecessor, guessed merged branch

**Out-of-Order Merge**:
A topology violation in which a non-front MR merged while one or more predecessors remained open. It is invalid and requires human restructuring rather than automated restacking.
_Avoid_: Early merge, automatic recovery

**Successor**:
The merge request immediately after another merge request in a stack; its target branch is the former merge request's source branch. The last merge request has no successor.
_Avoid_: Child MR, dependent MR

**Front**:
The first active merge request in a stack. Its target is the stack's base branch.
_Avoid_: Head MR, bottom MR, base MR

**Aligned**:
A stack condition in which the current base tip is an ancestor of the front and every predecessor tip is an ancestor of its successor. GitLab may consider an unaligned merge request mergeable, but the stack is not ready.
_Avoid_: Up to date, mergeable

**Affected Suffix**:
The first unaligned layer and every successor above it. A restack rewrites this suffix while preserving the aligned prefix below it.
_Avoid_: Entire stack, changed branches

**Restack**:
An explicit operation that realigns successor branches with their current predecessors while preserving stack order. A restack may rewrite branch history, but detecting an unhealthy stack never initiates one implicitly.
_Avoid_: Automatic repair, sync

**Restack Session**:
A durable, resumable restack state machine isolated in a CLI-managed worktree for one project. It records old and proposed remote ref maps before publication, survives process failure, and remains active until completion, safe abort, or explicit recovery.
_Avoid_: User worktree, temporary checkout

**Restack Plan**:
A read-only, snapshot-bound preview required when a caller supplies a manual layer boundary. It identifies the exact commits and branches that would be replayed and produces the `plan_id` required to start that exceptional restack.
_Avoid_: Restack session, dry-run rebase

**Indeterminate Publication**:
A recovery state in which remote refs do not collectively match either a session's complete old ref map or its complete proposed new ref map. `mrstack` performs no further remote mutation and blocks new restacks until the condition is explicitly resolved.
_Avoid_: Partial success, assumed push failure

**Session Abandonment**:
A human-only acknowledgement that an indeterminate session cannot be reconciled. It archives local recovery state and accepts the current remote for the next fresh check without changing remote refs.
_Avoid_: Rollback, remote repair, agent retry

**Local Work**:
Staged, unstaged, or untracked changes in a registered worktree on an affected source branch, or local-only commits on any affected local branch ref. Its presence blocks restacking until it is committed and pushed, stashed, or moved.
_Avoid_: Remote layer, managed-session changes

**Layer**:
The change set introduced by one merge request relative to its predecessor, or relative to the base branch for the first merge request. A layer may contain one or more linear commits, contains no merge commits in v1, and preserves their order, messages, and authorship when restacking rewrites them.
_Avoid_: MR diff, branch contents

**Layer Boundary**:
The exact predecessor or base revision from which a layer's commits begin. It must be supported by an observed snapshot, GitLab MR diff history, or an explicit one-session override rather than inferred heuristically.
_Avoid_: Merge base, guessed parent

**Empty Commit**:
A layer commit whose change is already present in the new ancestry during replay. Restacking pauses until the caller explicitly preserves or drops it; `mrstack` never makes that choice silently.
_Avoid_: Redundant commit, automatically skipped commit

**Signature Loss**:
The unavoidable removal of a commit's cryptographic signature when restacking changes that commit's identity. Signed commits require an explicit, journaled authorization before their layer can be rewritten.
_Avoid_: Signature preservation, automatic re-signing

**Journal**:
The local, per-user history of stack observations and CLI actions shared by the user's CLI invocations and coding agents on one machine. It preserves history but never determines current stack membership or order.
_Avoid_: Stack registry, manifest, source of truth

**Tracked Stack**:
The journal's historical identity for an automatically discovered stack. It retains observed members as the live stack gains successors or loses a merged prefix, but never controls those relationships.
_Avoid_: Registered stack, named stack

**Complete Stack**:
A previously observed stack for which current GitLab state confirms that every member merged into the default branch in dependency order. Absence of open MRs by itself is not completion.
_Avoid_: No MR found, empty stack

**Integration Revision**:
A GitLab-reported squash, merge, or merged-head commit that proves a merged MR's result is reachable from the captured default-branch tip.
_Avoid_: Source branch name, merged state alone

**Check**:
One externally read-only reconciliation cycle that refreshes GitLab state, may fetch objects or private CLI refs, records an observation in the journal, and returns the stack snapshot and actionable findings. Repeating checks is the caller's responsibility.
_Avoid_: Watcher cycle, daemon tick

**Externally Read-only**:
An operation that may read GitLab, fetch Git objects, and update private journal or recovery state but never changes GitLab, remote refs, user branch refs, or user worktrees.
_Avoid_: No local writes, no caching

**Snapshot**:
A complete observed stack state containing the GitLab project, credential-free selected remote identity, default branch and exact base tip, plus every member's IID, state, source, target, author, and exact source and resolved-target revisions. A mutation snapshot becomes stale when any captured topology, remote identity, or revision changes.
_Avoid_: Cache, local state

**Mutation Remote**:
The explicitly resolved Git remote from which stack refs are fetched and to which rewritten refs are atomically pushed. Its fetch and push URLs must both identify the stack's authenticated GitLab project.
_Avoid_: Origin, first remote, guessed remote

**Repository Context**:
The Git clone or worktree from which every v1 command runs. It anchors project selection, ancestry, local-work inspection, and remote resolution.
_Avoid_: API-only context, arbitrary directory

**Capability Result**:
An externally read-only `doctor` assessment of one prerequisite as `verified`, `unverified`, or `unsupported`. Unverified means the behavior can only be established safely during a real operation, not that the check passed.
_Avoid_: Guaranteed permission, destructive probe

**Disposition**:
The control-flow outcome of an authoritative domain result, classifying it as `action_required`, `waiting`, `human_required`, `ready`, `complete`, or `invalid`. It remains independent of whether the CLI process itself succeeded and can describe a check, rejected preflight, or paused session.
_Avoid_: Process exit code, command success

**Ready**:
A disposition meaning the stack is valid, aligned, and green under `mrstack`'s CI scope. It does not assert that approvals or every GitLab merge policy permit merging.
_Avoid_: Mergeable, approved

**Command Outcome**:
The operational result of invoking `mrstack`, kept separate from stack disposition so a successful observation of an unhealthy stack is not mistaken for a CLI failure.
_Avoid_: Stack health, disposition

**Remediation Packet**:
The machine-readable handoff from the CLI to an external coding agent, identifying the finding, affected layer, session and snapshot, managed worktree when applicable, evidence references, and allowed structured actions. Actions carry argument arrays and safety metadata rather than shell command strings.
_Avoid_: Agent prompt, task description, shell snippet

**Relevant Pipeline**:
A pipeline bound to an MR's exact captured source revision and, for a synthetic merged-results pipeline, its exact captured target revision as well. The synthetic commit's two-parent identity is checked without depending on parent order. An older successful pipeline or branch-name match never represents the current layer.
_Avoid_: Latest pipeline, branch pipeline, SHA-only match

**Blocking Pipeline Status**:
GitLab's aggregate result for a relevant pipeline after its own `allow_failure` and downstream-propagation rules. `mrstack` consumes this result rather than independently redefining the CI job graph.
_Avoid_: Every failed job, recursive job status

**CI Evidence**:
Bounded job-log output retrieved from the exact pipeline and job identifiers returned by a check. It reports truncation explicitly and is never persisted in the journal.
_Avoid_: Latest logs, journaled logs, unbounded trace
