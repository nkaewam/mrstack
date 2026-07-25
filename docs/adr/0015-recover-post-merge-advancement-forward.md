# Recover post-merge advancement forward

Because Git ref updates and GitLab MR target updates cannot share one transaction, post-merge advancement will atomically publish the rewritten suffix first and then retarget the new front to the base. This begins only after GitLab identifies a unique merged predecessor whose merge result is present in the current base. If retargeting fails, the restack session remains `retarget_pending` and `restack continue` retries that idempotent update; the CLI will not roll back already published history with another force push.
