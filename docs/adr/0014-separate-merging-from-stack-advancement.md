---
status: superseded by ADR-0026
---

# Separate merging from stack advancement

`merge-next` will only submit a SHA-guarded merge for the front and record that request; it will not rewrite or retarget the remaining stack. The agent checks until GitLab confirms the merge, after which a separate explicit restack advances the active suffix onto the base, keeping asynchronous or failed merge attempts isolated from history rewriting.
