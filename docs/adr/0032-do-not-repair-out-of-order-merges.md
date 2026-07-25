# Do not repair out-of-order merges

If GitLab shows that a non-front stack MR merged while any predecessor remained open, `mrstack` returns `invalid/out_of_order_merge`. It records the observed topology, merge identities, and revisions but performs no replay, retarget, or ref update.

Repair requires human restructuring because the declared source/target order was violated: automatically choosing a new layer boundary could duplicate changes, omit dependencies, or conceal what actually entered the base. A later check may discover a valid stack again only from newly unambiguous GitLab source/target relationships; the journal cannot reinterpret the invalid history.
