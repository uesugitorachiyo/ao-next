# AO Mission Gateway Readback Runbook

AO Mission uses Hermes-style gateway separation: CLI, Telegram, and A2A are entry points into one durable mission ledger, but gateway messages create operator intents and readbacks only. They do not execute mutation, approve policy, call providers, publish releases, use credentials, or widen repository authority.

## Telegram

Telegram is disabled by default. A config file names the environment variable that holds the real bot credential and an allowlist of chat IDs. AO Mission never prints or persists the credential value. Public fixtures use fake chat IDs and redacted credential names only.

Supported Telegram commands are represented in `examples/valid/telegram-command-matrix.json`:

- `/status`
- `/next`
- `/continue`
- `/pause`
- `/resume`
- `/stop`
- `/approve`
- `/deny`
- `/where`
- `/help`

Every command returns `mutation_authority=false`. Admin commands are still requests for AO Mission continuation or readback, not direct execution. Unsupported slash commands and non-slash command strings are rejected by the invalid fixtures under `examples/invalid/`; non-admin `/continue` and `/approve` remain denied intents.

Use `ao-mission telegram replay-updates --fixture examples/valid/telegram-update-replay.json --config examples/valid/telegram-config.json` to replay Telegram-style update payloads. The update replay proves allowlisted chat IDs, denied admin intents, invalid non-slash text, and `mutation_authority=false` without contacting Telegram or storing a token value.

Use `ao-mission telegram webhook-replay --fixture examples/valid/telegram-webhook-replay.json --config examples/valid/telegram-config.json` for webhook fixture parity. Webhook replay uses the same allowlist and command rules as update replay, records intent/readback only, never contacts Telegram, and classifies replay fixture freshness as `fresh`, `stale`, or `unknown` from public-safe timestamps.

Use `ao-mission gateway replay-suite --telegram-config examples/valid/telegram-config.json --telegram-webhook examples/valid/telegram-webhook-replay.json --telegram-updates examples/valid/telegram-update-replay.json --a2a-http examples/valid/a2a-http-integration.json --a2a-lifecycle examples/valid/a2a-task-lifecycle-artifacts.json --out tmp/gateway-replay-suite.json` to bind Telegram webhook/update replay and A2A HTTP/lifecycle replay into one no-authority suite readback.

Use `ao-mission telegram role-matrix --config examples/valid/telegram-config.json --out tmp/telegram-role-matrix.json` to export allowlisted Telegram roles without token values. The role matrix is readback only and does not grant admin commands direct mutation authority.

## A2A

The local A2A gateway exposes an Agent Card and JSON-RPC style task readbacks for local interoperability. It follows the A2A idea of agent-to-agent communication without exposing AO Mission internals as unrestricted tools. The Agent Card includes local fixture metadata, `streaming=false`, `push_notifications=false`, `mutation_authority=false`, structured capability detail, and readback-only skills for mission status, continuation intents, and artifact refs. External agents may request `mission.start`, `mission.status`, `mission.next`, `mission.continue`, `mission.pause`, `mission.resume`, `mission.cancel`, `mission.artifacts`, and `mission.governance_snapshot`.

Parameter validation is intentionally strict:

- `mission.start` requires an objective.
- Status, next, continue, pause, resume, cancel, artifacts, and governance snapshot requests require a mission ID.
- Unknown methods produce an invalid readback.

The invalid JSON-RPC fixtures under `examples/invalid/` cover missing objective, missing mission ID, and unknown method requests.

Every A2A response remains intent/readback only and reports `mutation_authority=false`.

Use `ao-mission a2a serve --http --once` to emit a local fixture-server readback for the Agent Card and JSON-RPC paths without starting a long-running process. The readback records `/.well-known/agent-card.json`, `/`, supported JSON-RPC methods, and all no-authority flags so Atlas and Foundry can bind local A2A readiness without treating the gateway as execution approval.

`examples/valid/a2a-task-lifecycle-edges.json` covers resume and cancel lifecycle edges. `examples/valid/a2a-task-lifecycle-artifacts.json` covers artifact readbacks and shows that artifact refs are pointers only. Resume, cancellation, and artifact states are readback evidence only; they do not grant mutation, scheduling, approval, or repository write authority.

Use `ao-mission a2a compatibility --agent-card examples/valid/a2a-agent-card.json --http examples/valid/a2a-http-integration.json --lifecycle examples/valid/a2a-task-lifecycle-artifacts.json --out tmp/a2a-compatibility.json` to validate Agent Card, JSON-RPC, lifecycle, and artifact readback compatibility as one fixture-backed packet.

Use `ao-mission a2a streaming-denial --agent-card examples/invalid/a2a-agent-card-streaming.json --out tmp/a2a-streaming-denial.json` and `ao-mission a2a streaming-denial --agent-card examples/invalid/a2a-agent-card-streaming-sse.json --out tmp/a2a-streaming-sse-denial.json` to prove streaming, push-style, and SSE capability requests stay denied unless separately gated.

Use `ao-mission a2a cancellation-replay --lifecycle examples/valid/a2a-task-lifecycle.json --out tmp/a2a-cancellation-replay.json` to prove A2A cancellation requests and cancelled states remain lifecycle readbacks, not execution authority.

Use `ao-mission gateway readiness-rollup --mission <mission-id> --suite tmp/gateway-replay-suite.json --a2a-compatibility tmp/a2a-compatibility.json --archive-validation tmp/archive-validation.json --snapshot-diff tmp/snapshot-diff.json --correlation-id corr-gateway-001 --out tmp/gateway-readiness-rollup.json` after generating the referenced packets to bind gateway readiness into one no-authority summary. The `mission_id` lets Atlas and Foundry validate the rollup against the mission record, and the `correlation_id` connects replay artifacts to downstream Atlas, Foundry, and Command rollups without approving, scheduling, or executing work.

The reference pattern from Hermes is a single gateway process with cross-session continuity; AO Mission keeps that pattern but narrows it to intent/readback artifacts. The reference pattern from A2A is Agent Card discovery, task lifecycle, streaming, push notifications, and cancellation; AO Mission advertises readback-only skills, keeps `streaming=false` and `push_notifications=false`, and records cancellation as evidence only.

## Timeline Compaction

Use `ao-mission mission compact --mission <mission-id> --keep-route-history 25 --keep-steps 25 --timeline` when downstream repos need a digest-bound retained mission timeline. The emitted `ao.mission.timeline-compaction-readback.v0.1` records before/after route and continuation-step counts, `timeline_digest`, and no-authority flags. It is evidence for Mission ledger hygiene only; it does not complete generated work, approve work, or mutate repositories.

## Operator Rule

Gateway output is never completion evidence by itself. AO Mission must still route authorized work through Blueprint, Atlas, Foundry, Forge/AO2, Covenant, Sentinel, Promoter, Command, CI, rollback, eval/regression, and Architecture wording gates as applicable.
