# Cutover 1: watchers and supervision

Cutover 1 ([#36](https://github.com/tekmora/epitrop/issues/36)) moves the fleet's background jobs from host service units and session-bound shell loops to the Epitrop supervisor. The scripts themselves stay as they are: Epitrop starts, watches and restarts them. Replacing what the landing scripts do is cutover 2 ([#38](https://github.com/tekmora/epitrop/issues/38)).

This page is the plan. [`cutover-1.example.yaml`](cutover-1.example.yaml) declares the jobs with placeholder paths, and `TestExampleConfigValidates` in `internal/config/config_test.go` loads it and `TestCutoverOneExampleIsASupervisorConfig` in `internal/supervisor/cutover_linux_test.go` checks it as the supervisor does. The operator's copy names the real scripts and lives in the private configuration, outside this repository.

## The jobs

| Job | Today | Under Epitrop | Can run beside the live script |
| :--- | :--- | :--- | :--- |
| `landing-daemon` | host service unit | `schedule: always`, `restart: on-failure` | no: it moves queue files and lands pull requests, and it has no dry mode |
| `auto-rebase` | host service unit | `schedule: always`, `restart: on-failure` | as `auto-rebase-shadow` once its dry mode writes nothing (see below) |
| `update-intake` | host service unit | `schedule: always`, `restart: on-failure` | no: it writes the landing queue, and it has no dry mode |
| `fleet-monitor` | a loop a session starts, ends with the session | `schedule: always`, `restart: always` | no: it records the issues it has reported and starts the scratch cleanup |
| `scratch-cleanup` | started by the monitor when space runs low | `schedule: "*/15 * * * *"`, `max_runtime_seconds: 600` | no: it deletes |

The queue restore loop, which waits for one pull request to merge and then relands the units queued behind it, is not declared. It takes the pull request and the queue as arguments for each wait, and a declared job's arguments are fixed ([ADR-0007](adr/0007-job-arguments-unchanged.md)). It moves with the landing queue in cutover 2.

None of the scripts beats while idle: each writes its log only when it acts. A heartbeat bound (`heartbeat.max_age_seconds`, #21) would end an idle script as hung, so the jobs declare none until each script writes a beat line on every pass.

## Why the parallel run uses dry modes

Each job acts on shared state: the landing queue, the forge, the scratch directories, the monitor's record of reported issues. A second copy of a live script beside the running one acts twice, or takes work from the running copy. The parallel run therefore runs each script's dry mode under Epitrop, a mode that prints what the script would do and changes nothing, and compares that output with what the live script did.

The auto-rebase script has a dry mode, but it is not side-effect free yet: when a rebase run has finished, the dry mode still removes that run's result files and raises the attempt count, the same as the live mode. The other scripts have no dry mode. Adding a dry mode that writes nothing, and a beat line on every pass, is work in the scripts' own repository; Epitrop needs no change for it.

## Phases

1. **Declared (this change).** The jobs exist as configuration. `epitropctl jobs status --config <file>` loads the operator's copy and lists every job as `dead`, never started. No supervisor runs it.
2. **Parallel run.** A supervisor runs the shadow jobs only, outside any session, with the live scripts unchanged under their host units. Starting that supervisor is an operator decision, because it is a long-running process on the host. Each script joins when its dry mode writes nothing.
3. **Switch-over.** One script at a time: its host unit stops, and its job moves from the shadow configuration to the live one. Each switch-over is an operator decision that cites a week of matching evidence. Undoing it means starting the host unit again and removing the job.

## Matching evidence

For each script, over seven consecutive days of the parallel run:

- **Decisions match.** Every action in the live script's log (a landing, a rebase, a requeue, a skip) has a dry-mode line for the same unit in the same pass or the next one, and the dry mode names no action the live script did not take. The comparison is per unit name, and a pass is the script's own interval.
- **Supervision holds.** The supervisor's event log shows every shadow job started on its schedule, each exit recorded, and no `job.stale` or restart that the host unit's journal does not also show for the live script.
- **Sessions do not matter.** At least one operator session ends during the week, and `jobs_status` through MCP from a new session shows every shadow job with the same state as `epitropctl jobs status`.
- **A planted event fires.** One planted queue entry is seen by the dry mode after a supervisor restart.

A mismatch restarts the week for that script once its cause is fixed.

## Retired scripts

None yet. Each switch-over adds a row here: the script, the job that replaces it, the decision, and the evidence.
