# Catalog data sync

`tribunusctl sync` collects what Tribunus needs to know about models from a handful of sources and writes it to one JSON snapshot file: what each model costs, how much context it takes, which access path reaches it, and how much of a provider's limit is already spent. `tribunusctl show` renders a snapshot as a table. The routing that reads these snapshots comes later (milestone 0.5); this page covers the sync.

## Install

Releases are tagged `v*` and carry signed binaries ([Releasing](releasing.md)). Download the archive for your platform with `checksums.txt` and `checksums.txt.sigstore.json`, then verify before unpacking:

```bash
cosign verify-blob \
  --certificate-identity "https://github.com/cordanaLLM/tribunus/.github/workflows/release-binaries.yml@refs/tags/<tag>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --bundle checksums.txt.sigstore.json checksums.txt
sha256sum --check --ignore-missing checksums.txt
gh attestation verify tribunusctl_<version>_linux_amd64.tar.gz --repo cordanaLLM/tribunus
```

To build from source instead, pin the version:

```bash
go install github.com/cordanaLLM/tribunus/cmd/tribunusctl@<tag>
```

`tribunusctl version` prints the release version; a source build prints `dev`.

## Running it

```bash
# every source, written to ./tribunus-snapshot.json
tribunusctl sync

# a subset, explicit output path
tribunusctl sync --sources=codex-local,ollama-local --out=/tmp/snapshot.json

# litellm-gateway needs both flags, or it reports a skip
tribunusctl sync --sources=litellm-gateway \
  --litellm-base=https://litellm.example.com \
  --litellm-token-file="$HOME/.config/<gateway>/agent-token"

# render a snapshot; print the snapshot's JSON Schema
tribunusctl show --in=/tmp/snapshot.json
tribunusctl schema
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--sources` | all four, in the order of the table below | comma-separated; an empty, duplicate or unknown entry is an error |
| `--out` | `tribunus-snapshot.json` | where the snapshot is written |
| `--litellm-base`, `--litellm-token-file` | none | required together for `litellm-gateway`; the token is read from the file and never printed |
| `--ollama` | `http://localhost:11434` | Ollama daemon endpoint |
| `--sessions-dir` | `~/.codex/sessions` | Codex CLI session logs |
| `--openrouter-url`, `--litellm-prices-url` | the public OpenRouter API; the LiteLLM price map at a pinned commit | override only to test against a local fixture |

`sync` prints one `<source> <status> count=<n> [detail]` line per source. Sources run independently: one failing never discards another's records. The status is `ok`, `skip` (the source cannot run here, such as missing flags or no session logs), `fail`, or `degraded` (records were produced but upstream data was lost, such as rejected entries or one of the two public catalogs failing). `sync` exits non-zero when every selected source fails, after printing every line. The whole command times out after 3 minutes (`syncTimeout` in `cmd/tribunusctl/sync.go`).

## The snapshot

A snapshot is `catalog.Snapshot` with `schema_version: 1`. Its JSON Schema is `catalog/snapshot.schema.json`, printed by `tribunusctl schema` and shipped with every release. `tribunusctl show` refuses a snapshot without a supported `schema_version` and asks for a new `sync`. A snapshot is exactly what one sync produced: nothing is merged across runs or sources.

Each record (`catalog.Record`) carries a model id, provider, access path (`api`, `subscription_cli`, `gateway` or `local`), context window, price per million tokens in and out, capabilities, rate limits and a usage window. Only the model id, access path and provenance are required. Provenance is `{source, fetched_at, kind}`: `measured` means read live from a real system, and `declared` means copied from a published catalog. A value a source does not report stays empty, and `Record.Absent` names it with a reason; nothing is guessed. A record that fails `Record.Validate` is rejected and counted in its source's line, and the rest of the snapshot is still written.

## Sources

| Source | How | Kind | Bounds | Failure and edge cases |
| --- | --- | --- | --- | --- |
| `codex-local` | Reads `rate_limits.primary` from the newest `~/.codex/sessions/**/*.jsonl`, read-only | measured | 20,000 files walked, 128 MiB per file, 4 MiB per line; one record | no logs or no rate limits: skip; an empty `limit_id`: fail |
| `litellm-gateway` | `GET <base>/v1/models` with a bearer token | measured | 15 s, 8 MiB, 5,000 models | an empty model list: fail; an entry without an id is rejected and counted; a list where no entry has an id: fail. Prices and context are absent, because `/model/info` is not readable with an agent token |
| `ollama-local` | `GET /api/tags` for installed models, `GET /api/ps` for loaded ones | measured | 5 s, 8 MiB, 5,000 models | no installed models: skip (a real local state); `/api/ps` unavailable: records kept, loaded state absent, status degraded |
| `public-catalog` | OpenRouter's public models API and LiteLLM's price map | declared | 20 s and 16 MiB per sub-fetch, 4,999 records each | an empty list or map: that sub-fetch fails; one sub-fetch failing: degraded; a malformed entry is counted (`malformed=N`, `rejected=N`); an OpenRouter `-1` price is absent as `variable price`, never negative |

The two public catalogs use incompatible model-id schemes (for example `openai/gpt-4` and `gpt-4`). The sync does not reconcile them; each record names its sub-feed in its provenance (`public-catalog:openrouter` or `public-catalog:litellm-prices`).

Every parser is checked against a schema fragment taken from the upstream's published document at a pinned revision (`internal/sources/upstream-schemas.json`; `make schemas-check`, and `make schemas-repin` after Renovate moves a pin). A rename or retype upstream fails a parser test instead of dropping a field.

Two sources are not implemented, on purpose: a Claude subscription usage source (no current local store to read) and a Gemini quota source (no readable local store). Neither is scraped from an undocumented endpoint.

## Verification notes

The sources were first verified live on 2026-09-18, before their parsers were written:

- a real Codex session log showed the `rate_limits.primary` shape the parser reads;
- a LiteLLM gateway returned HTTP 200 for `/v1/models` and 403 for `/model/info` with an agent token;
- a local Ollama daemon answered both endpoints;
- both public catalog URLs returned real bodies.

The pinned schema fragments record the revision and full-document digest each parser was last checked against (2026-10-09).

## Out of scope

Routing, scheduling or repeating a sync, scraping undocumented endpoints, and persistence beyond the snapshot file are not part of the sync. A snapshot is a point-in-time file that an operator or a later routing layer reads.
