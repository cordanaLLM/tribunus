# Agent onboarding

Read this first when you work in `cordanaLLM/tribunus`.

## What Tribunus is

An agent runtime and task-graph engine for the cordanaLLM ecosystem. It reads its organisation (agents, routines, budgets) from files in Git, not from a database wizard.

Planned stack:

- Control plane: Go 1.27.2+ (`cmd/`, `internal/`), built on pinned golusoris modules ([ADR-0004](adr/0004-golusoris-pinned-base.md)).
- Vector kernel: Rust (`crates/tribunus-graph`), SIMD skill matching.
- Operator UI: Svelte 5 (`ui/`).

Today only the model catalog and in-memory task graph exist (`catalog/`, `cmd/tribunusctl`, `internal/sources/`, `internal/graph`). The rest lands in milestones 0.2 to 0.6; see the [roadmap](roadmap.md). The catalog has no silent fallback. A value a source does not report is nil with an `Absent` reason, and a malformed upstream entry or invalid record is rejected and counted in the source line while the rest is kept. A source run is `degraded` when it still produced records but lost upstream data; the detail text keeps the rejected, malformed, or failed sub-feed count. Empty upstream model responses are failures because they usually mean a schema or authorization break, except `ollama-local` `/api/tags`, where an empty local daemon is real state and remains `skip`. `tribunusctl sync` exits non-zero when every selected source fails. The rest lands per milestone 0.2 to 0.5.

The task graph status vocabulary is `proposed`, `ready`, `claimed`, `implementing`, `review`, `fix-round-N`, `blocked`, `queued`, `landing`, `landed` and `dropped`.

When Renovate moves a pin in `internal/sources/upstream-schemas.json`, its pull request arrives red: the committed fragments no longer match the new revision. Check out the branch, run `make schemas-refresh`, and commit the regenerated fragments. If the parser tests then fail, the upstream shape changed and the parser needs the fix. Until a Renovate intake unit does this (#38), the session that picks up the pull request runs the step.

## Rules for source code

Tribunus code follows the HISS code standards that Praetor enforces:

| Rule | Limit |
| --- | --- |
| HISS-01 | no recursion, call graph is a DAG |
| HISS-02 | scalar bound on every loop, context timeout on every I/O |
| HISS-04 | cyclomatic <= 10, cognitive <= 15, function <= 60 lines, statements <= 50 |
| HISS-07 | no unchecked errors, no `panic` in library code |
| HISS-10 | zero warnings from compiler, linter and formatter |

## Runtime invariants

The runtime has its own namespace, RT. These are never HISS rules.

| Id | Invariant |
| --- | --- |
| RT-01 | deterministic edges: temperature 0 plus schema-validated output |
| RT-02 | side-effect idempotency keys: `(task_id, action_type)` |
| RT-03 | control-plane isolation |
| RT-04 | event-sourced state |

Each RT rule gets fixtures replayed in both directions, like [HISS-20](https://github.com/cordanaLLM/praetor/blob/main/docs/standards/hiss-spec.md#hiss-20-replayable-enforcement-evidence).

## Models

Call models only through router aliases such as `cordana/auto`, `cordana/light`, `cordana/coding`, `cordana/reasoning`. Never hard-code a concrete model. Accelerators are one dynamic pool: placement comes from measured free memory, never from a per-device map.

## Files

- `AGENTS.md`: canonical agent briefing, rendered by Praetor; the vendor files are compiled from it.
- `docs/config.schema.json`: published Tribunus config schema; `internal/config.Load` validates files against the embedded byte-identical copy.
- `REUSE.toml`, `LICENSES/`: licensing (EUPL-1.2).
- `docs/`: these documents.

## Workflow

Run `make verify-all` before every push; the Praetor hooks run the audit before each commit and push. Do not add a relational database or a setup wizard; state flows through the event log.
