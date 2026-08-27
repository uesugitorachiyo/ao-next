# AO Mission Operator Next Actions

AO Mission should leave the operator with a concrete command, not only a
generated artifact path. Use this short sequence when checking or continuing a
mission.

## Current AO Stack Roadmap Action

The current portfolio cycle is validating the release boundary and public
installability. It does not authorize a new release, promotion, provider call,
or external beta.

Current public releases:

- AO2 [v0.5.12](https://github.com/uesugitorachiyo/ao2/releases/tag/v0.5.12).
- AO2 Control Plane [v0.1.19](https://github.com/uesugitorachiyo/ao2-control-plane/releases/tag/v0.1.19).
- AO Mission [v0.1.6](https://github.com/uesugitorachiyo/ao-mission/releases/tag/v0.1.6),
  tag target `f631893906e3bed6f257ac30bc3d0ad2739fe9df`.
- AO Command [v0.1.3](https://github.com/uesugitorachiyo/ao-command/releases/tag/v0.1.3).
- AO Forge [v0.1.5](https://github.com/uesugitorachiyo/ao-forge/releases/tag/v0.1.5).
- AO Covenant [v0.1.1](https://github.com/uesugitorachiyo/ao-covenant/releases/tag/v0.1.1).

The authoritative public release matrix, tags, workflow runs, and digests are
maintained by [AO Architecture current releases](https://github.com/uesugitorachiyo/ao-architecture/blob/main/docs/current-release.md).
Use that document before choosing any release-related action.

AO Atlas [v0.2.1](https://github.com/uesugitorachiyo/ao-atlas/releases/tag/v0.2.1)
is a public release. Its release does not authorize a new release, promotion,
provider call, or external beta.

Next action: continue the bounded release-boundary and installability cycle.
Do not start a new release train, external beta, promotion, provider pilot,
live self-modification, or RSI work from this readback.

For doubled 2-3 hour waves, use the dedicated
[Long-Run Operator Runbook](long-run-operator-runbook.md). It defines the
bounded request shape, role routing, stop gate, per-node evidence, and Atlas
continuation prompt template.

For local private pilots on real local codebases, use the
[Local Private Pilot Workflow](local-private-pilot-workflow.md). For iOS app or
device smoke work, use the
[iOS/Xcode Local App Smoke Checklist](templates/ios-xcode-app-smoke-checklist.md).
These guides keep evidence local, preserve provided-library boundaries, and
avoid upload or deploy actions.

## Start And Inspect

```sh
ao-mission start "<objective>"
ao-mission doctor
ao-mission mission list --json
ao-mission next --mission <mission-id>
ao-mission status --mission <mission-id>
```

## Continue Locally

Select a campaign evidence root outside every source repository. Pass it to
recommendation-bearing final commands; omission leaves an explicit
`<evidence-root>` placeholder rather than selecting checkout-local storage.

```sh
export AO_MISSION_EVIDENCE_ROOT="<campaign-evidence-root>"
ao-mission continue --mission <mission-id> --until-done --max-iterations 20 --min-nodes 15 --min-minutes 0 --max-minutes 180
ao-mission mission history --mission <mission-id>
ao-mission mission events index --out tmp/mission-event-index.json
ao-mission mission events query-index --index tmp/mission-event-index.json --out tmp/mission-timeline-query-index.json
ao-mission mission events search --mission <mission-id> --query "AO Atlas" --index tmp/mission-event-index.json --json
ao-mission mission events search --mission <mission-id> --query "checkpoint" --index tmp/mission-event-index.json --json
ao-mission mission events search --mission <mission-id> --query "return_gate" --index tmp/mission-event-index.json --json
ao-mission mission events resume-prompt --mission <mission-id> --out tmp/<mission-id>-compaction-resume-prompt.json --json
ao-mission mission dashboard --mission <mission-id> --compact --out tmp/<mission-id>-dashboard.json
ao-mission mission verification-bundle --mission <mission-id> --readiness-bundle tmp/ao-mission-readiness-bundle.json --gateway-replay-bundle tmp/gateway-replay-bundle.json --out tmp/<mission-id>-verification-bundle.json
ao-mission mission compact --mission <mission-id> --keep-route-history 25 --keep-steps 25
ao-mission mission compact --mission <mission-id> --keep-route-history 25 --keep-steps 25 --timeline
ao-mission mission archive --mission <mission-id> --out tmp/<mission-id>-archive.json
ao-mission mission validate-archive --path tmp/<mission-id>-archive.json --out tmp/<mission-id>-archive-validation.json
ao-mission artifacts manifest --mission <mission-id> --out tmp/<mission-id>-artifact-manifest.json
ao-mission import atlas-recommendation-readback --mission <mission-id> --path tmp/recommendation-readback.json
ao-mission final rollup --mission <mission-id> --evidence-root "$AO_MISSION_EVIDENCE_ROOT"
ao-mission final atlas-prompt --mission <mission-id> --event-index tmp/mission-event-index.json --evidence-root "$AO_MISSION_EVIDENCE_ROOT" --out tmp/<mission-id>-atlas-prompt.json
ao-mission final synthesize --mission <mission-id> --evidence-root "$AO_MISSION_EVIDENCE_ROOT"
ao-mission final reconcile --mission <mission-id>
```

For 2-3 hour work, AO Mission owns the long-run supervisor lease and checkpoint
ledger. Atlas owns workgraph and context-heavy sequencing. Foundry owns exactly
one bounded implementation node at a time. Blueprint is used only when Mission
or Atlas lacks requirements, authorization, or a safe class boundary. Do not
route ready Atlas nodes through Blueprint for batching.

The final rollup is not a final response unless `final_response_allowed=true`.
If it is false, use `exact_next_action`, the latest checkpoint bundle, and the
Feature Depth Recommendations to continue. A true terminal hard blocker is a
blocked or denied rollup, stopped mission, explicit blocker list, or safety
boundary mismatch after repair/repack/support work has already been attempted.

Use `atlas-recommendation-readback` when AO Atlas has already executed a
recommendation wave and emitted a terminal readback. Mission closes only when
the Atlas readback reports all generated nodes complete, zero ready nodes,
checkpoint evidence for the wave, the long-run lease minimum met, and
`final_response_allowed=true`. If any of those are missing, Mission keeps the
route on AO Atlas and preserves the exact next action for continuation.

Use `examples/valid/final-reconciliation-packet.json` as the shape reference for
the final Mission reconciliation packet. A valid packet keeps
`promotion_claimed=false`, `rsi_remains_denied=true`,
`claims_authority_advance=false`, and all execution/approval flags false.

## Final Reconciliation Check

Before any final response for an imported Atlas recommendation, reconcile the
Mission and Command surfaces:

```sh
ao-mission command status --mission <mission-id>
ao-mission command status --mission <mission-id> --json
ao-mission mission events index --out tmp/mission-event-index.json
ao-mission mission events search --mission <mission-id> --kind atlas_recommendation --index tmp/mission-event-index.json --json
ao-mission mission events search --mission <mission-id> --kind final_reconciliation --index tmp/mission-event-index.json --json
ao-mission final reconcile --mission <mission-id>
```

Treat the final response as denied if Command status, event search, or final
reconciliation still shows ready nodes, a stale route, `final_response_allowed`
false, a missing Promoter no-promotion summary, a missing Foundry terminal
rollup binding, a missing Command compact timeline, or an exact next action.
The next action stays with Atlas unless the imported readback is terminal and
all no-authority closure evidence agrees.

## Windows JSON Handoff

AO Command accepts strict UTF-8 JSON without a byte-order mark (BOM). Windows
PowerShell 5.1 `Set-Content -Encoding UTF8` adds a BOM, so write Mission’s
JSON with a BOM-free .NET encoder instead:

```powershell
$status = & ao-mission command status --mission <mission-id> --json
[System.IO.File]::WriteAllText('mission-command-status.json', $status, [System.Text.UTF8Encoding]::new($false))
ao-command mission status --status mission-command-status.json
```

## Gateway Fixture Checks

```sh
ao-mission telegram replay --matrix examples/valid/telegram-command-matrix.json --config examples/valid/telegram-config.json
ao-mission telegram replay-updates --fixture examples/valid/telegram-update-replay.json --config examples/valid/telegram-config.json
ao-mission telegram webhook-replay --fixture examples/valid/telegram-webhook-replay.json --config examples/valid/telegram-config.json
ao-mission telegram role-matrix --config examples/valid/telegram-config.json --out tmp/telegram-role-matrix.json
ao-mission a2a serve --http --once
ao-mission a2a replay --fixture examples/valid/a2a-http-integration.json
ao-mission a2a lifecycle --fixture examples/valid/a2a-task-lifecycle-edges.json
ao-mission a2a compatibility --agent-card examples/valid/a2a-agent-card.json --http examples/valid/a2a-http-integration.json --lifecycle examples/valid/a2a-task-lifecycle-artifacts.json --out tmp/a2a-compatibility.json
ao-mission a2a streaming-denial --agent-card examples/invalid/a2a-agent-card-streaming.json --out tmp/a2a-streaming-denial.json
ao-mission a2a streaming-denial --agent-card examples/invalid/a2a-agent-card-streaming-sse.json --out tmp/a2a-streaming-sse-denial.json
ao-mission gateway replay-bundle --telegram-config examples/valid/telegram-config.json --telegram-matrix examples/valid/telegram-command-matrix.json --telegram-webhook examples/valid/telegram-webhook-replay.json --telegram-updates examples/valid/telegram-update-replay.json --a2a-http examples/valid/a2a-http-integration.json --a2a-lifecycle examples/valid/a2a-task-lifecycle-artifacts.json --scheduler examples/valid/scheduler-readback-replay.json --out tmp/gateway-replay-bundle.json
ao-mission gateway replay-suite --telegram-config examples/valid/telegram-config.json --telegram-webhook examples/valid/telegram-webhook-replay.json --telegram-updates examples/valid/telegram-update-replay.json --a2a-http examples/valid/a2a-http-integration.json --a2a-lifecycle examples/valid/a2a-task-lifecycle-artifacts.json --out tmp/gateway-replay-suite.json
ao-mission gateway readiness-rollup --mission <mission-id> --suite tmp/gateway-replay-suite.json --a2a-compatibility tmp/a2a-compatibility.json --archive-validation tmp/<mission-id>-archive-validation.json --snapshot-diff <snapshot-diff.json> --out tmp/gateway-readiness-rollup.json
ao-mission schedule replay --fixture examples/valid/scheduler-readback-replay.json
ao-mission schedule alerts --fixture examples/valid/scheduler-readback-replay.json
ao-mission schedule recover --mission <mission-id> --fixture examples/valid/scheduler-readback-replay.json
ao-mission import scheduler-recovery-readback --mission <mission-id> --path examples/valid/scheduler-recovery-readback.json
ao-mission import ledger-compaction-readback --mission <mission-id> --path examples/valid/ledger-compaction-readback.json
ao-mission validate contract --path examples/valid/timeline-compaction-readback.json
ao-mission mission compact --mission <mission-id> --keep-route-history 25 --keep-steps 25 --dry-run
ao-mission artifacts repair-manifest --path <artifact-manifest.json> --out <artifact-manifest.repaired.json>
ao-mission governance diff --before <snapshot-before.json> --after <snapshot-after.json>
ao-mission mission archive --mission <mission-id> --out tmp/<mission-id>-archive.json
```

## Cross-Repo Readiness Bundle

```sh
ao-mission mission readiness-bundle --repo ao-mission=tmp/ao-mission-readiness.txt --repo ao-atlas=tmp/ao-atlas-readiness.txt --out tmp/ao-mission-readiness-bundle.json
```

Use local readiness summaries only. The bundle records status and SHA-256
digests for operator review; it does not push branches, open PRs, wait for
hosted CI, merge, sync main, or delete branches.

The verification bundle is the final local handoff packet for this loop. It
binds the event index, dashboard, artifact manifest, readiness bundle, and
gateway replay bundle with SHA-256 digests, but it still does not perform
credentialed remote lifecycle work.

Telegram and A2A fixture checks record intent/readback only. They do not grant
execution authority, approval authority, repository mutation, provider calls,
credential use, release/deploy/publish/upload/tag authority, dependency update
authority, direct-main mutation, concurrent mutation, hidden instruction
mutation, unrestricted self-modification, unrestricted RSI, or broad_RSI.

## Route Handoff

If `ao-mission next --mission <mission-id>` returns `ao-blueprint`, Move to AO
Blueprint and create or inspect the Blueprint pack.

If it returns `ao-atlas`, Move to AO Atlas and compile/import the authorized
Mission context into an Atlas workgraph.

If it returns `ao-foundry`, Move to AO Foundry and consume only the first safe
Atlas node through Foundry gates.

If it returns `complete`, read the final rollup and recommended next tasks. Do
not treat a generated handoff file alone as completion.

## Long-Run Role Routing

Use AO Mission when the operator asks for a 2-3 hour run, minimum node budget,
checkpoint/resume behavior, final-response gating, or cross-repo readback
reconciliation.

Use AO Atlas when the work needs an ordered workgraph, context pack, exact next
ready node, repair plan, or Feature Depth Recommendation wave.

Use AO Foundry when Atlas has produced a ready bounded node and the next action
is implementation evidence, tests, rollback evidence, or a run-link/final
rollup.

Use AO Blueprint only for missing requirements, missing authorization, or an
underspecified objective. Blueprint should not be used to split ordinary ready
implementation batches.

For doubled waves, Blueprint is not a batching queue. Use the long-run runbook
and route ready Atlas nodes directly to Foundry one bounded implementation node
at a time.
