# Tribunus: description

Tribunus is the agent runtime and task-graph engine for the cordanaLLM ecosystem. The name comes from the Roman tribunes, who coordinated units and protected rights.

## Idea

- Organisation, routines and budgets come from files in Git.
- A task graph and a skill-vector index decide who does what.
- Every model call goes through a router alias; accelerators are one dynamic pool.
- Praetor governs the code and supplies the efficiency ledger that budgets read.

## Pillars

1. File-based, Git-native configuration. No database setup wizard. Paperclip is not required.
2. Go control plane: acyclic task graphs, bounded traversal, event bus with dead-letter triage, A2A delegation.
3. Rust vector kernel: SIMD cosine and Euclidean matching over f32, f16 and i8 vectors.
4. Svelte 5 operator UI.
5. Runtime invariants RT-01 to RT-04.
6. Budgets scaled by measured uncertainty, read from the efficiency ledger.

## Licence

EUPL-1.2 for code and documents. REUSE-annotated via `REUSE.toml`.
