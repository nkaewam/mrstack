# Treat machine output as a public API

The CLI will provide human-readable output by default and a versioned `--json --no-input` contract for coding agents. Machine mode emits exactly one schema-valid document on stdout, sends diagnostics to stderr, and never prompts, so agents do not need to scrape prose or infer control flow.

Stack disposition is data, not process success. Exit code `0` means the command successfully produced an authoritative domain outcome, including `action_required`, `waiting`, `human_required`, `ready`, `complete`, or `invalid`. Stable nonzero classes are reserved for invalid invocation or input (`2`), inability to produce an authoritative result because of environment, authentication, transport, or prerequisite failure (`3`), and an unexpected internal invariant failure (`4`).

Checks return compact pipeline and failed-job identifiers rather than job logs. A separate CI-evidence command requires that exact pipeline ID and optionally exact job IDs, so a concurrent retry cannot redirect the agent to different evidence. Log output is size-bounded and reports truncation and returned-byte metadata; raw logs are never stored in the journal.

Remediation packets expose suggested next steps as structured actions rather than shell strings. Each action has a stable `kind`, an `argv` array whose elements require no shell parsing, whether it mutates state, whether explicit confirmation is required, and the snapshot or session identity that must still hold. Human-readable renderings may display an escaped command, but agents consume only the structured form.
