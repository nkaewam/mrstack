# Serialize mutations per project

Concurrent checks are allowed using the journal's SQLite WAL mode. The journal durably permits only one unfinished restack session per GitLab project, while an OS-level advisory lock serializes individual state transitions and remote writes. A competing mutation returns `waiting/operation_in_progress` with the owning session identity and never steals a live lock. Process termination releases the OS lock but not the durable session, which is reconciled according to ADR-0030 before another mutation begins.
