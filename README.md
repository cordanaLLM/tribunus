# Tribunus

Agent runtime and task-graph engine for the cordanaLLM ecosystem.

Status: early. Today the repository holds the model catalog (`catalog/`, `cmd/tribunusctl`, `internal/sources/`), moved from `cordanaLLM/praetor` with its history. The runtime is planned in milestones 0.1 to 0.5.

## Build and test

```bash
go vet ./...
go test -race ./...
```

## Documents

- [Agent onboarding](docs/agent-onboarding.md)
- [Architecture](docs/architecture-v2.md)
- [Repository layout](docs/repository-layout-v2.md)
- [Description](docs/tribunus-description-v2.md)
- [Sources](docs/sources-list-v2.md)

## Licence

EUPL-1.2, for code and documents alike. See `LICENSE` and `REUSE.toml`.
