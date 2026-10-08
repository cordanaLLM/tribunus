# Repository layout

`[now]` exists today. `[planned]` is a target for a milestone.

```text
tribunus/
  .github/workflows/ci.yml   [now]     go vet, go test -race
  LICENSE, LICENSES/         [now]     EUPL-1.2
  REUSE.toml                 [now]
  AGENTS.md                  [planned] canonical agent briefing
  Makefile                   [planned] verify target
  catalog/                   [now]     model catalog records and snapshots
  cmd/tribunusctl/           [now]     operator CLI (show, sync)
  cmd/tribunus-server/       [planned] REST, SSE and MCP gateway
  internal/sources/          [now]     catalog sources (gateway, local, public)
  internal/sot/              [planned] task graph and state
  internal/mcp/              [planned] MCP bridge
  internal/telemetry/        [planned] token and resource accounting
  crates/tribunus-graph/     [planned] Rust vector kernel
  ui/                        [planned] Svelte 5 operator UI
  docs/                      [now]
```

## Notes

- Config lives in files under Git. A Tribunus config format is defined in milestone 0.2; Paperclip files are not required.
- `internal/` packages follow the HISS code standards (function limit 60 lines).
- Runtime invariants RT-01 to RT-04 are checked by fixtures, not by HISS scans.

## Build targets (planned)

```makefile
verify-all: lint test reuse
build:
	go build -o bin/tribunusctl ./cmd/tribunusctl
	cargo build --release --workspace
	npm run --prefix ui build
```
