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

Control-plane process configuration is loaded by `internal/config.Load` from a YAML or JSON file validated against the published schema at `docs/config.schema.json`; the embedded copy at `internal/config/config.schema.json` is the package authority and a test keeps the two byte-identical. The loader uses `golusoris/core/config` with file watching off and environment overrides disabled, because schema validation runs before weak typed decoding and string-valued environment overrides would otherwise bypass or break numeric validation. A bad key, type or bound fails load with the JSON pointer named before unmarshal.

#### Event log

The event log is the source of truth ADR-001 names, and RT-04 rests on it: the task graph and every task state are rebuilt from it alone. It is a directory of plain files in a Git working tree, written only by the control plane (RT-03).

Declared background jobs use the same log as their supervisor ledger. Each job is loaded from config as an argv-only command with exactly one log path, schedule, restart policy, stop policy and `sandbox.mode`; `always` means a daemon. On Linux, `tribunusctl jobs start|status|stop|supervise` starts jobs through the hidden shim path, which holds `<event_log.dir>/jobs/<name>.lock` on a close-on-exec file descriptor, records the shim and child pids, appends stdout and stderr to the declared log, starts the child in its own process group, and keeps the shim as the waiting parent. The default `sandbox.mode: enforce` wraps the job in the RT-03 runner; `sandbox.mode: off` runs the bare command only when the job carries a non-empty reason, and the `job.started` payload and status row name that choice. Before stop or escalation sends any process-group signal from a persisted record, the supervisor refuses the record unless the child pid is greater than 1, is not the supervisor pid or process group, is the process-group leader, has the recorded shim as parent, and the live shim can be proven to be the hidden `__job-shim` for that job and record while the job lock is held. The child receives a kernel parent-death signal if the shim dies, so a killed shim releases the lock only after the child is gone and a restarted supervisor cannot create a second live copy. A new supervisor replays job events, adopts a still-locked record without starting a second copy, records a lost exit when the lock is free, and applies bounded restart decisions from replayed state. Other platforms report `not-supported` for this orphan-prevention guarantee until file locks, process groups, signal delivery and parent-death signalling are implemented there.

- **One file per record.** Record `n` is `events/<n, 20 digits>.json`, so file order is log order and a Git diff shows each append as one new file. A record is written to a temporary file in the same directory, fsynced, then renamed into place, so a crash leaves either the whole record or none of it. A record is at most 64 KiB, and one replay reads at most the configured number of records.
- **Canonical bytes.** A record is JSON canonicalised by RFC 8785 (`core/codec/jcs`) with the fields `seq` (1, 2, 3, …), `prev` (the SHA-256 of the previous record's bytes; zeros for the first), `time`, `type`, `task_id`, `payload` and `receipt`. Replay re-canonicalises each file and refuses one whose bytes differ.
- **Signed.** `receipt` is an Ed25519 receipt (`core/crypto/receipt`) whose output hash is the SHA-256 of the record without its `receipt` field. Replay checks it with `receipt.VerifyOutput` against the public key in config, so an edited record fails even when its JSON stays valid.
- **Single writer.** An append takes an exclusive lock on `events/` (a lock file held with `flock`) for a bounded time. It allocates the next `seq`, writes the record and replaces `HEAD.json` while holding it, and fails closed when the lock is not granted in time. Two concurrent appenders can neither reuse a `seq` nor lose a record.
- **Signing key.** The private key path comes from the config (#31). The writer refuses a key file inside the repository tree and one readable by group or others, as ssh does. Replay needs only the public key; with the wrong one it fails at the first record and names it.
- **Head.** `events/HEAD.json` names the last `seq` and its hash, signed the same way and replaced atomically after each append. A deleted tail, which the hash chain alone cannot see, then fails replay.
- **Replay.** Records are read in order. Each is checked for contiguous `seq`, the `prev` chain, canonical form, receipt and size, then applied to a pure reducer. Any failure stops replay with the record's position (`seq` and file), never skipping it. The rebuilt state serialises through JCS, so two replays of the same log produce the same bytes, which the tests compare.

Committing `events/` to Git, segment rotation and compaction follow separately (#58).

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

The sandboxed runner is implemented for Linux jobs ([ADR-0005](adr/0005-sandboxed-job-runner.md)). In enforce mode the shim resolves the configured command outside the sandbox, then starts `systemd-run --user --scope --collect` with `MemoryMax`, `CPUWeight` and `TasksMax`, optionally starts `pasta` for `network: egress`, and runs `bwrap --die-with-parent --unshare-all` with only `/usr`, certificate stores, `/proc`, `/dev`, tmpfs scratch paths, the writable workspace and declared read-only inputs mounted. The job sees exactly `PATH`, `HOME` set to the workspace, `TMPDIR=/tmp`, `PWD` and its configured `env_allow` names; the values travel in the process environment, never on the argv. `network: none` has no network namespace sharing; `network: egress` gives the namespace its own TEST-NET-1 address, disables host loopback mapping and automatic port forwarding, and binds the systemd-resolved upstream resolver file. bwrap and pasta start with SIGTERM ignored so that a graceful stop reaches the job, which gets the default TERM action back just before it starts. Workspace and inputs are checked against the event log and signing key at config load and again at job start.

The current runner enforces RT-03 process isolation, writable-path isolation, environment credential filtering and network modes `none` and `egress` without host loopback. It does not yet implement per-destination egress allowlists, minted short-lived credentials, the broker, or `rt03.violation` event emission. The fixtures in `internal/supervisor/testdata/rt03/`, replayed by `TestRT03Fixtures`, therefore assert only that violations are refused and compliant attempts are allowed; broker event recording lands with the later broker unit.

The release watch is the first job built for this runner (`tribunusctl watch run`, `internal/releasewatch`). It polls the GitHub releases (tags when a repository has none) and Hugging Face organisations named in the `release_watch` config block, and files one issue per new release in the consuming repository of each route. Because a sandboxed job may not write the event log, its seen-set is a file in its own workspace, written atomically after each delivery; before filing, it searches the target repository for the marker `<!-- release-watch: <id> -->` (the newest 500 issues carrying the route's first label), so a lost seen-set does not re-file a release whose issue that search still reaches. A failed source or delivery is logged with its cause and fails the pass; an empty but valid answer does not. `GITHUB_TOKEN` is sent only to the configured GitHub API scheme and host and is dropped on any redirect away from it. Upstream release notes go into a code block, so their mentions and references do not notify or cross-reference upstream projects. `watch.*` events in the signed log wait for the broker.

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
