# Persist restack sessions for crash recovery

A restack session is a durable project-scoped state machine, not merely an operating-system lock or a live process. The journal enforces at most one unfinished session per project, while an OS lock serializes each individual command that reads or advances that session.

Before publishing, `mrstack` durably records the captured topology, the complete old remote ref map, the complete proposed new ref map, and any pending target-branch update. This record is committed before `git push --atomic` begins. Project-scoped commands reconcile unfinished sessions against GitLab and the remote refs before starting new work:

- if every participating ref still has its old revision, publication did not occur and the session enters `publication_ready`; continue revalidates and retries the same atomic leased push, while abort remains safe;
- if every participating ref has its proposed new revision, publication succeeded and the session advances idempotently to retargeting or completion;
- if refs are split between old and new revisions, or contain unexpected revisions that make publication unknowable, the session becomes `human_required/indeterminate_publication`;
- no new restack session may start while an indeterminate session remains unresolved.

This makes a lost process, terminal, or command response recoverable without guessing whether a force-push succeeded. The durable record tracks operation recovery only; current stack membership and order still come exclusively from GitLab.

The session durably enters `publication_pending_reconcile` immediately before spawning the push process. A remote change detected before that transition is the ordinary `action_required/remote_changed` snapshot failure. After that transition, an unexpected ref map is `human_required/indeterminate_publication`, because the process may have lost the server's response after publication.

A topology, revision, or selected-remote change before publication makes the session terminal `invalidated`. It can never resume or publish and no longer blocks a new project session. A clean managed worktree is removed automatically; one containing conflict-resolution edits remains inspectable until explicit abort acknowledges disposal.

When the Git remote is all-new but GitLab's MR API still reports the captured old source revisions, the session returns `waiting/remote_visibility_pending` and retries observation without pushing again. It advances only after GitLab reports the proposed revisions; an unrelated topology change remains a stale or human-required result.

For an indeterminate session, `restack recover --session <id>` is remote-read-only: it refreshes the current ref map and advances recovery only if the map has become entirely old or entirely new. It never tries to manufacture either state. If the state cannot be made determinate, human-only `restack abandon --session <id> --accept-current-remote` archives the failed session and removes its managed worktree without changing any remote ref. Machine mode cannot abandon an indeterminate session; after abandonment, a fresh check treats GitLab and the remote as current truth.
