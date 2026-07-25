# Use an agent-owned check loop

The core monitoring operation will be a deterministic, one-shot check rather than a resident daemon: it refreshes GitLab state, updates the local journal, and emits one structured result or remediation packet. The CLI never launches or embeds an agent; an external coding agent owns retry timing, conflict resolution, validation, and repeated checks, making the CLI easy to invoke, cancel, test, and embed. A human-oriented watch command may later wrap the same operation.
