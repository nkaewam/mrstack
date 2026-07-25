# mrstack v1 acceptance contract

## Test harness

V1 needs three complementary layers:

1. A scripted fake `glab` controls GitLab version, identity, project policy, MR topology, diff versions, pipelines, logs, and target-update responses.
2. Real local Git repositories use one bare remote plus multiple clones and linked worktrees. Receive hooks, atomic-push support, concurrent ref movement, and crash injection are controllable.
3. Small opt-in contract suites run against representative GitLab 18.11 and 19.1+ projects to validate actual API fields, MR refs, pipeline refs, and retarget behavior.

The fake makes state transitions deterministic; it cannot substitute for the live GitLab contract suite.

## P0 release blockers

### Discovery and source of truth

| ID | Scenario | Required result |
|---|---|---|
| D01 | Discover `main <- b1 <- b2 <- b3` from current branch, branch selector, and each MR IID | Every selector produces the same ordered chain, base, revisions, and tracked identity without external mutation |
| D02 | Journal contains an older order after GitLab retargets a member | Live result follows GitLab; journal records the transition and cannot preserve its old order |
| D03 | Fork, cycle, ambiguous edge, cross-project member, non-default base, or 11 members | Typed `invalid` finding; no plan, session, worktree, or push |
| D04 | Front merged on legacy GitLab and exactly one integrated predecessor matches successor target | Active suffix is discovered without using journal authority |
| D05 | Zero or two integrated predecessors match | `human_required/ambiguous_merged_predecessor`; no guess |
| D06 | Open member's source or target branch is absent without a unique integration-proven legacy predecessor and exact historical target head | `invalid/missing_active_branch` |
| D07 | Member closed without merge | `human_required/closed_member`; successor is not skipped |
| D08 | Non-front member merged over an open predecessor | `invalid/out_of_order_merge`; no repair |

### Snapshot and restack

| ID | Scenario | Required result |
|---|---|---|
| R01 | Default branch advances under a three-layer stack | One session replays all three layers in order onto the captured new base and publishes all refs once |
| R02 | Default branch advances again before replay or push | `action_required/remote_changed`; no affected ref published |
| R03 | MR state, target, author, project identity, or default branch changes without a source SHA change | Complete topology revalidation rejects the mutation |
| R04 | Only a mid-stack edge is stale | Only the first stale successor and its successors are replayed; aligned prefix refs and commits remain identical |
| R05 | Exact boundary is unavailable | `human_required/ambiguous_layer_boundary`; no merge-base or patch-ID fallback |
| R06 | Valid evidence sources disagree | `human_required/conflicting_layer_boundary` |
| R07 | Manual boundary override | Bad override fails read-only; good override returns exact commits/refs and snapshot-bound plan; snapshot movement invalidates it |
| R08 | Layer contains merge commit or no commits | Typed human-required refusal before session creation |
| R09 | Conflict in a later layer | All remote refs remain old; one managed session/worktree persists; user worktrees are untouched |
| R10 | Continue with unresolved or unstaged conflict | Continue stages nothing and refuses; explicit staging resumes the same session |
| R11 | Multiple conflict stops | One session pauses and resumes repeatedly without publishing an intermediate prefix |
| R12 | Commit becomes empty | Plain continue refuses; explicit drop and keep paths produce the requested graph and journal decision |
| R13 | Already-applied commit optimization would normally skip a commit | Engine still attempts it and reaches the explicit empty-commit path |
| R14 | Signed commit in affected suffix | Preflight blocks; explicit signature-loss authorization proceeds, journals identities, and never re-signs |
| R15 | Preserve multi-commit layer | Original order, messages, and authors remain; only required commit identities change |

### Publication and local safety

| ID | Scenario | Required result |
|---|---|---|
| P01 | One-ref and multi-ref restacks | Both use `git push --atomic` and an explicit force-with-lease for every ref |
| P02 | One lease moves or receive hook rejects one ref | All affected refs remain old; no sequential fallback |
| P03 | Server rejects atomic push | Exit 3 `prerequisite_unsupported`; no ref or MR update |
| P04 | Affected registered worktree has staged, unstaged, or untracked changes | `human_required/local_work_present`; no bypass |
| P05 | Affected local branch has local-only commits even when not checked out | Same refusal |
| P06 | Unrelated worktree is dirty | It does not block the restack |
| P07 | Successful push with checked-out, safe un-checked-out, and locally moved affected refs | Checked-out/moved refs remain untouched and report `local_checkout_stale`; only safe un-checked-out refs fast-update |
| P08 | Two plausible remotes, triangular push URL, or unmappable alias | `invalid/ambiguous_remote` before worktree creation |
| P09 | Explicit remote maps fetch and push to the authenticated project | It is captured in the snapshot and used |
| P10 | An active member is authored by someone else | Check succeeds; mutation returns `human_required/foreign_authored_member` with no override |

### Merge-strategy and retarget recovery

| ID | Scenario | Required result |
|---|---|---|
| M01 | Squash front into base and optionally delete its source branch | GitLab relationship and exact historical target evidence still qualify the merged predecessor; only successor layers replay and predecessor commits do not reappear |
| M02 | Merge-commit, fast-forward, and rebase variants | Strategy-appropriate GitLab integration revision proves reachability |
| M03 | Exact historical objects cannot be fetched | `human_required/missing_layer_objects` |
| M04 | Branch publication succeeds and legacy MR target update fails | Durable `retarget_pending`; refs are not rolled back; continue retries only the target update |
| M05 | Native GitLab has not yet auto-retargeted | `waiting/native_retarget_pending`; `mrstack` performs no competing update |
| M06 | Native GitLab later retargets | Next check consumes the new GitLab relationship and can restack normally |

### CI

| ID | Scenario | Required result |
|---|---|---|
| C01 | Every active MR has successful exact-current CI and stack is aligned | `ready` |
| C02 | Only an older branch-name-matching pipeline is green | It never counts |
| C03 | Relevant downstream MR pipeline fails | Top-level `action_required` with pinned MR, pipeline, and failed-job IDs |
| C04 | Synthetic merged-results commit has captured source and target as parents | Pipeline may count |
| C05 | Synthetic pipeline association or parents cannot be proven | `human_required/ambiguous_pipeline` |
| C06 | `allow_failure`, blocking manual, nonblocking downstream, and propagated downstream failure | Results follow GitLab's aggregate blocking semantics |
| C07 | No pipeline and observable policy requires one | `waiting/missing_required_pipeline` |
| C08 | No pipeline and policy is observably optional | CI is N/A |
| C09 | Requiredness cannot be observed | `human_required/ci_policy_unknown`, never N/A |
| C10 | Pipeline retry starts after check | `ci logs` still returns only supplied pipeline/job identities |
| C11 | Trace exceeds bound or contains invalid UTF-8 | Default total budget is 524288 bytes, hard maximum is 4194304, no more than 20 exact job IDs are accepted, allocation is deterministic and tail-preserving, and output reports truncation, source-byte counts, and replacement; trace is absent from SQLite |
| C12 | Exact pipeline has an unrecognized aggregate status | `human_required/pipeline_status_unknown`; it never counts as green or failed |

### Concurrency and crash recovery

| ID | Scenario | Required result |
|---|---|---|
| X01 | Two mutations start for one project from different processes or clones | Exactly one durable session; competitor returns `waiting/operation_in_progress` and owner ID |
| X02 | Checks run while a session is paused | Checks remain available; SQLite WAL remains consistent |
| X03 | Process dies during preparation, replay, or conflict before publication | OS lock releases; durable session remains; remote is all-old and session can resume or abort |
| X04 | Process dies immediately before push, after request send, or after server response | Old/new maps already exist; all-old and all-new reconcile without guessing or a second force-push |
| X05 | Refs become mixed or unexpected after publication attempt | `human_required/indeterminate_publication`; new sessions are blocked |
| X06 | Recover an indeterminate session | Command reads refs only and advances only for all-old or all-new |
| X07 | Try abandonment in agent mode, then human mode | Agent mode refuses; human acceptance archives local state without changing refs |
| X08 | SIGINT at every session transition | Exactly one durable, reconcilable state remains |
| X09 | Lost push response later reconciles all-old | Session becomes `publication_ready`; continue revalidates and retries once, while abort remains safe |
| X10 | Snapshot or selected remote changes before publication | Session becomes terminal, nonblocking `invalidated`; it can never resume or publish, and a worktree containing edits is retained until explicit disposal |

### Agent protocol

| ID | Scenario | Required result |
|---|---|---|
| A01 | Every disposition under `--json --no-input` | Exactly one `mrstack/v1` document on stdout, no prompt, exit 0 |
| A02 | Invalid invocation, unavailable dependency, and injected internal failure | Exactly one document and exits 2, 3, and 4 respectively |
| A03 | Names and paths contain permitted shell metacharacters | Remediation uses literal `argv[]` and explicit `cwd`, never a shell string |
| A04 | Unknown field | Older consumer ignores it |
| A05 | Unknown enum or finding code before mutation | Consumer fails closed and does not execute a mutating action |
| A06 | Conflict remediation | Packet identifies layer, original commit, worktree, paths, evidence, required staging, and session-bound continue action |
| A07 | Successful remote publication leaves local checkout stale | Command remains an authoritative success but disposition is `action_required/local_checkout_stale` |
| A08 | Every documented actionable state | At least one schema-valid remediation uses only the frozen remediation/action/precondition vocabulary and carries all required identities |
| A09 | Cross-reference and precedence fixtures | Every finding/evidence/remediation reference resolves, identities agree with stack/session, and top-level disposition equals documented precedence |
| A10 | Full object IDs | Snapshot, lease, plan, session, and remediation SHAs are complete 40- or 64-hex object IDs; abbreviations fail schema validation |
| A11 | Provider payload contains credential-shaped fields, embedded userinfo, diffs, source text, or raw traces | Per-kind serializer/redaction guard rejects them before stdout and journal persistence |
| A12 | Doctor, plan, CI-log, and history command fixtures plus exits 2–4 | Authoritative commands require their discriminated `data` payload; operational failures require non-null `error`, null disposition, and no partial authoritative claim |

## P1 important scenarios

| ID | Scenario | Required result |
|---|---|---|
| H01 | All selected historical members merge in strategy-aware dependency order | `complete` |
| H02 | No open MR and no MR/tracked identity selector | `invalid/no_stack_selected`, not complete |
| H03 | Integration revision or merge order cannot be proven | `human_required/ambiguous_completion` |
| J01 | Repeated unchanged checks | Only `last_seen_at` changes; no duplicate findings or transitions |
| J02 | Finding resolves and later recurs | New finding identity; earlier interval remains queryable |
| J03 | Delete journal | Open live discovery still works; historical identity and operation recovery are lost |
| J04 | Prune | Active plans/sessions, identities, and newest observation remain |
| J05 | New live chain overlaps two historical identities | New lineage identity links both; histories are not silently merged |
| V01 | Versions below 19.1, at 19.1, later major, and unreadable | Correct legacy/native/override behavior |
| V02 | Capability cache identity, Git, `glab`, host, or project changes | Cached doctor result is invalidated |
| V03 | Doctor subprocess/API trace | No MR, remote ref, user ref, or worktree mutation |
| V04 | Readable server version plus contradictory explicit mode | Exit 2 `invalid_arguments`; no check or mutation runs |
| G01 | GitLab mergeability is checking or stale | `waiting/mergeability_checking`; check never claims simulated conflict freedom |
| G02 | GitLab API propagation lags after mrstack push | Proposed all-new refs are recognized; API lag becomes bounded waiting, not foreign movement |
| B01 | Build matrix | GitHub Actions verifies `CGO_ENABLED=0` builds and smoke-runs on macOS/Linux amd64/arm64 |

## Live GitLab contract assertions

The live suites must verify rather than assume:

- `/version` format across 18.11 and 19.1+;
- MR `diff_refs`, version-history, merge, squash, and timestamp fields;
- fetchability and order-insensitive source/target parent identity of merged-results temporary commits;
- project pipeline-required policy visibility at the authenticated role;
- MR pipeline association, job aggregate behavior, and trace retrieval;
- legacy target update permissions and idempotence;
- native successor retarget timing and failure presentation;
- Git transport atomic-push rejection semantics.

Any unavailable or version-dependent field must map to `unverified`, a typed waiting/human result, or an unsupported prerequisite—never silent inference.

## Release gate

V1 is releasable when all P0 fake/local scenarios pass, the GitLab 18.11 contract suite passes against the Agoda instance, JSON fixtures validate against the frozen agent contract, and cross-compiled binaries pass smoke tests. Native-mode behavior remains guarded by its contract suite until a 19.1+ test instance is available.

Ordinary GitHub-hosted workflows must also pass a secret-access audit proving that no Agoda GitLab credential or private job/repository evidence is available to pull-request code.
