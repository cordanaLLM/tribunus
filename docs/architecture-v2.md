# Architecture

Status: design draft. Only the catalog exists in code today.

## Overview

```text
[ Svelte 5 UI ] <--(SSE / REST / JSON-RPC)--> [ Go control plane ]
                                                  - task graph and state
                                                  - event bus
                                                  - budgets, telemetry
                                                        |
                                                  (FFI / IPC)
                                                        v
                                                [ Rust vector kernel ]
                                                  - skill-vector matching
```

## Components

### Go control plane

Owns task graphs, the event bus and budgets.

`internal/graph` is the in-memory task graph store. It stages node, edge and
status edits, validates the staged result with Kahn acyclicity, configured depth
and vocabulary guardrails, then swaps the state only after every guard passes.
Replay wiring belongs to the append-only event log (#32); the package provides
the pure `Apply(event)` reducer that replay will call once both are on the same
branch. Removing a node with any incident dependency edge is refused; callers
remove edges first so history stays explicit.

The task-graph node vocabulary follows Praetor #883 as amended on 2026-10-09
and the claim-code source `feat/issue-claims` at `42f2a486b`
(`internal/forge/claim_marker.go`). Kinds are `epic`, `unit`, `decision`,
`research` and `gate`; statuses are `proposed`, `ready`, `claimed`,
`implementing`, `review`, `fix-round-N`, `blocked`, `queued`, `landing`,
`landed` and `dropped`.

- Task graphs are acyclic. A cycle is rejected before the graph changes.
- Traversal is bounded. The proposed depth limit is 12; it is a design parameter, not a standard.
- State changes are staged and roll back when a trajectory breaks a guardrail.

Control-plane process configuration is loaded by `internal/config.Load` from a YAML or JSON file validated against the published schema at `docs/config.schema.json`; the embedded copy at `internal/config/config.schema.json` is the package authority and a test keeps the two byte-identical. The loader uses `golusoris/core/config` with file watching off and environment overrides disabled, because schema validation runs before weak typed decoding and string-valued environment overrides would otherwise bypass or break numeric validation. A bad key, type or bound fails load with the JSON pointer named before unmarshal.

#### Upstream schemas

Catalog parsers are checked against small pinned upstream schema fragments under `internal/sources/*/testdata/upstream/`, with the source revisions and full-document digests recorded in `internal/sources/upstream-schemas.json`. Run `make schemas-refresh` to regenerate those fragments from the pinned upstream documents, and `make schemas-check` to verify the committed bytes still match.

### Rust vector kernel

Matches tasks to agent skill embeddings with SIMD distance (cosine, Euclidean). Integer-indexed arena trees keep hierarchical task decomposition cache-friendly. A topological check (Kahn) verifies acyclicity in O(V + E).

### Svelte 5 UI

Operator view built with runes (`$state`, `$derived`, `$effect`) and snippets.

## Runtime invariants

| Id | Invariant |
| --- | --- |
| RT-01 | deterministic edges: temperature 0, schema-validated output |
| RT-02 | idempotency key `(task_id, action_type)` on every side effect |
| RT-03 | control-plane isolation |
| RT-04 | event-sourced state |

RT rules are separate from the HISS code standards. Source code in this repository also meets HISS-01, -02, -04 (function limit 60 lines), -07 and -10.

### RT-03: control-plane isolation

The control plane is the Go process that owns the task graph, the event log, the broker, budgets and the operator's credentials. Task execution is everything that runs a unit of work: the sandboxed runner ([#25](https://github.com/cordanaLLM/tribunus/issues/25)), the agents it starts and the commands they run. RT-03 holds when task execution can affect the control plane only by sending messages it validates. A task never gets the control plane's process, credentials, writable paths or network reach:

| Boundary | The control plane never shares | A task gets instead |
| --- | --- | --- |
| Process | its process, process group or session; signals and `ptrace` access to it; its memory and open file descriptors | a separate process tree, started by the runner, that reports through the broker |
| Credentials | its tokens and keys (gateway admin, forge, receipt signing), its environment and its credential files | an environment built from an allowlist, plus a short-lived credential minted for that task and scoped to the router aliases and forge actions the task declares |
| Writable paths | the event log, the graph store, config, its working tree and `.git`, credential files and every path outside the task workspace | one writable workspace per task and read-only views of declared inputs; paths resolve inside them without symlink escape |
| Network reach | its own listeners (API, MCP, metrics) and loopback services it did not grant | egress to the destinations the task declares, such as the gateway behind a router alias |

A task's results reach the control plane as broker messages, and the control plane validates them before it appends to the event log. The task never writes control-plane state directly. A violation is refused, and the refusal is recorded as an event naming the boundary, so the run fails loudly instead of degrading.

#### RT-03 fixtures

RT-03 is proven by fixtures replayed in both directions, as HISS-20 asks of every enforcement claim. Each fixture is one case file:

| Field | Meaning |
| --- | --- |
| `id` | a stable name, such as `rt03-credentials-env-leak` |
| `boundary` | `process`, `credentials`, `paths` or `network` |
| `direction` | `violation` (the attempt must be refused) or `compliant` (the action must succeed) |
| `task` | the task spec under test: environment allowlist, mounts, declared egress, minted credential scope |
| `attempt` | the one action the task performs: read a variable or file, write a path, connect to a destination, or signal a process |
| `expect` | `refused` or `allowed`, and for `refused` the event the control plane must record (type `rt03.violation`, the boundary, the task id) |

Every boundary has at least one `violation` and one `compliant` case. The checker fails when a violation is allowed and when a compliant action is refused, so neither an open sandbox nor one that blocks everything passes. A fixture is trusted only after it is shown failing against a runner without the rule (rule 13).

The definition and this format land first. The runner and the checker that replays the fixtures follow with the sandboxed runner (#25), on top of config (#31) and the event log (#32).

## Models and accelerators

Every model call goes through a router alias. No concrete model name appears in code or config. Accelerators form one dynamic pool; an arbiter places models from measured free memory. Work is admitted only when the resolved model reports ready.

## Decisions

### ADR-001: graph and event log as source of truth

- Context: SQL-backed agent rosters add setup friction.
- Decision: keep an in-memory graph hydrated from Git files and an append-only event log.
- Consequence: fast start, no database setup, history in Git. Paperclip is not required.

### ADR-002: Go and Rust split

- Context: orchestration needs networking and API tooling; vector search needs memory control.
- Decision: Go control plane, Rust kernel.
- Consequence: fast service work, low-latency matching.

### ADR-003: Svelte 5 runes for the UI

- Decision: build the UI with runes and export reusable component bundles.
- Consequence: small bundles, usable on web and desktop.

### ADR-004: golusoris as the pinned base of the control plane

- Context: the control plane needs config, logging, telemetry, an HTTP, SSE and MCP server, health, idempotency and signed records. golusoris ships all of them as opt-in modules, pre-1.0.
- Decision: build on golusoris and golusoris/core, pinned to exact versions and moved by Renovate. Tribunus writes only the task-graph store, the Git-backed event log and the in-process broker, which golusoris does not ship without Postgres or NATS.
- Consequence: one implementation per capability across the ecosystem ([HISS-19](https://github.com/cordanaLLM/praetor/blob/main/docs/standards/hiss-spec.md#hiss-19-reuse-before-writing)). Breaking golusoris releases arrive as planned migration units (#30).

### ADR-005: Praetor governs, Tribunus runs

- Context: runtime work (job supervision, event watches, resume, sandboxing, quota collection) had been filed in Praetor next to governance work, and three copies of the model catalog existed.
- Decision:
  - Praetor owns gates, the planning graph and its vocabulary, the efficiency ledger, the invocation and agent-loop budget layers, the dispatch hook, issue claims and the plan notebook.
  - Tribunus owns the runtime, the orchestration and platform budget layers, and the model catalog.
  - The runtime issues moved here with their history (#21 to #25).
- Consequence: Praetor reads the Tribunus catalog snapshot (#29), and the interfaces between the two are versioned contracts (cordanaLLM/praetor#1045).

### ADR-006: cutover order

- Context: operator scripts run the watchers, the landing queue and the wave runner today, and a session restart can kill them without notice.
- Decision: replace them in three cutovers, smallest and most shared first:
  1. Watchers and supervision (#36).
  2. The landing queue (#38).
  3. The wave runner (#39).
- Consequence: each cutover runs beside the script it replaces until the events match. The first cutover already removes the restart losses.

## Milestones

Milestones 0.1 to 0.6 and the interfaces with Praetor are in the [roadmap](roadmap.md).
