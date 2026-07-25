# mrstack agent API v1

## Contract

Agent mode is enabled by `--json --no-input`. Every invocation emits exactly one JSON document on stdout, including failures with exit codes 2–4. Diagnostics may go to stderr; prompts and progress rendering are forbidden.

The normative machine-readable envelope is the [mrstack/v1 JSON Schema](schema/mrstack-v1.schema.json).

The media-level version identifier is:

```json
{ "api_version": "mrstack/v1" }
```

Within `mrstack/v1`:

- object fields may be added;
- agents must ignore unknown object fields;
- existing fields, enum meanings, finding meanings, and exit classes may not change;
- IDs are opaque, case-sensitive strings;
- timestamps are RFC 3339 UTC;
- human prose is non-contractual;
- an unknown enum or finding code must be treated as unsafe for mutation, even if it can still be displayed.

The linked JSON Schema is a strict producer-conformance schema for outputs emitted by this version. It intentionally rejects unknown enum values. Consumer decoders follow the forward-compatible rules above: ignore unknown object fields, preserve unknown values for display, and fail closed before mutation. The producer schema is not a tolerant-consumer schema.

## Envelope

Every response has the same top-level shape:

```json
{
  "api_version": "mrstack/v1",
  "generated_at": "2026-07-25T12:34:56Z",
  "command": {
    "name": "check",
    "invocation_id": "cmd_01J..."
  },
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

All top-level keys are required. An inapplicable value is represented by `null`, `[]`, or `{}`, not by omitting the key.

Stable command names are:

```text
doctor
check
restack.start
restack.plan
restack.continue
restack.abort
restack.recover
restack.abandon
ci.logs
history.show
history.alias
history.prune
unknown
```

`unknown` is a parser sentinel used only when no stable command can be identified; it accompanies exit 2 `unknown_command` or `invalid_arguments` and is never accepted as an invocation. `restack.abandon` is named so an attempted machine invocation can return a schema-valid exit 2 `invalid_arguments`; it can never return an authoritative result under `--no-input`.

## Process outcome and exit codes

Process outcome and domain disposition are independent.

| Exit | Outcome class | Meaning |
|---:|---|---|
| 0 | `authoritative` | The command produced a complete domain result, including an unhealthy or invalid result |
| 2 | `invalid_input` | Syntax, arguments, selector form, or command usage is invalid |
| 3 | `unavailable` | Environment, authentication, transport, journal, or prerequisite prevented an authoritative result |
| 4 | `internal` | An unexpected invariant or implementation failure occurred |

`outcome.status` is `succeeded` only for an authoritative result. It is `failed` for exits 2–4. `outcome.retryable` is advice for the operational failure, not an instruction to repeat a mutation without a new snapshot.

Stable operational codes include:

| Class | Codes |
|---|---|
| `invalid_input` | `invalid_arguments`, `invalid_selector`, `unknown_command` |
| `unavailable` | `not_git_repository`, `git_unavailable`, `glab_unavailable`, `authentication_failed`, `gitlab_transport_failed`, `git_transport_failed`, `server_mode_undetermined`, `prerequisite_unsupported`, `journal_unavailable` |
| `internal` | `internal_invariant_failed` |

An `invalid` disposition is an authoritative domain result and exits 0. It is not `invalid_input`.

`doctor` also exits 0 when it authoritatively reports an `unsupported` capability. A later mutation blocked by that known prerequisite uses exit 3 and `prerequisite_unsupported`.

## Disposition

`disposition` is non-null only when an authoritative result has a control-flow classification. Successful data commands such as `doctor`, `ci.logs`, and `history.show` may exit 0 with a null disposition.

```text
action_required
waiting
human_required
ready
complete
invalid
```

When multiple findings coexist, top-level precedence is:

```text
invalid > action_required > human_required > waiting > ready > complete
```

Every finding remains present even when another one determines the top-level disposition.

## Stack snapshot

`stack` is present when a stack or historical stack was authoritatively resolved:

```json
{
  "stack_id": "stk_01J...",
  "alias": null,
  "snapshot_id": "snap_01J...",
  "observed_at": "2026-07-25T12:34:56Z",
  "selector": {
    "kind": "mr",
    "value": "42"
  },
  "gitlab_mode": "legacy",
  "remote": {
    "name": "origin",
    "selection": "upstream",
    "fetch": {
      "host": "gitlab.example.com",
      "project": "team/service"
    },
    "push": {
      "host": "gitlab.example.com",
      "project": "team/service"
    }
  },
  "project": {
    "host": "gitlab.example.com",
    "id": "1234",
    "path_with_namespace": "team/service",
    "web_url": "https://gitlab.example.com/team/service",
    "default_branch": "main"
  },
  "base": {
    "branch": "main",
    "sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  },
  "members": [],
  "affected_suffix": null
}
```

Selector kinds are `current_branch`, `branch`, `mr`, and `tracked_stack`. Remote selection is `upstream` or `explicit`. Remote endpoints contain only canonical host/project identity—never schemes, usernames, embedded credentials, or raw configuration URLs. Project, pipeline, and job IDs are decimal strings; MR IIDs and member positions are JSON integers.

Each member has:

```json
{
  "position": 0,
  "iid": 42,
  "state": "opened",
  "web_url": "https://gitlab.example.com/team/service/-/merge_requests/42",
  "source_branch": "feature/a",
  "target_branch": "main",
  "source_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "target_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "target_resolution": "live_branch",
  "author": {
    "id": "98",
    "username": "nonkaew"
  },
  "layer": {
    "boundary_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "boundary_source": "gitlab_diff_version",
    "commit_count": 2
  },
  "alignment": "aligned",
  "conflict_status": "none",
  "pipeline": null
}
```

Stable member enums are:

- `state`: `opened`, `closed`, `merged`;
- `target_resolution`: `live_branch`, `integrated_predecessor`;
- `alignment`: `aligned`, `stale`, `unknown`;
- `conflict_status`: `none`, `reported`, `checking`, `unknown`;
- `boundary_source`: `gitlab_diff_version`, `journal`, `override`, `unavailable`.

`target_sha` is a complete resolved target object ID. For `live_branch`, it is the captured live target tip. For the legacy-only `integrated_predecessor` case, the named target branch may have been deleted; `target_sha` is then the exact historical target head bound by GitLab MR/diff refs or prior journal evidence, and the merged predecessor's separate integration revision must be proven reachable from the captured base.

The optional pipeline object is:

```json
{
  "applicability": "required",
  "currentness": "exact",
  "kind": "merged_results",
  "id": "9001",
  "sha": "cccccccccccccccccccccccccccccccccccccccc",
  "source_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "target_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "blocking_status": "success",
  "web_url": "https://gitlab.example.com/team/service/-/pipelines/9001",
  "failed_jobs": []
}
```

Pipeline enums are:

- `applicability`: `required`, `not_applicable`, `unknown`;
- `currentness`: `exact`, `missing`, `ambiguous`, `not_applicable`;
- `kind`: `branch`, `detached_mr`, `merged_results`;
- `blocking_status`: `created`, `pending`, `running`, `manual`, `success`, `failed`, `canceled`, `skipped`, `unknown`.

Provider-specific statuses may appear under additive `provider_details`; agents use normalized fields for control flow.

The pipeline object is a normalized assessment, not merely an optional GitLab row:

- `currentness=exact` requires non-null `kind`, `id`, `sha`, `source_sha`, and `web_url`; `source_sha` equals the captured member source. A `merged_results` assessment also requires the exact captured `target_sha`; branch and detached-MR assessments use null `target_sha`.
- `currentness=missing|ambiguous|not_applicable` uses null `kind`, IDs, SHAs, and URL, an empty `failed_jobs`, and `blocking_status=unknown`. `not_applicable` additionally requires `applicability=not_applicable`.
- `pipeline=null` is reserved for a non-active historical member whose CI is not being assessed.
- every failed-job item requires its exact decimal ID, name, status, and credential-free URL.

An exact assessment with `blocking_status=unknown` produces `human_required/pipeline_status_unknown`; it never silently passes or fails.

`affected_suffix` is either null or:

```json
{
  "from_position": 1,
  "member_iids": [43, 44]
}
```

## Findings

A finding is a typed, continuously observed condition:

```json
{
  "finding_id": "fnd_01J...",
  "code": "restack_required",
  "disposition": "action_required",
  "scope": {
    "kind": "member",
    "mr_iid": 43,
    "position": 1,
    "pipeline_id": null,
    "job_id": null,
    "commit_sha": null
  },
  "summary": "Successor does not contain its current predecessor tip.",
  "details": {
    "expected_ancestor_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "source_sha": "cccccccccccccccccccccccccccccccccccccccc"
  },
  "evidence_refs": ["ev_01J..."],
  "first_seen_at": "2026-07-25T12:30:00Z",
  "last_seen_at": "2026-07-25T12:34:56Z"
}
```

`finding_id` remains stable while the same condition is continuously active. Resolution followed by recurrence creates a new ID. Stable scope kinds are `project`, `repository`, `stack`, `member`, `layer`, `pipeline`, `job`, and `session`.

`summary` and `details` are diagnostic and non-contractual. Agents branch on the stable finding code and scope kind, then consume identities and executable transitions only from a schema-valid remediation packet, stack, or session. They must not scrape `details` for an owner ID, path, revision, or command.

### Stable finding catalog

| Disposition | Code | Meaning |
|---|---|---|
| `invalid` | `no_stack_selected` | No live or historical stack was unambiguously selected |
| `invalid` | `ambiguous_relationship` | More than one MR can occupy a chain edge |
| `invalid` | `cyclic_relationship` | Source/target relationships contain a cycle |
| `invalid` | `non_linear_stack` | Relationships contain a fork |
| `invalid` | `cross_project_member` | A source or target belongs to another project |
| `invalid` | `non_default_base` | The front does not terminate at the GitLab default branch |
| `invalid` | `stack_too_deep` | The chain exceeds ten members |
| `invalid` | `missing_active_branch` | An open member's source or target branch is absent |
| `invalid` | `out_of_order_merge` | A non-front member merged above an open predecessor |
| `invalid` | `ambiguous_remote` | Repository remote mapping is unsafe or ambiguous |
| `action_required` | `restack_required` | One or more ancestry edges are stale |
| `action_required` | `merge_conflict` | GitLab reports a current source/target conflict |
| `action_required` | `rebase_conflict` | The managed replay stopped on a Git conflict |
| `action_required` | `pipeline_failed` | A relevant blocking pipeline failed, canceled, or skipped |
| `action_required` | `remote_changed` | A snapshot or plan became stale before publication |
| `action_required` | `empty_commit` | Replay requires an explicit drop-or-keep decision |
| `action_required` | `retarget_pending` | Publication succeeded and legacy target update must be retried |
| `action_required` | `local_checkout_stale` | Remote publication succeeded but a user checkout intentionally remains on old history |
| `human_required` | `ambiguous_merged_predecessor` | Legacy predecessor integration is not unique |
| `human_required` | `closed_member` | A member closed without merging |
| `human_required` | `ambiguous_layer_boundary` | No exact layer boundary is available |
| `human_required` | `conflicting_layer_boundary` | Exact evidence sources disagree |
| `human_required` | `missing_layer_objects` | Exact historical objects cannot be fetched |
| `human_required` | `merge_commit_in_layer` | A v1 layer contains a merge commit |
| `human_required` | `empty_layer` | A selected MR has no layer commits |
| `human_required` | `signed_commits` | Replay requires explicit signature-loss authorization |
| `human_required` | `local_work_present` | An affected local branch has uncommitted or local-only work |
| `human_required` | `foreign_authored_member` | The authenticated user did not author every active member |
| `human_required` | `ambiguous_pipeline` | Pipeline currentness cannot be proven |
| `human_required` | `pipeline_status_unknown` | An exact pipeline's aggregate status cannot be normalized safely |
| `human_required` | `ci_policy_unknown` | Requiredness of absent CI cannot be observed |
| `human_required` | `blocking_manual_job` | Relevant CI awaits a blocking manual job |
| `human_required` | `indeterminate_publication` | Remote refs cannot be classified as all-old or all-new after a publication attempt |
| `human_required` | `ambiguous_completion` | Historical integration or merge order cannot be proven |
| `waiting` | `pipeline_running` | Relevant CI is nonterminal |
| `waiting` | `missing_required_pipeline` | Observable policy requires CI but no current pipeline exists |
| `waiting` | `mergeability_checking` | GitLab conflict status is still calculating or stale |
| `waiting` | `operation_in_progress` | Another command owns the project's active session transition |
| `waiting` | `native_retarget_pending` | GitLab 19.1+ has not yet performed its automatic target update |
| `waiting` | `remote_visibility_pending` | GitLab API has not yet reflected mrstack's atomically published refs |

`ready` and `complete` normally have no blocking finding. Informational provider details do not affect precedence.

## Evidence

Evidence objects are immutable facts referenced by findings and remediation packets:

```json
{
  "evidence_id": "ev_01J...",
  "kind": "git_ancestry",
  "member_iid": 43,
  "source_sha": "cccccccccccccccccccccccccccccccccccccccc",
  "expected_ancestor_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
}
```

Stable evidence kinds include `gitlab_mr`, `gitlab_diff_version`, `git_ancestry`, `git_commit`, `pipeline`, `job`, `remote_ref`, `local_worktree`, and `managed_worktree`. Evidence must not contain credentials, source diffs, file contents, or raw CI logs.

Additive evidence fields are emitted only through per-kind allowlisted serializers followed by a mandatory redaction guard; provider responses are never serialized directly. Producer conformance tests inject credential-shaped keys, URLs, source text, diffs, and traces and require rejection before stdout or journal persistence.

## Remediation packets

Remediations identify semantic work and executable transitions:

```json
{
  "remediation_id": "rem_01J...",
  "finding_id": "fnd_01J...",
  "kind": "restack",
  "snapshot_id": "snap_01J...",
  "session_id": null,
  "plan_id": null,
  "member": {
    "iid": 43,
    "position": 1
  },
  "layer": {
    "mr_iid": 43,
    "boundary_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "commit_sha": null
  },
  "worktree": null,
  "required_work": null,
  "evidence_refs": ["ev_01J..."],
  "actions": [
    {
      "kind": "start_restack",
      "argv": [
        "mrstack",
        "restack",
        "--snapshot",
        "snap_01J...",
        "--remote",
        "origin",
        "--json",
        "--no-input",
        "--yes"
      ],
      "cwd": "/absolute/repository/worktree",
      "mutates": true,
      "confirmation_required": true,
      "preconditions": ["snapshot_current"],
      "requires": {
        "snapshot_id": "snap_01J...",
        "session_id": null,
        "plan_id": null,
        "pipeline_id": null,
        "job_ids": []
      }
    }
  ]
}
```

`argv[0]` is the executable. Every element is literal; no shell interpolation, environment assignment, redirection, or `cd` is implied. `cwd` is explicit. URLs never contain embedded credentials.

`mutates` means the action may change GitLab, remote or user refs, a user or managed worktree, or managed replay history. Private journal/cache updates, object fetches, and recovery observations do not make it true. Thus `recheck` and `recover_restack` are false even though they can record local observations; starting, continuing, or aborting a restack is true.

For conflict resolution, `required_work` describes non-executable semantic work:

```json
{
  "kind": "resolve_and_stage_conflicts",
  "paths": ["src/config.go"],
  "staging": "caller_explicit"
}
```

The action following it may be invoked only after the external agent has satisfied the listed preconditions.

Stable remediation kinds are:

| Kind | Required bound context | Permitted action kinds |
|---|---|---|
| `restack` | snapshot, and plan when applicable | `start_restack`, `start_planned_restack` |
| `resolve_conflict` | session, current layer, managed worktree, conflict paths | `continue_restack`, `abort_restack` |
| `choose_empty_commit` | session and current commit | `continue_drop_current`, `continue_keep_empty`, `abort_restack` |
| `authorize_signature_loss` | snapshot and signed-commit evidence | none until a human authorizes a new `start_restack` invocation |
| `inspect_ci_failure` | exact pipeline and job IDs | `fetch_ci_logs`, `recheck` |
| `recover_publication` | session and complete old/new ref evidence | `recover_restack`, or `continue_restack` only from `publication_ready` |
| `retry_retarget` | session and typed pending target update | `continue_restack` |
| `wait_and_recheck` | stable selector | `recheck` |
| `refresh_local_checkout` | branch, old SHA, and published SHA | none; caller performs ordinary Git work outside `mrstack` |
| `human_handoff` | finding and all supporting evidence | none |

Stable `required_work.kind` values are `resolve_and_stage_conflicts`, `repair_ci_failure`, `choose_empty_commit_outcome`, `obtain_human_decision`, `refresh_local_checkout`, and `wait_for_external_state`. Each is a discriminated object in the JSON Schema; agents reject unknown kinds.

Stable action kinds and identity rules are:

| Action kind | Non-null `requires` identities | Mandatory preconditions |
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

Every other identity in `requires` is null or an empty `job_ids` array. Stable precondition values are exactly those named in the table. Action objects are discriminated by `kind` in the schema; an unknown kind or precondition is unsafe and must not be invoked.

## Restack session

The optional session object is:

```json
{
  "session_id": "rs_01J...",
  "state": "rebase_conflict",
  "snapshot_id": "snap_01J...",
  "plan_id": null,
  "created_at": "2026-07-25T12:35:00Z",
  "updated_at": "2026-07-25T12:35:04Z",
  "remote": {
    "name": "origin",
    "selection": "upstream",
    "fetch": {
      "host": "gitlab.example.com",
      "project": "team/service"
    },
    "push": {
      "host": "gitlab.example.com",
      "project": "team/service"
    }
  },
  "worktree": {
    "path": "/absolute/mrstack/worktrees/rs_01J...",
    "git_state": "rebase_conflict"
  },
  "affected_member_iids": [43, 44],
  "current_layer": {
    "mr_iid": 43,
    "original_commit_sha": "dddddddddddddddddddddddddddddddddddddddd",
    "onto_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "conflicted_paths": ["src/config.go"]
  },
  "publication": {
    "state": "not_started",
    "refs": [
      {
        "branch": "feature/b",
        "old_sha": "cccccccccccccccccccccccccccccccccccccccc",
        "new_sha": null,
        "current_sha": "cccccccccccccccccccccccccccccccccccccccc",
        "classification": "old"
      }
    ]
  },
  "target_update": null,
  "signature_loss_authorized": false,
  "resumable": true,
  "abortable": true
}
```

Stable session states are:

```text
preparing
replaying
rebase_conflict
empty_commit
publication_ready
publication_pending_reconcile
retarget_pending
completed
aborted
invalidated
indeterminate_publication
abandoned
```

Stable publication states are:

```text
not_started
all_old
in_flight_unknown
all_new
indeterminate
```

Per-ref classification is `old`, `new`, or `unexpected`.

A non-null target update is:

```json
{
  "mr_iid": 43,
  "from_target": "feature/a",
  "to_target": "main",
  "expected_source_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "expected_mr_state": "opened",
  "status": "pending",
  "attempt_count": 1,
  "last_attempt_at": "2026-07-25T12:35:20Z"
}
```

`status` is `pending` or `applied`. `retarget_pending` requires a pending object, `publication.state=all_new`, `resumable=true`, and `abortable=false`; its remediation is session-bound `retry_retarget` with `continue_restack`. Completed sessions with an applied update retain it as evidence.

The session schema rejects impossible control combinations. In particular:

- `publication_pending_reconcile` pairs with `in_flight_unknown`;
- `publication_ready` pairs with `all_old`, remains resumable and abortable, and can continue or abort;
- `indeterminate_publication` pairs with `indeterminate`, is recoverable but not abortable by an agent;
- `completed` pairs with `all_new` and is neither resumable nor abortable;
- `aborted` and `abandoned` are neither resumable nor abortable;
- `invalidated` is not resumable and never blocks a new session; it is abortable only while a retained managed worktree awaits explicit disposal.

Every publication ref requires complete old/current object IDs. `new_sha` is non-null once replay has reached `publication_ready`; an `all_new` ref must have `current_sha=new_sha` and classification `new`, while an `all_old` ref must have `current_sha=old_sha` and classification `old`. Cross-field SHA equality is enforced by runtime conformance fixtures in addition to the structural schema.

## Command-specific data

`restack.plan` always returns a `data.plan` key. A successful, null-disposition plan contains:

```json
{
  "plan": {
    "plan_id": "plan_01J...",
    "snapshot_id": "snap_01J...",
    "state": "active",
    "created_at": "2026-07-25T12:34:58Z",
    "remote": {
      "name": "origin",
      "selection": "upstream",
      "fetch": {
        "host": "gitlab.example.com",
        "project": "team/service"
      },
      "push": {
        "host": "gitlab.example.com",
        "project": "team/service"
      }
    },
    "overrides": [
      {
        "mr_iid": 43,
        "boundary_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
      }
    ],
    "layers": [
      {
        "position": 0,
        "mr_iid": 43,
        "source_branch": "feature/b",
        "old_sha": "cccccccccccccccccccccccccccccccccccccccc",
        "boundary_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        "commits": [
          "dddddddddddddddddddddddddddddddddddddddd"
        ]
      }
    ]
  }
}
```

Plan state is `active`, `consumed`, `invalidated`, or `expired`. A plan validation finding returns `"plan": null`; it never returns a partially executable plan. `start_planned_restack` repeats the plan's remote name and binds both plan and snapshot IDs in `requires`.

An authoritative `doctor` returns `data.doctor`:

```json
{
  "doctor": {
    "requested_mode": "auto",
    "detected_mode": "legacy",
    "effective_mode": "legacy",
    "server_version": "18.11.3-ee",
    "git_version": "2.50.1",
    "glab_version": "1.70.0",
    "capabilities": [
      {
        "name": "atomic_push",
        "status": "unverified",
        "summary": "Write behavior is checked only by the real atomic publication."
      }
    ]
  }
}
```

Capability names are `repository_context`, `git`, `glab`, `gitlab_auth`, `server_mode`, `atomic_push`, `target_update`, and `sqlite_journal`; status is `verified`, `unverified`, or `unsupported`. `detected_mode` and `server_version` are null only when an explicit matching-unverifiable override supplies `effective_mode`. Summary is prose.

`history.show` is bounded and cursor-paginated under `data.history`:

```json
{
  "history": {
    "stack_id": "stk_01J...",
    "alias": null,
    "observations": [
      {
        "observation_id": "obs_01J...",
        "observed_at": "2026-07-25T12:34:56Z",
        "snapshot_id": "snap_01J...",
        "disposition": "action_required",
        "member_iids": [42, 43],
        "finding_codes": ["restack_required"]
      }
    ],
    "finding_intervals": [
      {
        "finding_id": "fnd_01J...",
        "code": "restack_required",
        "first_seen_at": "2026-07-25T12:34:56Z",
        "last_seen_at": "2026-07-25T12:35:56Z",
        "resolved_at": null
      }
    ],
    "next_cursor": null
  }
}
```

The default page is 50 observations and the hard maximum is 200. `--cursor` is opaque. `history.alias` returns `data.history_alias` with `stack_id`, nullable `previous_alias`, and nullable resulting `alias`. `history.prune` returns `data.history_prune` with nullable `stack_id`, the normalized UTC `before` timestamp, integer `deleted_observations`, integer `deleted_evidence`, and integer `preserved_records`.

## CI log data

`ci.logs` returns traces only under `data.logs` and reports its deterministic total budget:

```json
{
  "log_request": {
    "pipeline_id": "9001",
    "job_ids": ["9102"]
  },
  "log_budget": {
    "requested_bytes": 524288,
    "effective_bytes": 524288,
    "hard_max_bytes": 4194304,
    "allocation": "equal_per_job_tail"
  },
  "logs": [
    {
      "pipeline_id": "9001",
      "job_id": "9102",
      "job_name": "test",
      "status": "failed",
      "text": "bounded UTF-8 trace",
      "returned_bytes": 262144,
      "total_bytes": null,
      "truncated": true,
      "invalid_utf8_replaced": false
    }
  ]
}
```

At most 20 explicit job IDs are accepted. `--max-bytes` defaults to 524288 and cannot exceed 4194304. The effective total source-byte budget is split as evenly as possible across jobs in request order, and each allocation retains the newest tail bytes. Invalid UTF-8 is replaced with the Unicode replacement character and reported. `returned_bytes` measures retained source bytes before replacement. `total_bytes` is null when the transport does not reveal it.

`log_request` echoes the accepted exact identities. Every returned log entry must use that pipeline and one requested job ID exactly once; the runtime envelope validator rejects omissions, duplicates, or mismatches.

## Compact examples

### Check requiring a restack

```json
{
  "api_version": "mrstack/v1",
  "generated_at": "2026-07-25T12:34:56Z",
  "command": { "name": "check", "invocation_id": "cmd_1" },
  "outcome": {
    "status": "succeeded",
    "class": "authoritative",
    "code": "ok",
    "exit_code": 0,
    "retryable": false
  },
  "disposition": "action_required",
  "stack": {
    "stack_id": "stk_1",
    "alias": null,
    "snapshot_id": "snap_1",
    "observed_at": "2026-07-25T12:34:56Z",
    "selector": { "kind": "mr", "value": "43" },
    "gitlab_mode": "legacy",
    "remote": {
      "name": "origin",
      "selection": "upstream",
      "fetch": {
        "host": "gitlab.example.com",
        "project": "team/service"
      },
      "push": {
        "host": "gitlab.example.com",
        "project": "team/service"
      }
    },
    "project": {
      "host": "gitlab.example.com",
      "id": "1234",
      "path_with_namespace": "team/service",
      "web_url": "https://gitlab.example.com/team/service",
      "default_branch": "main"
    },
    "base": {
      "branch": "main",
      "sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
    },
    "members": [
      {
        "position": 0,
        "iid": 43,
        "state": "opened",
        "web_url": "https://gitlab.example.com/team/service/-/merge_requests/43",
        "source_branch": "feature/b",
        "target_branch": "main",
        "source_sha": "cccccccccccccccccccccccccccccccccccccccc",
        "target_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        "target_resolution": "live_branch",
        "author": { "id": "98", "username": "nonkaew" },
        "layer": {
          "boundary_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
          "boundary_source": "gitlab_diff_version",
          "commit_count": 1
        },
        "alignment": "stale",
        "conflict_status": "none",
        "pipeline": null
      }
    ],
    "affected_suffix": { "from_position": 0, "member_iids": [43] }
  },
  "findings": [
    {
      "finding_id": "fnd_1",
      "code": "restack_required",
      "disposition": "action_required",
      "scope": {
        "kind": "member",
        "mr_iid": 43,
        "position": 0,
        "pipeline_id": null,
        "job_id": null,
        "commit_sha": null
      },
      "summary": "Successor does not contain its predecessor tip.",
      "details": {
        "expected_ancestor_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        "source_sha": "cccccccccccccccccccccccccccccccccccccccc"
      },
      "evidence_refs": ["ev_1"],
      "first_seen_at": "2026-07-25T12:34:56Z",
      "last_seen_at": "2026-07-25T12:34:56Z"
    }
  ],
  "evidence": [
    {
      "evidence_id": "ev_1",
      "kind": "git_ancestry",
      "member_iid": 43,
      "source_sha": "cccccccccccccccccccccccccccccccccccccccc",
      "expected_ancestor_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
    }
  ],
  "remediations": [
    {
      "remediation_id": "rem_1",
      "finding_id": "fnd_1",
      "kind": "restack",
      "snapshot_id": "snap_1",
      "session_id": null,
      "plan_id": null,
      "member": { "iid": 43, "position": 0 },
      "layer": {
        "mr_iid": 43,
        "boundary_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        "commit_sha": null
      },
      "worktree": null,
      "required_work": null,
      "evidence_refs": ["ev_1"],
      "actions": [
        {
          "kind": "start_restack",
          "argv": [
            "mrstack",
            "restack",
            "--snapshot",
            "snap_1",
            "--remote",
            "origin",
            "--json",
            "--no-input",
            "--yes"
          ],
          "cwd": "/absolute/repository/worktree",
          "mutates": true,
          "confirmation_required": true,
          "preconditions": ["snapshot_current"],
          "requires": {
            "snapshot_id": "snap_1",
            "session_id": null,
            "plan_id": null,
            "pipeline_id": null,
            "job_ids": []
          }
        }
      ]
    }
  ],
  "session": null,
  "data": {},
  "error": null
}
```

### Restack paused on a conflict

```json
{
  "api_version": "mrstack/v1",
  "generated_at": "2026-07-25T12:35:04Z",
  "command": { "name": "restack.start", "invocation_id": "cmd_2" },
  "outcome": {
    "status": "succeeded",
    "class": "authoritative",
    "code": "ok",
    "exit_code": 0,
    "retryable": false
  },
  "disposition": "action_required",
  "stack": null,
  "findings": [
    {
      "finding_id": "fnd_2",
      "code": "rebase_conflict",
      "disposition": "action_required",
      "scope": {
        "kind": "layer",
        "mr_iid": 43,
        "position": 1,
        "pipeline_id": null,
        "job_id": null,
        "commit_sha": "dddddddddddddddddddddddddddddddddddddddd"
      },
      "summary": "Replay stopped with a conflict.",
      "details": { "conflicted_paths": ["src/config.go"] },
      "evidence_refs": ["ev_2"],
      "first_seen_at": "2026-07-25T12:35:04Z",
      "last_seen_at": "2026-07-25T12:35:04Z"
    }
  ],
  "evidence": [
    {
      "evidence_id": "ev_2",
      "kind": "managed_worktree",
      "path": "/tmp/mrstack/rs_1",
      "git_state": "rebase_conflict"
    }
  ],
  "remediations": [
    {
      "remediation_id": "rem_2",
      "finding_id": "fnd_2",
      "kind": "resolve_conflict",
      "snapshot_id": "snap_1",
      "session_id": "rs_1",
      "plan_id": null,
      "member": { "iid": 43, "position": 1 },
      "layer": {
        "mr_iid": 43,
        "boundary_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        "commit_sha": "dddddddddddddddddddddddddddddddddddddddd"
      },
      "worktree": {
        "path": "/tmp/mrstack/rs_1",
        "git_state": "rebase_conflict"
      },
      "required_work": {
        "kind": "resolve_and_stage_conflicts",
        "paths": ["src/config.go"],
        "staging": "caller_explicit"
      },
      "evidence_refs": ["ev_2"],
      "actions": [
        {
          "kind": "continue_restack",
          "argv": [
            "mrstack",
            "restack",
            "continue",
            "--session",
            "rs_1",
            "--remote",
            "origin",
            "--json",
            "--no-input",
            "--yes"
          ],
          "cwd": "/absolute/repository/worktree",
          "mutates": true,
          "confirmation_required": true,
          "preconditions": [
            "session_state_current",
            "conflicts_resolved_and_staged"
          ],
          "requires": {
            "snapshot_id": null,
            "session_id": "rs_1",
            "plan_id": null,
            "pipeline_id": null,
            "job_ids": []
          }
        }
      ]
    }
  ],
  "session": {
    "session_id": "rs_1",
    "state": "rebase_conflict",
    "snapshot_id": "snap_1",
    "plan_id": null,
    "created_at": "2026-07-25T12:35:00Z",
    "updated_at": "2026-07-25T12:35:04Z",
    "remote": {
      "name": "origin",
      "selection": "upstream",
      "fetch": {
        "host": "gitlab.example.com",
        "project": "team/service"
      },
      "push": {
        "host": "gitlab.example.com",
        "project": "team/service"
      }
    },
    "worktree": {
      "path": "/tmp/mrstack/rs_1",
      "git_state": "rebase_conflict"
    },
    "affected_member_iids": [43],
    "current_layer": {
      "mr_iid": 43,
      "original_commit_sha": "dddddddddddddddddddddddddddddddddddddddd",
      "onto_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      "conflicted_paths": ["src/config.go"]
    },
    "publication": {
      "state": "not_started",
      "refs": [
        {
          "branch": "feature/b",
          "old_sha": "cccccccccccccccccccccccccccccccccccccccc",
          "new_sha": null,
          "current_sha": "cccccccccccccccccccccccccccccccccccccccc",
          "classification": "old"
        }
      ]
    },
    "target_update": null,
    "signature_loss_authorized": false,
    "resumable": true,
    "abortable": true
  },
  "data": {},
  "error": null
}
```

### Operational failure

```json
{
  "api_version": "mrstack/v1",
  "generated_at": "2026-07-25T12:36:00Z",
  "command": { "name": "check", "invocation_id": "cmd_3" },
  "outcome": {
    "status": "failed",
    "class": "unavailable",
    "code": "gitlab_transport_failed",
    "exit_code": 3,
    "retryable": true
  },
  "disposition": null,
  "stack": null,
  "findings": [],
  "evidence": [],
  "remediations": [],
  "session": null,
  "data": {},
  "error": {
    "message": "GitLab could not be queried through glab.",
    "tool": "glab",
    "tool_exit_code": 1
  }
}
```
