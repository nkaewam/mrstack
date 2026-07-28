# mrstack reference

Detailed reference for the `mrstack` agent API v1. Validate all machine output
against the [mrstack/v1 JSON Schema](https://github.com/nkaewam/mrstack/blob/main/docs/schema/mrstack-v1.schema.json).

## Command reference

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

Global options: `--remote <name>`, `--gitlab-mode auto|legacy|native`,
`--json` (with `--no-input`), `--no-input` (with `--json`), `--yes` (mutations),
`-h/--help`, `--version`.

Read-only commands: `doctor`, `check`, `ci logs`, `history`, `restack plan`,
`restack recover`. History-rewriting: `restack` (start), `restack --plan`,
`restack continue`. `restack abort` is available before remote publication.
`restack abandon` is human-only (unavailable with `--no-input`).

## Envelope

Every response has the same required top-level keys (inapplicable = `null`,
`[]`, or `{}`, never omitted):

```json
{
  "api_version": "mrstack/v1",
  "generated_at": "2026-07-25T12:34:56Z",
  "command": { "name": "check", "invocation_id": "cmd_01J..." },
  "outcome": {
    "status": "succeeded",
    "class": "authoritative",
    "code": "ok",
    "exit_code": 0,
    "retryable": false
  },
  "disposition": "action_required",
  "stack": null,
  "findings": [],
  "evidence": [],
  "remediations": [],
  "session": null,
  "data": {},
  "error": null
}
```

Stable command names: `doctor`, `check`, `restack.start`, `restack.plan`,
`restack.continue`, `restack.abort`, `restack.recover`, `restack.abandon`,
`ci.logs`, `history.show`, `history.alias`, `history.prune`, `unknown`.

### Outcome classes

| Exit | `outcome.class` | Codes |
|---:|---|---|
| 0 | `authoritative` | `ok` (any domain disposition, including unhealthy/invalid) |
| 2 | `invalid_input` | `invalid_arguments`, `invalid_selector`, `unknown_command` |
| 3 | `unavailable` | `not_git_repository`, `git_unavailable`, `glab_unavailable`, `authentication_failed`, `gitlab_transport_failed`, `git_transport_failed`, `server_mode_undetermined`, `prerequisite_unsupported`, `journal_unavailable` |
| 4 | `internal` | `internal_invariant_failed` |

`outcome.retryable` is operational advice, not a license to repeat a mutation
without a fresh snapshot. An `invalid` disposition is an authoritative domain
result (exit 0), not `invalid_input`.

## Dispositions and precedence

`invalid > action_required > human_required > waiting > ready > complete`

Every finding remains present even when another determines the top-level
disposition. `ready`/`complete` normally have no blocking finding.

## Finding catalog

Branch on the stable `code`, not on `summary`/`details` (those are
non-contractual). Consume identities and transitions only from a schema-valid
remediation packet, stack, or session — never scrape `details` for an owner ID,
path, revision, or command.

| Disposition | Code | Meaning / response |
|---|---|---|
| `invalid` | `no_stack_selected` | No live/historical stack unambiguously selected |
| `invalid` | `ambiguous_relationship` | More than one MR can occupy a chain edge |
| `invalid` | `cyclic_relationship` | Source/target relationships contain a cycle |
| `invalid` | `non_linear_stack` | Relationships contain a fork |
| `invalid` | `cross_project_member` | A source/target belongs to another project |
| `invalid` | `non_default_base` | Front does not terminate at the default branch |
| `invalid` | `stack_too_deep` | Chain exceeds ten members |
| `invalid` | `missing_active_branch` | An open member's source/target branch is absent |
| `invalid` | `out_of_order_merge` | A non-front member merged above an open predecessor |
| `invalid` | `ambiguous_remote` | Repository remote mapping is unsafe/ambiguous |
| `action_required` | `restack_required` | One or more ancestry edges are stale → start restack |
| `action_required` | `merge_conflict` | GitLab reports a current source/target conflict |
| `action_required` | `rebase_conflict` | Managed replay stopped on a Git conflict |
| `action_required` | `pipeline_failed` | A relevant blocking pipeline failed/canceled/skipped |
| `action_required` | `remote_changed` | Snapshot/plan became stale before publication → fresh check |
| `action_required` | `empty_commit` | Replay requires explicit drop-or-keep decision |
| `action_required` | `retarget_pending` | Publication succeeded; legacy target update must be retried |
| `action_required` | `local_checkout_stale` | Publication succeeded; refresh reported local branch manually |
| `human_required` | `ambiguous_merged_predecessor` | Legacy predecessor integration not unique |
| `human_required` | `closed_member` | A member closed without merging |
| `human_required` | `ambiguous_layer_boundary` | No exact layer boundary available |
| `human_required` | `conflicting_layer_boundary` | Exact evidence sources disagree |
| `human_required` | `missing_layer_objects` | Exact historical objects cannot be fetched |
| `human_required` | `merge_commit_in_layer` | A v1 layer contains a merge commit |
| `human_required` | `empty_layer` | A selected MR has no layer commits |
| `human_required` | `signed_commits` | Replay requires explicit signature-loss authorization |
| `human_required` | `local_work_present` | Affected local branch has uncommitted/local-only work |
| `human_required` | `foreign_authored_member` | Authenticated user did not author every active member |
| `human_required` | `ambiguous_pipeline` | Pipeline currentness cannot be proven |
| `human_required` | `pipeline_status_unknown` | Exact pipeline's aggregate status cannot be normalized safely |
| `human_required` | `ci_policy_unknown` | Requiredness of absent CI cannot be observed |
| `human_required` | `blocking_manual_job` | Relevant CI awaits a blocking manual job |
| `human_required` | `indeterminate_publication` | Remote refs cannot be classified all-old/all-new after a push |
| `human_required` | `ambiguous_completion` | Historical integration/merge order cannot be proven |
| `waiting` | `pipeline_running` | Relevant CI is nonterminal → wait and recheck |
| `waiting` | `missing_required_pipeline` | Policy requires CI but no current pipeline exists |
| `waiting` | `mergeability_checking` | GitLab conflict status still calculating/stale |
| `waiting` | `operation_in_progress` | Another command owns the project's active session |
| `waiting` | `native_retarget_pending` | GitLab 19.1+ has not yet performed automatic target update |
| `waiting` | `remote_visibility_pending` | GitLab API has not yet reflected atomically published refs |

Finding scope kinds: `project`, `repository`, `stack`, `member`, `layer`,
`pipeline`, `job`, `session`. `finding_id` is stable while the same condition
is continuously active; resolution + recurrence creates a new ID.

## Remediation kinds

Each remediation binds a finding to executable `actions[]`. `kind` selects the
required bound context and permitted action kinds:

| Kind | Required context | Permitted actions |
|---|---|---|
| `restack` | snapshot, plan when applicable | `start_restack`, `start_planned_restack` |
| `resolve_conflict` | session, current layer, managed worktree, conflict paths | `continue_restack`, `abort_restack` |
| `choose_empty_commit` | session, current commit | `continue_drop_current`, `continue_keep_empty`, `abort_restack` |
| `authorize_signature_loss` | snapshot, signed-commit evidence | none until a human authorizes a new `start_restack` |
| `inspect_ci_failure` | exact pipeline + job IDs | `fetch_ci_logs`, `recheck` |
| `recover_publication` | session, complete old/new ref evidence | `recover_restack`, or `continue_restack` only from `publication_ready` |
| `retry_retarget` | session, typed pending target update | `continue_restack` |
| `wait_and_recheck` | stable selector | `recheck` |
| `refresh_local_checkout` | branch, old SHA, published SHA | none; caller does ordinary Git work outside `mrstack` |
| `human_handoff` | finding + all supporting evidence | none |

`required_work` (non-executable semantic work) kinds:
`resolve_and_stage_conflicts`, `repair_ci_failure`, `choose_empty_commit_outcome`,
`obtain_human_decision`, `refresh_local_checkout`, `wait_for_external_state`.
Each is a discriminated object; agents reject unknown kinds. The action
following `required_work` may run only after the agent has satisfied the listed
preconditions.

## Action kinds and identity rules

`argv` is a literal array (`argv[0]` = executable); no shell interpolation,
env assignment, redirection, or implied `cd` — `cwd` is explicit. URLs never
contain embedded credentials. `mutates` means the action may change GitLab,
remote/user refs, a worktree, or replay history (private journal/cache updates
and object fetches do not set it).

| Action kind | Non-null `requires` | Mandatory preconditions |
|---|---|---|
| `start_restack` | `snapshot_id` | `snapshot_current` |
| `start_planned_restack` | `snapshot_id`, `plan_id` | `snapshot_current`, `plan_current` |
| `continue_restack` | `session_id` | `session_state_current`, plus `conflicts_resolved_and_staged` or `remote_all_old` when applicable |
| `continue_drop_current` | `session_id` | `session_state_current`, `empty_commit_current` |
| `continue_keep_empty` | `session_id` | `session_state_current`, `empty_commit_current` |
| `abort_restack` | `session_id` | `session_state_current`, `no_remote_publication` |
| `recover_restack` | `session_id` | `session_state_current` |
| `fetch_ci_logs` | `pipeline_id`, non-empty `job_ids` | `pipeline_and_jobs_pinned` |
| `recheck` | none | `repository_context_current` |

Every other identity in `requires` is null or an empty `job_ids` array. An
unknown action `kind` or precondition is unsafe — do not invoke.

## Restack session

Session states: `preparing`, `replaying`, `rebase_conflict`, `empty_commit`,
`publication_ready`, `publication_pending_reconcile`, `retarget_pending`,
`completed`, `aborted`, `invalidated`, `indeterminate_publication`, `abandoned`.

Publication states (per ref): `not_started`, `all_old`, `in_flight_unknown`,
`all_new`, `indeterminate`. Per-ref classification: `old`, `new`, `unexpected`.

Impossible-combination guards enforced by the schema + runtime fixtures:

- `publication_pending_reconcile` pairs with `in_flight_unknown`;
- `publication_ready` pairs with `all_old`, is resumable + abortable (continue or abort);
- `indeterminate_publication` pairs with `indeterminate` — recoverable, **not** agent-abortable;
- `completed` pairs with `all_new`, neither resumable nor abortable;
- `aborted`/`abandoned` are neither resumable nor abortable;
- `invalidated` is not resumable and never blocks a new session; abortable only
  while a retained managed worktree awaits explicit disposal.

`retarget_pending` requires a pending `target_update`, `publication.state=all_new`,
`resumable=true`, `abortable=false`; remediation is session-bound
`retry_retarget` via `continue_restack`. `restack.abandon` can never return an
authoritative result under `--no-input` (a machine invocation returns a
schema-valid exit 2 `invalid_arguments`).

## Stack member enums

- `state`: `opened`, `closed`, `merged`
- `target_resolution`: `live_branch`, `integrated_predecessor`
- `alignment`: `aligned`, `stale`, `unknown`
- `conflict_status`: `none`, `reported`, `checking`, `unknown`
- `boundary_source`: `gitlab_diff_version`, `journal`, `override`, `unavailable`

Pipeline enums: `applicability` (`required`/`not_applicable`/`unknown`),
`currentness` (`exact`/`missing`/`ambiguous`/`not_applicable`), `kind`
(`branch`/`detached_mr`/`merged_results`), `blocking_status`
(`created`/`pending`/`running`/`manual`/`success`/`failed`/`canceled`/`skipped`/`unknown`).

An exact assessment with `blocking_status=unknown` yields
`human_required/pipeline_status_unknown` — it never silently passes or fails.
`pipeline=null` is reserved for a non-active historical member whose CI is not
assessed.

## Selector and remote identity

Selector kinds: `current_branch`, `branch`, `mr`, `tracked_stack`. Remote
selection is `upstream` or `explicit`. Remote endpoints carry only canonical
host/project identity — never schemes, usernames, embedded credentials, or raw
config URLs. Project/pipeline/job IDs are decimal strings; MR IIDs and member
positions are JSON integers. A mutation snapshot binds the GitLab project, the
selected remote name and credential-free canonical fetch/push identities, the
default branch and exact base tip, and every participating MR's IID, state,
source, target, author, and exact source/resolved-target revisions. Every
emitted mutation action repeats `--remote <captured-name>`. `mrstack`
revalidates all of it before replay, push, and target update; a topology-only
or remote-selection change is as stale as a moved branch.

## Forward compatibility

Within `mrstack/v1`: object fields may be added (agents must ignore unknown
object fields); existing fields, enum meanings, finding meanings, and exit
classes may not change; IDs are opaque, case-sensitive; timestamps are RFC
3339 UTC; human prose is non-contractual. An unknown enum or finding code is
unsafe for mutation even if displayable. The published JSON Schema is a strict
producer-conformance schema (it rejects unknown enums); consumer decoders
follow the forward-compatible rules above and fail closed before mutation.
