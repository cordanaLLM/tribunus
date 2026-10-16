#!/usr/bin/env bash
# SPDX-License-Identifier: EUPL-1.2
# Writes the v0.1.0 fixture set with the published v0.1.0 binary (linux/amd64).
# Run from this folder: bash generate.sh. Needs gh, python3, sha256sum and
# anonymous read access to api.github.com (the release watch seed asks
# GitHub for the releases of tekmora/epitrop). The signing key is a fixed
# test seed, the same the tests use; it signs nothing outside these fixtures.
set -euo pipefail
version=v0.1.0
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'if [ -n "${standin:-}" ]; then kill "$standin" 2>/dev/null || true; fi; rm -rf "$work"' EXIT

cd "$work"
gh release download "$version" --repo tekmora/epitrop \
  --pattern 'epitropctl_0.1.0_linux_amd64.tar.gz' --pattern checksums.txt
sha256sum --check --ignore-missing checksums.txt
tar -xzf epitropctl_0.1.0_linux_amd64.tar.gz epitropctl
bin="$work/epitropctl"
"$bin" version

# The fixed test seed: 32 bytes of 0x07.
printf '0707070707070707070707070707070707070707070707070707070707070707' > seed.hex
chmod 600 seed.hex
mkdir -p eventlog/.git
printf '{"event_log":{"dir":"%s","signing_key_path":"%s"}}' "$work/eventlog" "$work/seed.hex" > config.json

# Event log: unit records, a note and a graph stage.
"$bin" units set --config=config.json --status=proposed epitrop:unit-1
"$bin" units set --config=config.json --status=ready epitrop:unit-1
"$bin" units note --config=config.json --from=fixture epitrop:unit-1 "fixture note"
printf '%s' '{"ops":[{"type":"graph.node_added","node":{"id":"task-1","kind":"unit","title":"Task 1","status":{"name":"proposed"}}},{"type":"graph.node_added","node":{"id":"task-2","kind":"unit","title":"Task 2","status":{"name":"proposed"}}},{"type":"graph.edge_added","edge":{"node_id":"task-2","depends_on":"task-1"}}]}' > stage.json
"$bin" graph apply --config=config.json --file=stage.json > /dev/null

# Snapshot: one model from a local Ollama stand-in.
coproc STANDIN { python3 "$here/ollama_standin.py"; }
standin=$STANDIN_PID
read -r port <&"${STANDIN[0]}"
"$bin" sync --sources=ollama-local --ollama="http://127.0.0.1:$port" --out=snapshot.json

# Release watch state: seed one route against GitHub.
mkdir -p release-watch
printf '{"release_watch":{"state_dir":"%s","routes":[{"name":"fixture","sources":[{"github":"tekmora/epitrop"}],"sinks":[{"ntfy":{"topic":"fixture-topic"}}]}]}}' "$work/release-watch" > watch.json
"$bin" watch seed --config=watch.json

rm -rf "$here/eventlog" "$here/release-watch"
cp -a eventlog "$here/eventlog"
rm -rf "$here/eventlog/.git" "$here/eventlog/events/.lock"
cp -a release-watch "$here/release-watch"
cp snapshot.json "$here/snapshot.json"
echo "fixtures written to $here"
