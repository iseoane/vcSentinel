# Agent Execution Control: Recovered Architecture Basis

This document records the architecture basis used to choose `sentinel runs`.
The original research report is not present in the checkout; this recovered
summary preserves its accepted conclusions and is not a substitute for fresh
provider documentation when a new adapter is designed.

## Decision

Use a provider-neutral controller with one durable supervisor per run. Store
immutable identities and an append-only normalized event stream locally. Expose
noninteractive control through `Start`, `Inspect`, and `Apply`; add a daemon and
human TUI only after the per-run contract is proven.

## Options Considered

| Option | Strength | Cost | Decision |
| --- | --- | --- | --- |
| Direct provider invocation | Smallest initial change. | Failures, retries, and process ownership remain opaque. | Keep behind an adapter port. |
| Per-run durable supervisor | Preserves lifecycle and failure evidence without a global service. | Requires explicit ownership and recovery rules. | Adopt first. |
| Repository daemon | Strong live control and shared observation. | IPC, startup, upgrade, and orphan lifecycle complexity. | Defer to D units. |
| Remote execution protocol | Enables other hosts and transports. | Capability, trust, evidence, and network failure surface. | Defer to A/D units. |

## Non-Negotiable Boundaries

- A physical adapter attempt is never silently folded into its parent job.
- A retry or fallback creates a new invocation linked to the same root run.
- Client disconnection detaches observation; it does not cancel execution.
- Cancellation requires both a cooperative signal and process-tree ownership.
- Operational logs describe what happened; they cannot establish a semantic
  review verdict.
- Adapter output is untrusted until local admission binds it to the execution
  snapshot, prompt, policy, capability set, and evidence.
- Capability resolution is deny-by-default and occurs before execution.

## Initial Control Surface

| Operation | Responsibility |
| --- | --- |
| `Start` | Admit a request, persist identities, launch the supervisor, and return the run identity. |
| `Inspect` | Replay durable state and expose status, events, and safe operational logs. |
| `Apply` | Submit an allowed control action such as abort or a response linked to the current invocation. |

The first implementation may expose this surface as an internal Go port. A CLI
and `logs --follow` are useful adapters; a daemon is not a prerequisite for R3.

## Consequences

The store and controller become the single operational source of truth. Existing
review and gate commands can migrate behind that seam without changing their
semantic authority. The design also makes unavailable agent calls durable input
instead of an invisible reason why a run stopped.
