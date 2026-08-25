# AO Mission v0.1 SDD

AO Mission creates durable mission records, route decisions, continuation steps, scheduler readbacks, gateway intents, artifact refs, and governance snapshots. It does not schedule as the mission brain and does not execute mutation.

## Pipeline

User, CLI, Telegram, or A2A creates an intent. AO Mission records the mission and routes underspecified work to AO Blueprint, Atlas-required work to AO Atlas, ready nodes to AO Foundry, and bounded execution packets to AO Forge/AO2 only through downstream gates.

## Completion

Handoff generation is not completion. A mission is done only when a final rollup is recorded or it is stopped with an exact blocker.

## Readback Additions

The current implementation adds read-only import and validation surfaces without changing the authority model:

- `validate contract` checks JSON contract shape and public-safety markers.
- `import blueprint-authorization`, `import atlas-workgraph`, and `import foundry-run-link` record artifact refs and exact next actions.
- `final rollup` summarizes mission evidence while keeping all execution flags false.
- `telegram serve --config <file>` parses allowlisted public-safe config and reports intent-only readiness.
- `a2a serve --http` exposes a local Agent Card/task handler for fixture use only.
- `schedule recover` converts stale or unknown codex-cron readback replay into an immediate continuation recommendation without executing work.
- `mission compact` trims retained route and continuation-step ledger entries while recording no-authority compaction evidence in the mission record.
