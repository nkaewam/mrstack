# Preserve layer commit history

Restacking will replay each layer's individual commits in their original order while preserving messages and authorship, changing only the commit identities required by the new ancestry. The CLI will not silently collapse a multi-commit MR into a single patch or commit, so conflict resolution follows normal per-commit rebase semantics and review history remains recognizable.

If replay makes a commit empty because its change is already present in the new ancestry, `mrstack` will stop with `action_required/empty_commit` rather than silently dropping it. The caller must explicitly resume with either `restack continue --drop-current` or `restack continue --keep-empty`. The selected resolution and the original commit identity are recorded in the journal.

The engine must explicitly attempt every commit enumerated for the layer. Git's patch-equivalence or already-applied-commit optimization may not silently omit one; an already-present change is routed through the same explicit empty-commit decision.

Commit signatures cannot survive a history rewrite. If the affected suffix contains any signed commit, preflight will stop with `human_required/signed_commits`. The caller may start the session with `--allow-signature-loss`; the override and affected commit identities are journaled. V1 will neither strip signatures silently nor re-sign another author's rewritten commit as the current user.
