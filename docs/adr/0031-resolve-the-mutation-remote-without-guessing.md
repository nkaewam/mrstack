# Resolve the mutation remote without guessing

Every v1 command must run inside a Git clone or worktree of the selected GitLab project. The repository supplies the canonical project context, Git object store, ancestry, worktree inventory, and remote configuration. API-only invocation through a separately supplied project is deferred beyond v1.

`mrstack` will never choose among multiple plausible Git remotes. It first considers the current branch's configured upstream remote, and accepts it only when the remote maps unambiguously to the same authenticated GitLab host and project as every stack MR. Otherwise the caller must supply `--remote <name>`. Observation validates the fetch URL and may fetch only objects or private CLI refs; mutation additionally validates the effective push URL.

Before mutation, both the selected remote's fetch URL and effective push URL must canonicalize to that same GitLab host and project. Triangular remotes, aliases that cannot be resolved unambiguously, and conflicting project mappings return `invalid/ambiguous_remote` before a managed worktree is created. The resolved remote name and credential-free canonical fetch and push project identities are captured in the mutation snapshot, repeated in generated mutation actions, and revalidated before replay and publication.
