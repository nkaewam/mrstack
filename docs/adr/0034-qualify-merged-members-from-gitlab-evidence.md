# Qualify merged members from GitLab evidence

A merged MR is proven integrated into the current default branch only when GitLab reports `state=merged`, supplies `merged_at`, and at least one strategy-appropriate integration revision is an ancestor of the captured default-branch tip:

- `squash_commit_sha` for a squash result;
- `merge_commit_sha` for a merge-commit result; or
- the MR's merged `diff_refs.head_sha` for a fast-forward or rebased result when GitLab supplies no separate merge or squash revision.

Missing, unreachable, or contradictory integration evidence returns a typed human-required result rather than treating the branch name or state alone as proof. Legacy merged-predecessor discovery also requires that MR's recorded target branch to be the default branch and its source branch to exactly match the open successor's target.

Completion additionally requires every member selected from the previously observed chain to have qualifying integration evidence and strictly increasing `merged_at` values in front-to-back dependency order. Missing, equal, or reversed timestamps return `human_required/ambiguous_completion` unless GitLab exposes a more precise authoritative ordering event; an actual later-layer merge before an open predecessor remains `invalid/out_of_order_merge`.

## References

- [GitLab Merge Requests API](https://docs.gitlab.com/api/merge_requests/)
- [GitLab merged results pipelines](https://docs.gitlab.com/ci/pipelines/merged_results_pipelines/)
