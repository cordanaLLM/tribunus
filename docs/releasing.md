# Releasing Tribunus

A release is a `v*` tag on a commit of `main`. The tag starts `.github/workflows/release.yml`, which publishes `tribunusctl`, the snapshot schema and the evidence that they were built from that commit. The first tag is an operator decision.

The flow follows Praetor's: the same GoReleaser layout, `changelog.d/` fragments rendered by `praetorctl release`, and a tag made after the merge ([Praetor's releasing guide](https://github.com/cordanaLLM/praetor/blob/main/docs/guides/releasing.md)).

## What a tag publishes

`release.yml` calls the reusable `.github/workflows/release-binaries.yml`. That workflow runs `goreleaser check`, builds `./cmd/tribunusctl` into a draft release (`.goreleaser.yaml`), signs `checksums.txt` with keyless cosign, verifies that signature against its own workflow identity, attests every checksummed artefact with `actions/attest-build-provenance`, and only then publishes the release. `praetorctl audit` measures this as SLSA Build Level 3 with cosign signing and an SBOM, so `.standards.yaml` declares no HISS-11 exception.

| Asset | Content |
| --- | --- |
| `tribunusctl_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) | the binary for linux, darwin and windows on amd64 and arm64, with `README.md` and the licences |
| `<archive>.cyclonedx.json` | a CycloneDX SBOM for each archive (syft) |
| `checksums.txt`, `checksums.txt.sigstore.json` | SHA-256 of every artefact and its keyless cosign bundle |
| `snapshot.schema.json` | the snapshot contract this release writes (`catalog/snapshot.schema.json`, `schema_version` 1) |

`tribunusctl version` prints the version the release build injected (`-X main.version`); a build from source prints `dev`.

## Verifying a release

```bash
cosign verify-blob \
  --certificate-identity "https://github.com/cordanaLLM/tribunus/.github/workflows/release-binaries.yml@refs/tags/<tag>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --bundle checksums.txt.sigstore.json checksums.txt
sha256sum --check --ignore-missing checksums.txt
gh attestation verify tribunusctl_<version>_linux_amd64.tar.gz --repo cordanaLLM/tribunus
```

## How Praetor pins Tribunus

Praetor reads snapshots through the catalog package, pinned as a Go module: `github.com/cordanaLLM/tribunus/catalog` at a pseudo-version before the first tag, at a tag after it. The package imports only the standard library (`TestCatalogImportsStdlibOnly`) and embeds the schema as `catalog.SnapshotSchema()`. The binary comes from the release assets.

## Cutting a release

1. Every merged user-visible change carries one fragment under `changelog.d/`: `type`, `title`, optional `issue`, and `breaking`.
2. On a branch cut from `origin/main`, render the changelog. This rewrites `CHANGELOG.md` and removes the rendered fragments:

   ```bash
   praetorctl release --version=<tag> --date=<YYYY-MM-DD>
   ```

3. Merge that change through a pull request.
4. Tag the merged commit and push only the tag:

   ```bash
   git fetch origin
   git tag -s <tag> -m "<tag>" origin/main
   git push origin <tag>
   ```

5. Check the run with `gh run list --workflow release.yml --limit 1` and the result with `gh release view <tag>`.
