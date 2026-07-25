# Keep a local observational journal

The CLI will keep a local per-user journal, initially backed by SQLite and keyed by GitLab host and project identity, so the user's coding agents can retain stack history across invocations and worktrees. GitLab source and target branch relationships remain authoritative for the live stack; the journal records observed membership, remote revision and disposition transitions, findings appearing or resolving, and mutating actions, while unchanged checks only advance `last_seen_at`. It never stores source content, diffs, or CI logs and is not committed or synchronized to teammates.

`history alias` may attach display-only metadata to a tracked identity. Confirmed `history prune` removes eligible old observations and completed actions but never an unfinished session, active plan, tracked identity, or the newest observation for a stack. The journal may still be deleted wholesale without affecting live stack discovery, though historical selection and recovery information are then lost.

If a newly observed live chain overlaps exactly one tracked identity, that identity continues. If it overlaps more than one historical identity, the journal creates a new identity linked to each predecessor identity instead of silently merging their histories; this lineage affects history presentation only.
