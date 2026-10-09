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

- Task graphs are acyclic. A cycle is rejected before the graph changes.
- Traversal is bounded. The proposed depth limit is 12; it is a design parameter, not a standard.
- State changes are staged and roll back when a trajectory breaks a guardrail.

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

## Milestones

See the repository milestones 0.1 to 0.5.
