# Gate mutations on capability probes

Compatibility will be assessed by an externally read-only `doctor` command. It inspects the authenticated host and project, server version, `glab api`, GitLab MR diff-version API, observable target-update permissions, local Git/worktree support, and advertised or dry-run atomic-push behavior without changing an MR, remote/user ref, or user worktree merely to test access.

Each capability is reported as `verified`, `unverified`, or `unsupported`. A known unsupported prerequisite blocks mutation. A behavior that cannot be proven without a real write remains `unverified` and is attempted only within the normal snapshot, atomicity, and durable-session safety envelope; for example, an actual target-update failure leaves `retarget_pending`, while a server that rejects `git push --atomic` publishes no refs. Capability records are invalidated when the host, project, authentication identity, or relevant tool versions change.

Target-update permission is `unverified` unless GitLab exposes an authoritative read-only permission result for the authenticated user. `doctor` never sends a nominal no-op update or dry-run request that could change a real MR.
