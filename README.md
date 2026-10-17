# Epitrop

<!-- praetor:readme-governance:start -->
[![HISS Adopted][praetor-hiss-badge]][praetor-hiss-agents]
[![Documentation Governance][praetor-docs-badge]][praetor-docs-runs]

Praetor manages this repository's declared governance policy. This managed
block records adoption state; it is not a verification certificate.

**Verification**: `make verify-all` runs the repository's configured
verification cascade.

**HISS Audit**: `praetorctl audit` enforces policy, generated-surface
integrity, and the debt ratchet.

**Context Sync**: `praetorctl compile-context --verify` verifies every
generated agent context against `AGENTS.md`.

**Documentation**: `make docs-lint` enforces locked Markdown style and the
private scratch-link policy.

**Debt Baseline**: `.standards-baseline.json` anchors the debt ratchet at
0 recorded infractions; audit forbids growth.

[praetor-hiss-badge]: https://img.shields.io/badge/Standards-HISS%20Adopted-blue
[praetor-hiss-agents]: https://github.com/tekmora/epitrop/blob/HEAD/AGENTS.md
[praetor-docs-badge]: https://github.com/tekmora/epitrop/actions/workflows/praetor-docs.yml/badge.svg
[praetor-docs-runs]: https://github.com/tekmora/epitrop/actions/workflows/praetor-docs.yml
<!-- praetor:readme-governance:end -->

Agent runtime and task-graph engine for the Tekmora ecosystem.

Status: early. Today the repository holds the model catalog (`catalog/`, `cmd/epitropctl`, `internal/sources/`), moved from `cordanaLLM/praetor` with its history. The runtime is planned in milestones 0.1 to 0.6; see the [roadmap](docs/roadmap.md).

## The name

The runtime is called **Epitrop** (eh-PEE-trop), from Greek *epitropos*, the
appointed trustee. Tekmora (tek-MOH-ra), the family the name belongs to, comes
from Greek *tekmerion*, "decisive proof". The repository was first named
`tribunus`, in another organisation; its old address redirects.

## Build and test

```bash
go vet ./...
go test -race ./...
```

## CLI usage

```bash
epitropctl sync [--sources=a,b] [--out=file]
epitropctl show [--in=file]
epitropctl headroom --max-age=duration [--in=file]
epitropctl schema
epitropctl version
epitropctl jobs start|status|stop --config=file [name]
epitropctl jobs supervise --config=file [--poll-interval-seconds=1]
```

`epitropctl sync` writes a versioned catalog snapshot with `schema_version: 1`.
The contract is described by `catalog/snapshot.schema.json`, and
`epitropctl schema` prints the embedded copy for downstream pin checks.
`epitropctl version` prints the release version, or `dev` for a source build.
Releases, their signed artefacts and how to verify them are described in [Releasing](docs/releasing.md).

`epitropctl` exports OpenTelemetry traces and metrics over OTLP once
`OTEL_EXPORTER_OTLP_ENDPOINT` or a signal-specific endpoint is set. With no endpoint set, nothing is exported.
[Telemetry](docs/telemetry.md) lists the spans and metrics.

## MCP server

```bash
epitrop-server --config=file
```

`epitrop-server` serves the run record over the Model Context Protocol on stdin and stdout, for a client that starts it as a subprocess. Its tools only read, and no tool appends to the event log or signals a job:

- `jobs_status` lists the configured jobs as `epitropctl jobs status` does. On a platform without the supervisor (anything but Linux) the tool returns the reason as a tool error.
- `units_list` lists the work units of the run record, sorted by id, optionally narrowed to one status.
- `unit_status` shows one unit as `epitropctl units show` does, with the count of unread notes in place of the notes. Reading does not mark a note read.

The tool names are provisional until the first release that ships the server.

## Documents

- [Agent onboarding](docs/agent-onboarding.md)
- [Roadmap](docs/roadmap.md)
- [Cutover 1](docs/cutover-1.md): the background jobs Epitrop takes over, and the parallel run before each switch-over
- [Catalog data sync](docs/data-sync.md): installing `epitropctl`, the sources and their limits
- [Releasing](docs/releasing.md)
- [Architecture](docs/architecture-v2.md)
- [Telemetry](docs/telemetry.md): spans and metrics, and how to export them
- [Repository layout](docs/repository-layout-v2.md)
- [Description](docs/epitrop-description-v2.md)
- [Sources](docs/sources-list-v2.md)

## Licence

EUPL-1.2, for code and documents alike. See `LICENSE` and `REUSE.toml`.
Third-party code that `epitropctl` includes, with the notice each licence asks to carry, is listed in
`NOTICE.md`.
