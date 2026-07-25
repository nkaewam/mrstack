---
status: superseded by ADR-0026
---

# Gate review policy at the front

Required approvals and other human merge-policy conditions will gate only the front MR, even though CI health gates the whole active stack. Missing downstream approvals remain visible as per-MR findings but do not override `ready` until that MR becomes the front, supporting progressive review without blocking an earlier layer on later review state.
