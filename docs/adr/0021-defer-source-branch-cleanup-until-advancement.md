---
status: superseded by ADR-0026
---

# Defer source-branch cleanup until advancement

Before `merge-next`, the CLI will create private local safety refs for every captured layer boundary and request that GitLab retain the front source branch. If the merge or project policy requires deletion, the safety refs still preserve the exact objects needed for recovery. The merged source branch is deleted only after the active suffix has been successfully published and retargeted, and only when the original MR or project cleanup preference requested it.
