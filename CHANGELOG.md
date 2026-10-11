# Changelog

All notable changes to this project will be documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]


## [0.1.0] - 2026-10-11

### Changed

- No change since 0.1.0-rc.3: the first release is the code of that candidate, and the changelog sections of the release candidates list what it holds. Known gap: a sandboxed job with the network mode egress reaches the whole internet, and no mode limits a job to declared destinations. A job started by slot run that needs its model gateway therefore has network access to every address. Run no real work unit in a slot until a later release closes this (#25)


## [0.1.0-rc.3] - 2026-10-11

### Fixed

- List the snapshot schema in checksums.txt, so a release signs and attests it with the archives, and take the release notes from the changelog section of the tag (#29)
- Stopping a job whose process ends while its status is read now reports the job as gone and not as an error (#21)


## [0.1.0-rc.2] - 2026-10-11

### Added

- Base the control plane on the golusoris framework at a pinned version (#30)
- Load configuration from one file checked against a published JSON Schema (#31)
- Record state in an append-only event log with signed records and replay (#32)
- Add the acyclic task graph with staged, bounded changes (#5)
- Supervise declared jobs with a per-job shim, restart and a validated stop path (#21)
- Run supervised jobs in a sandbox with cgroup limits, a restricted filesystem view and a chosen network mode (#25)
- Watch upstream releases and file one issue per release (#23)
- Keep a run record of work units and answer which units to resume (#22)
- Record a refused job start as a job.refused event (#94)
- Stop a job at its own deadline with max_runtime_seconds (#95)
- Give a sandboxed job an optional soft memory limit with memory_high (#98)
- Keep an evidence pointer on the unit record (#99)
- Add the catalog data-sync guide (#61)
- Keep the task graph in the event log: graph apply commits a staged change as one record, graph show rebuilds the graph from the log (#5)
- Run each outbound side effect at most once: a ledger in the event log keyed by task and action type, with effects list and effects resolve (#7)
- Run one work unit in a slot: slot run prepares a fresh clone, starts a sandboxed job from a one-job template, checks the result and the branch the run left, and records the unit with an evidence pointer (#25)

### Changed

- Require govulncheck and gosec on every pull request (#57)
- **BREAKING**: Rename Tribunus to Epitrop: the module is github.com/tekmora/epitrop, the command is epitropctl, the default event log directory is .epitrop and job scopes are named epitrop-job-<id>.scope

### Fixed

- Refuse a sandboxed job found outside its systemd scope (#87)
- Collect the shims a supervisor starts (#90)
- Keep the last replay error when the wait for a job start ends (#91)
- Let the supervisor tests compile on macOS and Windows (#93)
- Let schemas-repin move a pinned upstream document (#56)
- Apply event_log.lock_timeout_seconds in the supervisor and the job shim, which waited the default 5 s whatever the config said (#83)

## [0.1.0-rc.1] - 2026-10-09

Not published: the release run for this tag was cancelled, so no artefact exists for it. 0.1.0-rc.2 supersedes it.

### Added

- Run the adopted verification gates on every pull request and main branch push (#1)
- Adopt the Praetor governance harness and local verification gates (#26)
- Publish tribunusctl binaries, the snapshot schema, CycloneDX SBOMs, signed checksums and build provenance for every release tag (#29)
- Configure Renovate and exclude Praetor-managed assets from direct dependency updates (#18)
- **BREAKING**: Version the catalog snapshot with schema_version 1 and publish its JSON Schema (#29)
- Check catalog sources against pinned upstream schemas before accepting snapshot changes (#28)

### Fixed

- Refuse silent fallback in catalog source collection and surface degraded source runs explicitly (#27)
- Publish release-candidate tags as prereleases, never as the latest release (#29)
- Align the v2 architecture, roadmap and onboarding documents with the implemented catalog code (#3)

