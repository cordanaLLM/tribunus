# ADR-0009: Give a slot's coding client a home of its own with its login bound read-only

- **Status**: Proposed
- **Date**: 2026-10-15
- **Authors**: Epitrop maintainers

## Context

A headless agent slot ([architecture N23](../architecture-v2.md)) runs a coding client as a sandboxed job. The client needs two things from its home folder: its settings (`config.toml` for the Codex CLI) and its login (`auth.json`). [ADR-0008](0008-sandbox-input-roots.md) lets a job read a client folder that a root names exactly, but an input is bound read-only as a whole folder, and the client also writes into its home: session logs, history, caches.

Measured with the Codex CLI 0.145.0 under the real sandbox:

- A home bound read-only as a whole stops the client at start, because it cannot write its session files.
- A writable home with `auth.json` alone bound read-only works: the client reads the login and writes everything else.

Three risks remain with a writable home inside the workspace:

1. **The run hands the login back.** The home lies in the workspace, and a run's commits are published as a branch. A commit that carries `auth.json`, by `git add -f` past the exclude, under another name, or in a commit that a later one removes again, would publish the login.
2. **The settings drift.** A copied `config.toml` that someone changed in the source would change what every later run is allowed to do, without anyone seeing it.
3. **The token refresh.** Codex 0.145.0 refreshes its access token when it expires within 5 minutes (`CHATGPT_ACCESS_TOKEN_REFRESH_WINDOW_MINUTES`, `should_refresh_proactively`). The server rotates the refresh token on every refresh, and a refresh token used twice is refused (`refresh_token_reused`). A refresh inside the slot cannot be saved to the read-only `auth.json`, and a failed save is only logged. The rotated refresh token is then lost and the old one is spent: the login outside the slot stops working.

## Decision

1. **A job may declare a home.** `sandbox.home` names a `source` folder, the variable that points the client at the home (`env`, for example `CODEX_HOME`), the files to `copy` with their SHA-256 and the files to `bind`, at most 8 of each. Before each start the shim makes a fresh `.slot/home` in the workspace, removing what an earlier start left. It copies each copy file there only when it hashes to the configured value, and binds each bind file read-only, one by one, over a placeholder. Only `sandbox.mode: enforce` takes a home.

2. **The source is checked like an input.** It resolves through symlinks and must lie in a declared root (ADR-0008). It may not lie in, contain or equal the workspace. It may not overlap one of ADR-0008's login locations below `$HOME`, even when a root names that location: a slot home holds a login of its own, never the operator's.

3. **No branch hands back the home or a bound file.** Before a branch is fetched into the repository, the runner reads every commit of `base..head`, merge commits against each parent. It refuses the run when a commit touches any path below `.slot/`, or adds a file whose content equals a bound read-only file. The run is recorded as `not-taken` and nothing is published.

4. **A login that may expire during the run is refused before the start.** A bind entry may name `jwt`, the dotted path of a JSON Web Token in the file. The shim reads the token's `exp` and refuses the start unless it lies after the run's latest end plus a 10-minute margin. The latest end is the job's `max_runtime_seconds` plus its stop grace. The margin is twice the client's 5-minute refresh window, so the client never starts a refresh it cannot save. A refused start is recorded with its reason, which names the file, the expiry, the latest end and the hand-over: the operator refreshes the login outside the slot and starts the run again. The error never carries token content. There is no fallback to `~/.codex` or to an API key.

5. **A job with a home needs a deadline.** Without `max_runtime_seconds` the latest end is the longest any job may run, and no access token is valid that long. The loader refuses a home without it.

6. **The report lists the binds.** `run.json` gets `binds`: each workspace, input, copy and bind with its source, target, mode and, for copies, the SHA-256. It never holds a file's content.

7. **Tests prove it in both directions.**

   | Test | What it shows |
   | :--- | :--- |
   | `TestRunTakesNoBranchThatHandsBackTheLogin` | a run that adds `.slot/home/auth.json` with `git add -f`, another file of the home, a file of `.slot/in`, a file moved into the home, the login under another name, the login added and removed again, or the login only in a merge commit is not taken; refs stay unchanged and the report lists the binds without login content. |
   | `TestRunTakesACleanBranchAndListsItsBinds` | a clean run is published and its report lists its binds. |
   | `TestSandboxedJobReadsItsHomeAndCannotWriteTheLogin` | against the real sandbox: the job reads its settings and its login; writing, moving and removing the bound `auth.json` fail, and the source stays unchanged. |
   | `TestPrepareHomeRefuses` | a changed copy file, a token that is expired or expires on the margin's boundary, an unknown latest end, a symlinked or missing file and a home or `.slot` the job left as a file or symlink are refused; no error holds token content. |
   | `TestJWTExpiry` | a token path through no object, a value that is no token, a token without a numeric `exp` or with one that is no time is refused; no error holds token content. |
   | `TestLoadSandboxHomeSource` | a source outside every root, in the workspace, overlapping `~/.codex` even when a root names it, or without a job deadline is refused. |

   Mutants of the checks, the binds, the margin, the latest end and the branch content check each turn one of these tests red.

## What this record decides

1. A slot's client gets a fresh home in `.slot/home` per start; settings are copied by hash, the login is bound read-only file by file.
2. No published branch may touch `.slot/` or carry a bound file's content.
3. A bound token must outlast the run's latest end by 10 minutes, or the run does not start; a job with a home needs `max_runtime_seconds`.
4. A home's source may not be one of the operator's login locations.

## References

Decisions made before this record, by the maintainer. The wording is paraphrased.

- `epitrop-n23-slot-client-home`, 2026-10-15: the runner copies only the settings file into the home and binds the login alone read-only, with the settings checked against their recorded hash before each run; the branch step refuses a head that touches `.slot/` or carries a bound file; an expired login or a failed refresh fails the run with a named reason and a hand-over, never a fallback to the operator's own client folder or an API key; one pull request with a record, a planted forced add of the login among its tests, and the binds listed in the run's report (paraphrased). Items 4 and 5 above refine the expiry rule into a refusal before the start, derived from the client's refresh behaviour.
- `epitrop-n23-retire-ide-chats`, 2026-10-15: the first real slot run uses the Codex CLI on one low-risk unit of one public repository, and measures what the client needs inside the sandbox (paraphrased).

## Consequences

- **Positive**: A slot's client runs with its own settings and login and cannot change, publish or spend either. A drifted settings file or an expiring login stops the run before it starts, with a reason the operator can act on.
- **Negative**: The content check finds only verbatim copies of a bound file: a run that encodes the login, or splits it over files, is not caught. Only files bound one by one are compared: the files inside an input folder are not. A run with network access can send the login elsewhere; this record covers the branch alone. The login must be refreshed outside the slot before a run whose end lies close to the token's expiry, which is an operator act.
