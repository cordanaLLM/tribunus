# Agent onboarding

Read this first when you work in `tekmora/epitrop`.

## What Epitrop is

An agent runtime and task-graph engine for the Tekmora ecosystem. It reads its organisation (agents, routines, budgets) from files in Git, not from a database wizard.

Planned stack:

- Control plane: Go 1.27.2+ (`cmd/`, `internal/`), built on pinned golusoris modules ([ADR-0004](adr/0004-golusoris-pinned-base.md)).
- Vector kernel: Rust (`crates/epitrop-graph`), SIMD skill matching.
- Operator UI: Svelte 5 (`ui/`).

Today these parts exist: the model catalog (`catalog/`, `internal/sources/`), the config loader (`internal/config`), the signed event log (`internal/eventlog`), the in-memory task graph (`internal/graph`), job supervision with its sandbox (`internal/supervisor`), the release watch (`internal/releasewatch`) and the run record (`internal/units`), all driven through `cmd/epitropctl`. The rest lands in milestones 0.2 to 0.6; see the [roadmap](roadmap.md). The catalog has no silent fallback. A value a source does not report is nil with an `Absent` reason, and a malformed upstream entry or invalid record is rejected and counted in the source line while the rest is kept. A source run is `degraded` when it still produced records but lost upstream data; the detail text keeps the rejected, malformed, or failed sub-feed count. Empty upstream model responses are failures because they usually mean a schema or authorization break, except `ollama-local` `/api/tags`, where an empty local daemon is real state and remains `skip`. `epitropctl sync` exits non-zero when every selected source fails. Snapshots carry `schema_version: 1`; the matching JSON Schema is `catalog/snapshot.schema.json`, and `epitropctl schema` prints the embedded copy so downstream consumers can compare their pinned contract. The rest lands per milestone 0.2 to 0.5.

The task graph status vocabulary is `proposed`, `ready`, `claimed`, `implementing`, `review`, `fix-round-N`, `blocked`, `queued`, `landing`, `landed` and `dropped`.

When Renovate moves a pin in `internal/sources/upstream-schemas.json`, its pull request arrives red: the recorded document digests and the committed fragments belong to the old revision. Check out the branch, run `make schemas-repin` (it prints each source whose digest changed and regenerates the fragments), review the manifest and fragment diff, and commit it. If the parser tests then fail, the upstream shape changed and the parser needs the fix. `make schemas-refresh` and `make schemas-check` keep refusing a document whose digest does not match the manifest. Until a Renovate intake unit does this (#38), the session that picks up the pull request runs the step.

## Rules for source code

Epitrop code follows the HISS code standards that Praetor enforces:

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
| RT-03 | control-plane isolation ([definition](architecture-v2.md#rt-03-control-plane-isolation)) |
| RT-04 | event-sourced state |

Each RT rule gets fixtures replayed in both directions, like [HISS-20](https://github.com/cordanaLLM/praetor/blob/main/docs/standards/hiss-spec.md#hiss-20-replayable-enforcement-evidence).

## Models

Call models only through the router alias `cordana/auto`, the one alias the fleet gateway serves; class aliases such as `cordana/coding` return an error there. Never hard-code a concrete model. Accelerators are one dynamic pool: placement comes from measured free memory, never from a per-device map.

## Files

- `AGENTS.md`: canonical agent briefing, rendered by Praetor; the vendor files are compiled from it.
- `docs/config.schema.json`: published Epitrop config schema; `internal/config.Load` validates files against the embedded byte-identical copy.
- `internal/eventlog`: append-only signed event log; private signing key stays outside the Git working tree and must not be readable by group or others.
- `internal/supervisor`: declared background jobs, RT-03 sandboxed runner, lock-backed liveness, Linux shim death orphan prevention and replay-based re-adoption.
- `internal/egressproxy`: the way out of a job with `sandbox.network: proxy` ([ADR-0006](adr/0006-proxy-socket-egress.md)). The shim runs a CONNECT proxy on a Unix socket outside the sandbox; it reaches the job's declared `sandbox.destinations` and nothing else. Inside the sandbox a relay carries the job's proxy requests to that socket: the hidden `epitropctl __egress-relay`, which the sandbox starts and nobody starts by hand.
- `internal/releasewatch`: upstream release watch (`epitropctl watch run|seed`), run as a sandboxed job; files one issue per new release in the consuming repository.
- `internal/units`: the run record (`epitropctl units set|resume|show|note|inbox|preempt|handover|frontier|assign|release`): one entry per work unit and status in the signed log, on the task graph's status vocabulary; a relaunch verdict after a crash; an inbox read at the next status record. `units set --evidence="<path> sha256:<hex> lines:<n>"` records where the proof of a unit's result lies; the pointer stays on the unit until a new one replaces it, and `units show` prints it. `units preempt <id> --reason=… [--by=…]` halts work at the next safe point and records why. `units handover --repo=<repository> [--answers=<file>] [--resume-out=<file>]` hands one repository to the next session: its open units, the decisions their last runs asked for that the answers file does not hold, and the unread notes, without marking anything read; `--resume-out` writes the same as a slot's resume note. `units frontier` prints the graph nodes a slot may take next: units that are `ready` and whose every dependency has landed. `units assign --repo=<repository> --lane=<lane> [--by=…] <node>` takes one of them: one record moves the node to `claimed` and opens the unit `<repository>:<node>` as `claimed`, so `slot run` can start it. `units release --reason=… [--by=…] <node>` gives a claimed node back to `ready` and drops its unit with the reason.
- `internal/graphlog`: the task graph in the signed log (`epitropctl graph show|apply`). `graph apply --config=file --file=stage.json` commits one staged change, a document `{"ops": [...]}` of `graph.node_added`, `graph.edge_added`, `graph.node_removed`, `graph.edge_removed` and `graph.status_changed` operations, as one record; a stage the graph refuses (a cycle, a chain deeper than `graph.max_depth`, a status no node may take) changes nothing. `graph show --config=file` prints the graph rebuilt from the log alone.
- `internal/effects`: the side-effect ledger of RT-02 (`epitropctl effects list|resolve`). `Ledger.Do` runs an outbound side effect at most once per key of task id and action type and records it in the signed log; a second delivery is skipped and recorded. `effects list --config=file` prints every effect with its state; a claim that never ended or failed is open and blocks its key. `effects resolve --config=file --outcome=done|retry --by=<name> [--note=<text>] <task id> <action type>` records what became of it.
- `internal/edges`: the check of RT-01 for a model call (`Check`: temperature exactly 0, output validated against the declared JSON Schema), with fixtures both ways. It is bound to no live edge yet: the runtime makes no model call of its own so far.
- `internal/slot`: one headless agent run on one work unit, as a sandboxed job (`epitropctl slot run|resume`). `slot run --config=<template> --base=<rev> --brief=<file> --workspace=<dir> --out=<dir> [--repo=<dir>] [--resume=<file>] [--decisions=<file>] <unit id>` makes a fresh clone on the branch `unit/<unit id>` with the inputs under `.slot/in/`, starts the template's one job in it, and takes what the run left: `.slot/out/result.json` is checked against `internal/slot/result.schema.json`, the branch is fetched, checked and brought into the repository without force, and the unit is recorded as `review` or `blocked` with an evidence pointer to the runner's report `<out>/run.json`. `slot run --detach` starts the same run as a transient systemd user service with the template's `sandbox.memory_max` and `sandbox.memory_high`, both required, and returns. `slot resume --out=<dir> [--repo=<dir>] [<unit id>]` takes up an interrupted run without a second start. The template is a configuration with exactly one sandboxed job with a deadline; the runner sets its `log_path`, `schedule`, `restart` and `sandbox.workspace`. Its `sandbox_input_roots` must hold a directory that contains `--workspace`, or the job does not load.
- `REUSE.toml`, `LICENSES/`: licensing (EUPL-1.2).
- `docs/`: these documents.

## Workflow

Run `make verify-all` before every push; the Praetor hooks run the audit before each commit and push. CI runs gofmt, go vet, go test -race, golangci-lint, the HISS audit and the docs-drift check on every pull request, using the tools pinned in `tools/go/go.mod` (`go tool -modfile=tools/go/go.mod <tool>`). Renovate moves those pins. Do not add a relational database or a setup wizard; state flows through the event log.
