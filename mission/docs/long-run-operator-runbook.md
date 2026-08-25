# AO Mission Long-Run Operator Runbook

Use this runbook for multi-repository AO programs that need durable Mission
state across bounded execution leases. Historical program handoffs are context
only and do not authorize execution.

## Runtime Layout

Use three distinct locations:

| Purpose | Location | Rule |
| --- | --- | --- |
| Owning source context | Clean AO Mission repository root | Start the supervisor here from clean, synchronized source. |
| Shared workspace | Common parent of the active repository checkouts | Access sibling repositories; never use it as Mission's owning source context. |
| Durable state and evidence | A campaign directory outside all repositories | Preserve across worktrees, rebuilds, restarts, and compaction. |

Set `AO_MISSION_HOME` or pass `--home <dir>` before the command name. Never
commit the Mission store, generated readbacks, temporary bundles, or supervisor
binaries.

## Supervisor Pinning

Build a uniquely named supervisor from a clean AO Mission SHA and record both
the source and binary digests:

```sh
# Run from the clean AO Mission repository root.
export AO_STACK_CAMPAIGN="${AO_STACK_CAMPAIGN:-$HOME/.local/state/ao-stack/production-adoption-20260801}"
export AO_MISSION_HOME="$AO_STACK_CAMPAIGN/mission-state"
export AO_MISSION_EVIDENCE_ROOT="$AO_STACK_CAMPAIGN/evidence"

SUPERVISOR_SHA="$(git rev-parse HEAD)"
MISSION_BIN="$AO_STACK_CAMPAIGN/bin/ao-mission-$SUPERVISOR_SHA"
mkdir -p "$(dirname "$MISSION_BIN")" "$AO_MISSION_HOME" "$AO_MISSION_EVIDENCE_ROOT" "$AO_STACK_CAMPAIGN/readbacks"
go build -o "$MISSION_BIN" ./cmd/ao-mission
shasum -a 256 "$MISSION_BIN"
```

Keep using that binary until an AO Mission change has merged and passed the
self-change procedure below.

Pass `--evidence-root "$AO_MISSION_EVIDENCE_ROOT"` to `final rollup`,
`final atlas-prompt`, and `final synthesize` so emitted recommendations remain
bound to this campaign. If omitted, Mission emits an explicit
`<evidence-root>` placeholder and does not infer a repository-local directory.

## Monthly Bootstrap

The first monthly route may point at Atlas because the objective is complex.
That route is a request for compilation, not authorization to compile or run
work. Bootstrap every monthly campaign in this order:

1. Mission records the approved monthly objective, source inventory, and
   authority boundary.
2. Blueprint emits a fresh requirements pack and bounded build authorization
   for that exact monthly scope.
3. Atlas validates the Blueprint artifacts and creates the fresh monthly
   workgraph.
4. Foundry receives only the first dependency-ready node after Atlas readback.

Never reuse a historical Blueprint authorization merely because its prose is
similar, and never let Mission or Atlas manufacture approval fields.

## Lease Contract

One six-month program uses one Mission identity.
Each month uses one fresh Atlas workgraph.
Execution targets roughly 120 minutes and hard-stops at 180 minutes, with
roughly 6-10 measured, useful nodes. Useful work may finish early; elapsed time
is not a completion requirement.

Before activation, bind:

- exact source-head inventory;
- workgraph and node identities;
- measured duration estimate and estimator source;
- minimum and maximum lease duration;
- retry count and retry-eligible failures;
- node and aggregate timeouts;
- checkpoint policy; and
- return gate.

Classify expensive scale tests before partitioning. A scale declaration and a
repeat count must never multiply implicitly. Store requested and effective
repeat values separately and reject unsafe amplification.

## Mission Continuation Semantics

`ao-mission continue` records routing, continuation, checkpoint, and return
gate state. It does not execute repository work. A high `--max-iterations`
value without imported node progress only appends repeated handoff records.

Use one continuation cycle per real node:

```sh
"$MISSION_BIN" --home "$AO_MISSION_HOME" continue \
  --mission "$MISSION_ID" \
  --until-done \
  --max-iterations 1 \
  --min-nodes 8 \
  --min-minutes 0 \
  --max-minutes 180 \
  --return-only-when mission_done_or_true_hard_blocker_or_no_ready_work_and_no_exact_next_action \
  --checkpoint-policy after_each_node_or_timed_interval
```

The supervising agent performs or delegates the authorized source-owner node,
imports its readbacks, and then invokes this command once. Do not claim that a
continuation step completed an implementation node.

An explicit `--min-minutes 0` is meaningful and must remain zero in the durable
lease. Omit the flag to preserve an existing historical lease. Never wait or
pad work merely to increase elapsed duration.

## Per-Node Cycle

1. Load and verify the Mission checkpoint and current monthly terminal index.
2. Verify the current monthly Blueprint and Atlas artifact identities and
   digests.
3. Confirm the exact dependency-ready node and its authority class.
4. Verify no other mutation node is active.
5. Execute the node in an isolated source-owner branch or worktree.
6. Run focused and applicable full gates.
7. Open a bounded pull request, wait for hosted CI, merge only when green, and
   synchronize the source owner's `main` when mutation is authorized.
8. Record source heads, commands, exits, CI, artifact digests, rollback, and
   cleanup.
9. Import Atlas, Foundry, scheduler, or source-owner readbacks into Mission as
   applicable.
10. Run one Mission continuation cycle.
11. Reconcile status, inspect, checkpoint, event index, and Command readbacks.

Surface-specific `state_digest` values can differ because each surface has a
different envelope. Canonical mission identity, correlation identity, payload,
status, counts, authority, and exact next action must agree.

## Checkpoint And Restart

Inspect the durable state before and after every restart:

```sh
"$MISSION_BIN" --home "$AO_MISSION_HOME" status --mission "$MISSION_ID" --json
"$MISSION_BIN" --home "$AO_MISSION_HOME" checkpoint inspect --mission "$MISSION_ID" --json
"$MISSION_BIN" --home "$AO_MISSION_HOME" command status --mission "$MISSION_ID" --json
```

After a process restart, verify the Mission ID, objective digest, supervisor
SHA, workgraph ID, checkpoint count, imported artifact digests, route, lease,
and exact next action before resuming. A restart must not duplicate an external
effect or create a replacement Mission.

Perform at least one bounded checkpoint/restart and one compaction/replay in
each monthly campaign. Validate manifests before deleting superseded runtime
material.

## AO Mission Self-Change

When AO Mission itself needs modification:

1. Leave the durable store outside all source checkouts.
2. Keep the running supervisor pinned to its recorded clean SHA.
3. Create an isolated AO Mission task worktree.
4. Implement and verify the bounded source change there.
5. Merge through a reviewed pull request with green hosted CI.
6. Synchronize `main` and build a new uniquely named supervisor binary.
7. Record old/new source SHAs and binary SHA-256 values.
8. Read the existing Mission with the new binary.
9. Verify objective, checkpoint, route, artifact, and next-action continuity.
10. Resume the same Mission identity.

Do not hand-edit Mission JSON, move the store into the worktree, or restart the
program with a new identity merely because supervisor code changed.

## Monthly Closure

Each month closes with:

- exact source-head and authority inventory;
- canonical Atlas terminal index;
- Mission status, inspect, checkpoint, event-index, and Command reconciliation;
- node and CI evidence;
- measured lease and timeout results;
- blocker and operator-decision dispositions;
- independently rehashed artifact manifest;
- clean branch/worktree readback; and
- one exact next-month action.

Create a fresh workgraph for the next month. Never revive a historical wave.
Monthly closure does not complete the six-month Mission.

## Return Gate

Final response remains denied while any authorized useful node, bounded repair,
unreconciled readback, stale checkpoint, or exact next action remains.

A true blocker report must identify the month, workgraph, node, repository,
source SHA, command or workflow, run ID when applicable, artifact state,
attempted repairs, and smallest operator action. Missing release, pilot,
provider, credential, deployment, or migration authority is an approval gate;
it must not be inferred from program progress.
