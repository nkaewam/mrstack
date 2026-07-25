# Discover and track stacks without setup

Tracking will require no initialization or authoritative name: given the current branch, a branch name, or an MR IID, the CLI derives the live chain from GitLab and automatically assigns a local journal identity on first observation. That identity follows the chain through overlapping MR membership as its active suffix changes; an optional alias affects display only and never determines membership.

After no open members remain, `complete` is returned only when an MR selector or tracked-stack identity selects a previously observed chain and current GitLab state satisfies ADR-0034's strategy-aware integration and merge-order proof for every member. The local identity selects the historical MR set to verify but does not establish its outcome. Merely finding no open MR returns `invalid/no_stack_selected`, never `complete`.
