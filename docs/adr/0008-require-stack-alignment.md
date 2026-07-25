# Require stack alignment

A stack is healthy within `mrstack`'s scope only when the current base tip is an ancestor of the front and each predecessor tip is an ancestor of its successor. The CLI reports stale ancestry as `action_required/restack_required` even when GitLab says an individual MR is mergeable, trading additional restack churn for deterministic layers, earlier conflict discovery, and reliable behavior across squash merges.
