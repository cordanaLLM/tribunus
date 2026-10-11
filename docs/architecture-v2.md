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
The event log rebuilds the graph: `internal/graphlog` reduces one
`graph.stage_committed` record per committed stage, whose payload holds the
stage's operations in order, through the pure `ApplyStage(events)` of this
package. A stage the graph refuses, which two racing writers can produce, is
not applied and is counted in the replayed state; a record that is no stage
stops the replay. Replay checks the hard depth cap of 64; the configured depth
binds new writes: `epitropctl graph apply` checks a stage against the graph
the log holds, under the configured depth, and appends it as one record, and
`epitropctl graph show` prints the graph rebuilt from the log. The check runs
outside the log's lock, as the run record's does (#108). Removing a node with any incident dependency edge is refused; callers
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

#### Event log

The event log is the source of truth ADR-001 names, and RT-04 rests on it: the task graph and every task state are rebuilt from it alone. It is a directory of plain files in a Git working tree, written only by the control plane (RT-03).

Declared background jobs use the same log as their supervisor ledger. Each job is loaded from config as an argv-only command with exactly one log path, schedule, restart policy, stop policy, optional `max_runtime_seconds` and `sandbox.mode`; `always` means a daemon. On Linux, `epitropctl jobs start|status|stop|supervise` starts jobs through the hidden shim path, which holds `<event_log.dir>/jobs/<name>.lock` on a close-on-exec file descriptor, records the shim and child pids, appends stdout and stderr to the declared log, starts the child in its own process group, and keeps the shim as the waiting parent. A job that is still running after `max_runtime_seconds` is stopped by its shim the way `jobs stop` stops it, with the job's stop signal, its grace period and a kill of the whole process group, and the exit is recorded with reason `deadline`. The supervisor that started a shim collects its exit status, so a long-running `jobs supervise` leaves no zombie per job start (`TestSupervisorCollectsItsShims`); the shim itself still outlives a supervisor that exits first. The default `sandbox.mode: enforce` wraps the job in the RT-03 runner; `sandbox.mode: off` runs the bare command only when the job carries a non-empty reason, and the `job.started` payload and status row name that choice. Before stop or escalation sends any process-group signal from a persisted record, the supervisor refuses the record unless the child pid is greater than 1, is not the supervisor pid or process group, is the process-group leader, has the recorded shim as parent, and the live shim can be proven to be the hidden `__job-shim` for that job and record while the job lock is held. The child receives a kernel parent-death signal if the shim dies, so a killed shim releases the lock only after the child is gone and a restarted supervisor cannot create a second live copy. A new supervisor replays job events, adopts a still-locked record without starting a second copy, records a lost exit when the lock is free, and applies bounded restart decisions from replayed state. Other platforms report `not-supported` for this orphan-prevention guarantee until file locks, process groups, signal delivery and parent-death signalling are implemented there.

- **One file per record.** Record `n` is `events/<n, 20 digits>.json`, so file order is log order and a Git diff shows each append as one new file. A record is written to a temporary file in the same directory, fsynced, then renamed into place, so a crash leaves either the whole record or none of it. A record is at most 64 KiB, and one replay reads at most the configured number of records.
- **Canonical bytes.** A record is JSON canonicalised by RFC 8785 (`core/codec/jcs`) with the fields `seq` (1, 2, 3, …), `prev` (the SHA-256 of the previous record's bytes; zeros for the first), `time`, `type`, `task_id`, `payload` and `receipt`. Replay re-canonicalises each file and refuses one whose bytes differ.
- **Signed.** `receipt` is an Ed25519 receipt (`core/crypto/receipt`) whose output hash is the SHA-256 of the record without its `receipt` field. Replay checks it with `receipt.VerifyOutput` against the public key in config, so an edited record fails even when its JSON stays valid.
- **Single writer.** An append takes an exclusive lock on `events/` (a lock file held with `flock`) for a bounded time. It allocates the next `seq`, writes the record and replaces `HEAD.json` while holding it, and fails closed when the lock is not granted in time. Two concurrent appenders can neither reuse a `seq` nor lose a record.
- **Lock timeout.** A writer waits for the lock at most `event_log.lock_timeout_seconds` (default 5, from 1 to 300). The supervisor, the job shim and `epitropctl units` all use that value. One append costs four syncs, two for the record and two for `HEAD.json`, and writers queue: the last of *n* waiting writers waits for *n* − 1 appends. On an idle NVMe disk sixteen queued writers finish in 0.14 s, about 9 ms an append, so the default leaves room for several hundred. On a disk under heavy write load appends have been seen more than forty times slower (#83); at that rate about a dozen queued writers reach the default. A writer that times out appends nothing and reports the lock timeout, and no caller retries. Raise the value on a slow disk.
- **Signing key.** The private key path comes from the config (#31). The writer refuses a key file inside the repository tree and one readable by group or others, as ssh does. Replay needs only the public key; with the wrong one it fails at the first record and names it.
- **Head.** `events/HEAD.json` names the last `seq` and its hash, signed the same way and replaced atomically after each append. A deleted tail, which the hash chain alone cannot see, then fails replay.
- **Replay.** Records are read in order. Each is checked for contiguous `seq`, the `prev` chain, canonical form, receipt and size, then applied to a pure reducer. Any failure stops replay with the record's position (`seq` and file), never skipping it. The rebuilt state serialises through JCS, so two replays of the same log produce the same bytes, which the tests compare.

Committing `events/` to Git, segment rotation and compaction follow separately (#58).

The run record keeps one entry per work unit in the same signed log (`internal/units`, `epitropctl units`), so that after a crash the open units are read back, not reconstructed from transcripts. `units set <id> --status=…` appends `unit.recorded` at each status change. The run record has no status rules of its own: the names and the allowed moves are the task graph's (`internal/graph`: `proposed`, `ready`, `claimed`, `implementing`, `review`, `fix-round-N`, `blocked`, `queued`, `landing`, `landed`, `dropped`). A record that keeps the status only updates fields; worktree, branch, pull request and lane keep their value until given again; a landed or dropped unit moves again only with `--reopen`. `--evidence="<path> sha256:<hex> lines:<n>"` records where the proof of a unit's result lies: the record keeps that pointer, never the file, and keeps it until a new one replaces it. `units resume` lists the open units with a relaunch verdict: `safe` only for a ready, claimed or implementing unit without a pull request, because anything later may already be under review or landed. A note sent with `units note` is not pushed into a running agent: the next `units set` prints the unread notes and `units inbox` marks them read. A unit may carry Praetor's run identity under Praetor's own field names (`lane`, `target`, `resolved_model`, `branch`, `identity`, `identity_key`); the identity object is stored as given and the key is never recomputed here, so both records join without a mapping. Reducers share the log by domain prefix (`task.`, `job.`, `unit.`): each skips the other known domains and fails on an unknown one. The unit reducer also never fails on a rule that two writers can break by racing, such as moving a unit another writer just landed, because one such record in an append-only log would stop every later replay; it leaves the record unapplied and counts it, and `units resume` reports the count.

#### Agent slot

A slot is one run of a headless agent harness on one work unit, started as a supervised sandboxed job (RT-03). `epitropctl slot run` runs one unit: it makes the run's workspace, starts the job, waits for its end, takes what the run left and records the unit (`internal/slot`).

```bash
epitropctl slot run --config=slot.yaml --base=main --brief=brief.md \
  --workspace=/var/tmp/slots/unit-5 --out=/var/tmp/slots/unit-5.out 'epitrop#5'
```

The configuration is the slot's template: a configuration with exactly one job, sandboxed (`sandbox.mode: enforce`) and with a deadline (`max_runtime_seconds` from 1 to 86400). The job's command is the harness. The runner sets what only a run can know, the job's `log_path`, `schedule`, `restart` and `sandbox.workspace`, and refuses a template that sets one of them. It writes the result as `config.json` into the output directory and loads that file like any configuration, so the loader's checks, the protected paths among them, hold for the run's own workspace (`TestRunConfigKeepsTheEventLogOutOfTheWorkspace`). `epitropctl jobs status|stop --config=<out>/config.json` reaches the run's job.

A run goes through these steps (`slot.Run`):

1. The workspace is prepared and the output directory made. Neither may exist, and neither lies inside the other.
2. The host is checked for the sandbox's tools, and the slot's job must not run already: one slot runs one unit at a time.
3. The unit is recorded as `implementing`, with the workspace and the branch. The record comes before the start, so a unit that may not move to `implementing` starts no job. Up to here a failure leaves nothing: no workspace, no output directory, no record.
4. The job is started and watched until it no longer runs. The supervisor's shim ends it at its deadline; the runner waits one minute beyond the deadline and the stop grace.
5. The result is checked, the branch fetched and checked, the result and the evidence copied out, and the branch brought into the repository.
6. The runner writes its report, `run.json`, into the output directory and records the unit with an evidence pointer to it: `review` for a run that reports `done`, `blocked` for every other end.

- **Workspace.** `slot.Prepare` makes a fresh local clone of the repository for one unit. The clone is checked out on the branch `unit/<unit id>` at the named base commit, also when the repository itself is on a branch of that name; a `:` in the id is written `=`, because a branch name takes no colon. It has no remote and shares no object file with the repository (`git clone --no-hardlinks`). Git runs there without the caller's own `GIT_` variables and without global or system configuration: git exports `GIT_DIR` to every hook, and a runner started from one would otherwise act on the hook's repository (`TestPrepareIgnoresTheCallersGitEnvironment`). The inputs lie in `.slot/in/`: `brief.md`, `resume.md` and `decisions.tsv`, one answered decision per line as id, tab, answer. `.slot/` is excluded in the clone, so no commit of the run carries it. A request with a relative path, an existing workspace directory, a base that is no commit, an empty brief or an input above its bound (64 KiB for the brief and for the resume note, 256 decisions) is refused, and a workspace that could not be finished is removed again.
- **Result.** The run reports in `.slot/out/result.json`, validated against `internal/slot/result.schema.json`: the unit, an outcome of `done`, `blocked` or `failed`, a summary, the head commit of the unit's branch, evidence pointers and open questions. `ReadResult` refuses a document that breaks the schema, names another unit or claims `done` without a head. It refuses evidence that is listed twice, is no regular file under `.slot/out/evidence/`, leaves that directory through a symlink, or differs from its recorded SHA-256 or line count. The result is at most 64 KiB, one evidence file at most 8 MiB.
- **Branch.** The runner runs no git command inside the workspace after the run: its git directory is then the run's own writing, settings and hooks included. It fetches the unit's branch from the workspace, which serves it through `git-upload-pack`, into a ref of its own (`refs/epitrop/slot/…`), with the arriving objects checked (`fetch.fsckObjects`). The head has to be a commit, the one the result names, and has to descend from the base. Only then is the branch brought into the repository's branches, without force: a branch of that name that holds other commits, or that is checked out, stays as it is, and the run is not taken. A run that made no commit publishes nothing. `TestRunRunsNothingTheWorkspaceNames` plants settings that name a command and hooks in a workspace and shows that none runs.
- **Report.** `run.json` holds the outcome, the reason of a refusal, how the job ended (exit code, `deadline`), the base, the head, and the result and evidence copies with their digests. Two outcomes are the runner's own: `refused`, when the supervisor did not start the job, and `not-taken`, when the run ended and the runner did not take its result: there was none, it was not valid, or the repository did not take the branch. The command prints the unit, the branch and the report path, and fails for these two outcomes.

The result rule is stated by fixtures replayed in both directions (`internal/slot/testdata/result/`); `TestResultCheckerRefusesAFixtureThatClaimsTheWrongOutcome` shows that the replay fails on a fixture whose expectation is wrong. The whole run is proven with a stand-in harness, a shell script under the real sandbox (`cmd/epitropctl/slot_linux_test.go`): a run that commits, writes evidence and reports `done` is recorded as `review` with its branch published, and its attempts to write outside the workspace and to forge the runner's report fail; an unknown outcome, a missing result and a run ended by its deadline are each recorded as `blocked` and change nothing in the repository.

What this does not cover:

- No real harness has run in a slot. The proof uses a stand-in; which variables and files a real harness needs inside the sandbox is not measured yet.
- A job with `sandbox.network: egress` reaches the whole internet. A slot that reaches a model gateway and nothing else is not built, and no real unit should run in a slot before it is.
- A valid result is the run's own claim. The runner checks its form, the evidence files and the head of the branch. It does not check that the evidence shows what the summary says, and it reviews no commit.
- Nothing bounds the size of what a run commits and the runner fetches.
- When the runner itself is stopped while the job runs, or cannot tell what became of a start, it records nothing more: the unit stays `implementing`, the job runs to its deadline, and the workspace is kept. Nothing takes such a run up again yet.
- The workspace and the output directory are kept after every run that started; removing them is the operator's.

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

### RT-01: deterministic edges

`internal/edges.Check` is the check of RT-01 for one model call: the request carries a temperature of exactly 0, and the output validates against the JSON Schema its caller declared. A call without a temperature is refused, because it runs at the provider's default. A call without a schema, or with an empty one, is refused, because an output "validated" against a schema that accepts everything is not validated.

The rule is stated by fixtures replayed in both directions (`internal/edges/testdata/rt01/`): a temperature above 0 or missing, a schema missing or empty, and an output that is no JSON, lacks a field, breaks a bound or carries an extra field must be refused; a sound call must be allowed.

Coverage: the check is bound to no live edge. The runtime makes no model call of its own yet, so nothing in a running system passes through `Check` today, and RT-01 is not enforced anywhere. The fixtures prove the check, not a system. The first model edge has to route every call through it; that binding is tracked as its own issue.

### RT-02: the side-effect ledger

`internal/effects` records every outbound side effect in the event log under its key, the task id and the action type. `Ledger.Do(key, effect)` runs the effect at most once:

- A key that is done is not run again. The delivery is recorded as `effect.skipped`.
- A new key is claimed (`effect.claimed`), the effect runs, and its end is recorded (`effect.done` or `effect.failed`). Two writers that deliver the same new key both append a claim; the first claim in log order holds, and each writer replays after its own append to see whether it owns the claim. The other one does not run the effect.
- A claim without an end, which a crash between the claim and the end leaves behind, and a failed attempt are open. Whether the effect happened is not known then, so nothing runs it again by itself: `Do` returns an error naming the open claim, and `epitropctl effects list` shows it. It stays open until someone records what became of it with `epitropctl effects resolve`: `done` when the effect did happen, `retry` when it did not and may be attempted once more. An open claim does not age out.

The rule is proven by fixtures replayed in both directions (`internal/effects/testdata/rt02/`), in the format of the RT-03 fixtures: a second delivery, a delivery on an open claim and a delivery after a failed attempt must not run the effect; a first delivery and a delivery after a recorded `retry` must.

What it does not cover yet: a sandboxed job cannot write the event log (RT-03), so the release watch still guards its own side effect, one issue per release, with a marker on the forge and its own seen set. Its results pass through the ledger once the broker carries them to the control plane.

### RT-03: control-plane isolation

The control plane is the Go process that owns the task graph, the event log, the broker, budgets and the operator's credentials. Task execution is everything that runs a unit of work: the sandboxed runner ([#25](https://github.com/tekmora/epitrop/issues/25)), the agents it starts and the commands they run. RT-03 holds when task execution can affect the control plane only by sending messages it validates. A task never gets the control plane's process, credentials, writable paths or network reach:

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

The sandboxed runner is implemented for Linux jobs ([ADR-0005](adr/0005-sandboxed-job-runner.md)). In enforce mode the shim resolves the configured command outside the sandbox, then starts `systemd-run --user --scope --collect` with `MemoryMax`, `CPUWeight` and `TasksMax`, and `MemoryHigh` when the job sets a soft limit, under a scope unit it names itself, optionally starts `pasta` for `network: egress`, and runs `bwrap --die-with-parent --unshare-all` with only `/usr`, certificate stores, `/proc`, `/dev`, tmpfs scratch paths, the writable workspace and declared read-only inputs mounted. The job sees exactly `PATH`, `HOME` set to the workspace, `TMPDIR=/tmp`, `PWD` and its configured `env_allow` names; the values travel in the process environment, never on the argv. `network: none` has no network namespace sharing; `network: egress` gives the namespace its own TEST-NET-1 address, disables host loopback mapping and automatic port forwarding, and binds the systemd-resolved upstream resolver file. bwrap and pasta start with SIGTERM ignored so that a graceful stop reaches the job, which gets the default TERM action back just before it starts. Workspace and inputs are checked against the event log and signing key at config load and again at job start. Before the shim records `job.started` it confirms that the job's cgroup is its own scope unit; a job found outside it is killed and the start refused, with the reason in the job's log and in a `job.refused` event.

The current runner enforces RT-03 process isolation, writable-path isolation, environment credential filtering and network modes `none` and `egress` without host loopback. It does not yet implement per-destination egress allowlists, minted short-lived credentials, the broker, or `rt03.violation` event emission. The fixtures in `internal/supervisor/testdata/rt03/`, replayed by `TestRT03Fixtures`, therefore assert only that violations are refused and compliant attempts are allowed; broker event recording lands with the later broker unit.

The release watch is the first job built for this runner (`epitropctl watch run`, `internal/releasewatch`). It polls the GitHub releases (tags when a repository has none) and Hugging Face organisations named in the `release_watch` config block, and files one issue per new release in the consuming repository of each route. Because a sandboxed job may not write the event log, its seen-set is a file in its own workspace, written atomically after each delivery; before filing, it searches the target repository for the marker `<!-- release-watch: <id> -->` (the newest 500 issues carrying the route's first label), so a lost seen-set does not re-file a release whose issue that search still reaches. A failed source or delivery is logged with its cause and fails the pass; an empty but valid answer does not. `GITHUB_TOKEN` is sent only to the configured GitHub API scheme and host and is dropped on any redirect away from it. Upstream release notes go into a code block, so their mentions and references do not notify or cross-reference upstream projects. `watch.*` events in the signed log wait for the broker.

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
- Decision: build on golusoris and golusoris/core, pinned to exact versions and moved by Renovate. Epitrop writes only the task-graph store, the Git-backed event log and the in-process broker, which golusoris does not ship without Postgres or NATS.
- Consequence: one implementation per capability across the ecosystem ([HISS-19](https://github.com/cordanaLLM/praetor/blob/main/docs/standards/hiss-spec.md#hiss-19-reuse-before-writing)). Breaking golusoris releases arrive as planned migration units (#30).

### ADR-005: Praetor governs, Epitrop runs

- Context: runtime work (job supervision, event watches, resume, sandboxing, quota collection) had been filed in Praetor next to governance work, and three copies of the model catalog existed.
- Decision:
  - Praetor owns gates, the planning graph and its vocabulary, the efficiency ledger, the invocation and agent-loop budget layers, the dispatch hook, issue claims and the plan notebook.
  - Epitrop owns the runtime, the orchestration and platform budget layers, and the model catalog.
  - The runtime issues moved here with their history (#21 to #25).
- Consequence: Praetor reads the Epitrop catalog snapshot (#29), and the interfaces between the two are versioned contracts (cordanaLLM/praetor#1045).

### ADR-006: cutover order

- Context: operator scripts run the watchers, the landing queue and the wave runner today, and a session restart can kill them without notice.
- Decision: replace them in three cutovers, smallest and most shared first:
  1. Watchers and supervision (#36).
  2. The landing queue (#38).
  3. The wave runner (#39).
- Consequence: each cutover runs beside the script it replaces until the events match. The first cutover already removes the restart losses.

## Milestones

Milestones 0.1 to 0.6 and the interfaces with Praetor are in the [roadmap](roadmap.md).
