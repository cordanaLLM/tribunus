# Tribunus

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
[praetor-hiss-agents]: https://github.com/cordanaLLM/tribunus/blob/HEAD/AGENTS.md
[praetor-docs-badge]: https://github.com/cordanaLLM/tribunus/actions/workflows/praetor-docs.yml/badge.svg
[praetor-docs-runs]: https://github.com/cordanaLLM/tribunus/actions/workflows/praetor-docs.yml
<!-- praetor:readme-governance:end -->

Agent runtime and task-graph engine for the cordanaLLM ecosystem.

Status: early. Today the repository holds the model catalog (`catalog/`, `cmd/tribunusctl`, `internal/sources/`), moved from `cordanaLLM/praetor` with its history. The runtime is planned in milestones 0.1 to 0.6; see the [roadmap](docs/roadmap.md).

## Build and test

```bash
go vet ./...
go test -race ./...
```

## CLI usage

```bash
tribunusctl sync [--sources=a,b] [--out=file]
tribunusctl show [--in=file]
tribunusctl schema
tribunusctl version
tribunusctl jobs start|status|stop --config=file [name]
tribunusctl jobs supervise --config=file [--poll-interval-seconds=1]
```

`tribunusctl sync` writes a versioned catalog snapshot with `schema_version: 1`.
The contract is described by `catalog/snapshot.schema.json`, and
`tribunusctl schema` prints the embedded copy for downstream pin checks.
`tribunusctl version` prints the release version, or `dev` for a source build.
Releases, their signed artefacts and how to verify them are described in [Releasing](docs/releasing.md).

## Documents

- [Agent onboarding](docs/agent-onboarding.md)
- [Roadmap](docs/roadmap.md)
- [Catalog data sync](docs/data-sync.md): installing `tribunusctl`, the sources and their limits
- [Releasing](docs/releasing.md)
- [Architecture](docs/architecture-v2.md)
- [Repository layout](docs/repository-layout-v2.md)
- [Description](docs/tribunus-description-v2.md)
- [Sources](docs/sources-list-v2.md)

## Licence

EUPL-1.2, for code and documents alike. See `LICENSE` and `REUSE.toml`.
