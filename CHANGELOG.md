# Changelog

All notable changes to this project will be documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [0.1.0-rc.1] - 2026-10-09

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

