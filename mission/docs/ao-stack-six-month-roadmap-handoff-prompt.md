# AO Stack Six-Month Production Adoption Handoff

- Status: approved execution handoff
- Program window: August 1, 2026 through January 31, 2027
- Execution owner: AO Mission
- Design authority: AO Architecture
- Shared workspace: the common parent of the active repository checkouts
- Durable state: outside every source checkout
- Publication authority: not granted

This is the canonical executable handoff for the AO Stack production-adoption
program. AO Architecture owns the portfolio design, repository inventory,
authority map, and public status. AO Mission owns the durable program record,
monthly checkpoints, routing, continuation, and final reconciliation.

Run the program from the AO Mission repository. Treat the shared `public`
directory only as the workspace root that exposes sibling repositories. Do not
run the program with the shared directory as its owning source context.

The companion [long-run operator runbook](long-run-operator-runbook.md) defines
the exact lease, checkpoint, restart, compaction, and AO Mission self-change
procedure. The approved roadmap design is maintained in AO Architecture at
`docs/superpowers/specs/2026-08-01-ao-stack-production-adoption-roadmap.md`.

## Initialization

Start from a clean, synchronized AO Mission `main`. Pin and build the
supervisor before any AO Mission source change. Keep its state and launch
readbacks outside the repository:

```sh
# Run from the clean AO Mission repository root.
export AO_STACK_WORKSPACE="$(cd .. && pwd -P)"
export AO_STACK_CAMPAIGN="${AO_STACK_CAMPAIGN:-$HOME/.local/state/ao-stack/production-adoption-20260801}"
export AO_MISSION_HOME="$AO_STACK_CAMPAIGN/mission-state"

mkdir -p "$AO_STACK_CAMPAIGN/bin" "$AO_MISSION_HOME" "$AO_STACK_CAMPAIGN/readbacks"

SUPERVISOR_SHA="$(git rev-parse HEAD)"
MISSION_BIN="$AO_STACK_CAMPAIGN/bin/ao-mission-$SUPERVISOR_SHA"
go build -o "$MISSION_BIN" ./cmd/ao-mission

PROMPT_FILE=docs/ao-stack-six-month-roadmap-handoff-prompt.md
MISSION_JSON="$("$MISSION_BIN" --home "$AO_MISSION_HOME" start "$(sed -n '/^## Mission Prompt$/,$p' "$PROMPT_FILE")")"
printf '%s\n' "$MISSION_JSON" > "$AO_STACK_CAMPAIGN/readbacks/mission-start.json"
MISSION_ID="$(printf '%s' "$MISSION_JSON" | jq -r '.mission_id')"

"$MISSION_BIN" --home "$AO_MISSION_HOME" continue \
  --mission "$MISSION_ID" \
  --until-done \
  --max-iterations 1 \
  --min-nodes 8 \
  --min-minutes 0 \
  --max-minutes 180 \
  --return-only-when mission_done_or_true_hard_blocker_or_no_ready_work_and_no_exact_next_action \
  --checkpoint-policy after_each_node_or_timed_interval \
  > "$AO_STACK_CAMPAIGN/readbacks/mission-initial-continue.json"
```

`continue` records durable continuation and checkpoint state.
It does not run an Atlas node, modify a repository, approve work, or publish.
Invoke one continuation cycle after importing the evidence for each real node.
Do not use a large repeat count to manufacture progress.
Useful work may finish before the target duration. An explicit zero-minute
minimum must remain zero; never wait or pad execution to increase elapsed time.

The complex objective normally routes to `ao-atlas` immediately.
A current Atlas route is not build authorization.
Before Atlas creates the first monthly workgraph, the supervisor must produce
or validate a fresh AO Blueprint pack and bounded build-authorization packet
for that month's declared scope.

## Mission Prompt

You are supervising AO Mission for one six-month AO Stack production-adoption
program running from August 1, 2026 through January 31, 2027.

Preserve this complete objective in one fresh Mission record. Execute it as six
monthly campaigns, each with a fresh Atlas workgraph and bounded 120-180 minute
leases. Continue automatically across ready, authorized work. Pause only at an
explicit authority gate or a true infrastructure blocker.

### Objective

Make the active AO Stack installable, understandable, supportable, recoverable,
and useful for platform teams running governed AI-assisted software-engineering
workflows. AO2 is the reference product and integration proof. Each component
retains its documented authority and implementation contracts.

The program succeeds only when exact-head source evidence, clean-host product
journeys, recovery exercises, operator readbacks, assurance results, and public
documentation agree. Calendar milestones, readiness readbacks, and this
handoff do not authorize a release.

### Sources Of Truth

Use these sources in order:

1. AO Architecture's active repository inventory and authority map.
2. Source-owner contracts, tests, workflows, and release metadata.
3. This Mission handoff and the long-run operator runbook.
4. Fresh monthly evidence bound to exact source heads.

Resolve the active repository set from AO Architecture at campaign start. The
current approved roadmap scope contains these 14 hosted repositories:

- `ao-architecture`
- `ao-arena`
- `ao-atlas`
- `ao-blueprint`
- `ao-command`
- `ao-covenant`
- `ao-crucible`
- `ao-forge`
- `ao-foundry`
- `ao-mission`
- `ao-promoter`
- `ao-sentinel`
- `ao2`
- `ao2-control-plane`

Do not silently add a local stub, fixture, legacy repository, or newly found
directory. A scope change requires a reviewed Architecture lifecycle decision.

### Operating Model

- AO Mission owns the program identity, durable ledger, routing, checkpoints,
  imported readbacks, and final reconciliation.
- AO Blueprint owns the fresh monthly requirements pack, scope sufficiency,
  traceability, and bounded build authorization required before Atlas.
- AO Atlas owns one fresh dependency workgraph per month. Historical waves are
  immutable evidence and must not be resumed.
- AO Foundry selects and delegates one dependency-ready implementation node at
  a time.
- Source repositories own implementation and verification.
- AO Command provides read-only operator status.
- AO Architecture owns portfolio truth and a concise execution pointer, not a
  duplicate operational handoff.
- The shared workspace provides sibling repository access only.

Every monthly workgraph must have a new identity, exact source-head inventory,
declared lease, bounded node set, explicit dependencies, acceptance criteria,
verification commands, authority class, and exact next action.

Use 8-12 useful nodes for an ordinary lease. Increase the node count only when
measured task history proves the work fits inside the 120-180 minute lease.
Classify scale tests before partitioning, bind retry and timeout policy before
activation, and prevent scale multiplied by repeat-count amplification.

### Authority Boundaries

This handoff authorizes provider-free inventory, planning, tests, fixtures,
documentation repair, isolated source changes, pull requests, hosted CI, and
post-merge readback needed for the monthly gates.

It does not authorize:

- provider-backed execution or external pilot contact;
- credentials, private data, proprietary source, or permission changes;
- deployment, migration, package retirement, or repository archival;
- tag creation, release creation, asset upload, or publication;
- direct pushes to `main`;
- weakening a policy or verifier to make a gate pass; or
- treating Mission, Atlas, Command, Sentinel, or Promoter readback as approval.

Use isolated task branches and worktrees. Open bounded pull requests, wait for
required hosted checks, merge only when green, synchronize `main`, and remove
task branches and worktrees. Release and pilot gates require separate explicit
operator approval when reached.

### Model And Provider Policy

Do not hardcode or infer model availability from historical documentation.
Before any provider-backed node, resolve the requested profile through the
current approved runtime configuration and record:

- requested provider, profile, model, and reasoning effort;
- resolved provider, profile, model, and reasoning effort;
- configuration source and digest;
- fallback or substitution decision; and
- operator authority covering the call.

Fail closed when a requested profile is unavailable or substitution is not
explicitly authorized. Provider-free work should remain provider-free.

### Current Baseline

Treat all pre-August roadmap findings as historical inputs, not current truth.
Recheck them before scheduling repair.

The initial known current-state item is AO2 `v0.5.7` post-release stable-train
and documentation reconciliation. If the operator supplies the bounded
`ao2-v057-post-release-stable-train-doc-reconciliation-20260801` handoff,
import it as the first Month 1 candidate node only after validating that its
source heads, authority, and acceptance criteria remain current. Do not treat
the existence of that handoff as evidence that its work is complete.

### Month 1: Stack Truth And Adoption Baseline

Window: August 1-31, 2026.

1. Record the fresh 14-repository source-head, branch, cleanliness, lifecycle,
   release, and CI inventory.
2. Reconcile current AO2 and Control Plane release truth, including AO2
   `v0.5.7` references and the bounded post-release reconciliation node.
3. Bind every gate-critical contract to one producer, its consumers, fixtures,
   compatibility window, and authority boundary.
4. Execute the current credential-free clean-room platform-team journey and
   measure every stage.
5. Produce a prioritized adoption-friction inventory with source owners.
6. Define metrics, evidence privacy, pilot eligibility, consent, and stops.

Exit only when current-state records agree, the reference journey has a
replayable baseline or exact blocker, and every Month 2 node has one owner,
acceptance criteria, and bounded authority.

### Month 2: Installable Team Path

Window: September 1-30, 2026.

1. Establish one canonical installation and repository-bootstrap path for the
   supported platform matrix.
2. Rehearse upgrade, rollback, recovery, doctor, and support-bundle export from
   clean environments.
3. Make CI and pull-request integration reproducible from a public fixture.
4. Align Mission, Command, AO2, and Control Plane terminology and next actions.
5. Remove onboarding defects that expose unnecessary repository ordering.

Use controlled public fixtures for deterministic positive and negative paths.
Use a real public bug only as an additional non-destructive validation after
the fixture contract passes and separate issue-mutation authority exists.

Exit when the credential-free journey finishes in 30 minutes or less on the
supported native hosts, or an exact bounded exception and owner are recorded.
Install, upgrade, rollback, doctor, CI, and support paths need executable
regressions. Pilot intake may be prepared, but no team may be contacted.

### Month 3: First Controlled Pilots

Window: October 1-31, 2026.

After separate approval, onboard one or two eligible teams, run one governed
workflow per team in a repository they control, capture only approved minimal
metrics, and repair high-severity adoption blockers in their owning sources.

Without pilot approval, do not fabricate adoption. Complete all provider-free
preparation and return an exact approval request. Exit when at least one team
completes the journey or a precise product blocker and bounded repair campaign
are recorded.

### Month 4: Repeat Use And Operator Convergence

Window: November 1-30, 2026.

Expand to three to five approved pilots only if Month 3 is safe. Require repeat
use without live maintainer intervention. Reconcile Workbench, Control Plane,
Command, and Mission identity and status semantics without merging their
authority. Improve failure diagnosis, evidence search, support reproduction,
and interrupted-run recovery.

Exit when three teams complete a first run and two complete a repeat run, or
the evidence records truthful shortfalls and owners.

### Month 5: Production Hardening

Window: December 1-31, 2026.

Run a scoped security and trust-boundary review, establish performance and
resource baselines, exercise interruption recovery and version skew, rebuild
Control Plane from retained evidence, and reconcile Arena, Crucible, Sentinel,
and Promoter readbacks against the same canonical journey.

Exit with no unresolved critical finding. Every high-severity deferral needs an
owner, containment, rationale, and dated follow-up. No promotion or release
side effect is authorized.

### Month 6: Adoption Proof And Release Decision

Window: January 1-31, 2027.

Re-run the clean-host journey and approved pilot workflows against final merged
heads. Reconcile adoption, support, security, performance, recovery,
compatibility, and assurance evidence. Update current public documentation and
produce package dispositions plus one of:

- `READY_FOR_SEPARATE_RELEASE_AUTHORIZATION`; or
- `NO_RELEASE_RECOMMENDED`.

The program may recommend a separately governed release. It must not create a
tag, release, upload, deployment, or publication.

### Monthly Checkpoint Contract

At the start of each month:

1. Verify the previous terminal index and artifact manifest independently.
2. Inventory exact repository heads and unresolved operator decisions.
3. Produce or validate a fresh Blueprint pack and build authorization bound to
   the monthly scope and current source inventory.
4. Import that authorization into Atlas and create a new workgraph identity;
   never resume an old monthly wave.
5. Bind the lease, retry policy, timeouts, and measured duration estimate.
6. Select one dependency-ready node and keep all other mutation nodes inactive.

After each real node:

1. Record implementation, tests, CI, source heads, and artifact digests.
2. Import the source-owner and Atlas/Foundry readbacks into Mission.
3. Run one Mission continuation cycle and write a checkpoint.
4. Reconcile Mission status, inspect, checkpoint, event index, and Command
   readbacks. Surface-specific `state_digest` values may differ; their canonical
   identity, payload, status, counts, authority, and exact next action must
   agree.
5. Compact or replay only through the documented procedure and verify the
   result before deleting superseded runtime material.

At month end, create a canonical terminal index, independently verified
manifest, source-head inventory, metric report, blocker disposition, cleanup
readback, and exact next-month action. Monthly closure does not complete the
six-month Mission.

### AO Mission Self-Change Protocol

When a node changes AO Mission:

1. Keep `AO_MISSION_HOME` and campaign evidence outside the source checkout.
2. Keep the active supervisor binary pinned to its recorded clean source SHA.
3. Create an isolated AO Mission worktree for the source change.
4. Run focused and full gates plus a bounded canary in the worktree.
5. Merge through a reviewed, green pull request.
6. Build a new supervisor binary from synchronized merged `main`.
7. Record the old and new supervisor SHAs and binary digests.
8. Resume the same Mission record with the new binary.
9. Verify checkpoint, objective, route, artifact, and exact-next-action
   continuity before executing another node.

Never move the durable Mission store into the task worktree and never create a
replacement Mission solely because the supervisor source changed.

### Blocker Policy

A true blocker is an ungranted explicit authority, unavailable required
infrastructure, irreconcilable source or artifact identity, unsafe external
data boundary, or the same root failure after three bounded repair attempts
with no evidence-backed alternative.

Failing tests, stale docs, malformed fixtures, missing workflows, digest drift
with intact source artifacts, and readiness-ledger contradictions are repair
nodes, not terminal blockers. Never edit only an evidence summary to turn a
failure into success.

If blocked, report the exact month, workgraph, node, repository, source SHA,
command or workflow, run ID when applicable, artifact state, attempted repairs,
and smallest operator action.

### Program Completion Gate

Complete the Mission only when:

1. all six monthly exit gates have terminal indexes and verified manifests;
2. active-repository lifecycle, release, compatibility, and ownership truth
   agree across Architecture and source owners;
3. the clean supported journey is reproducible from public documentation;
4. install, doctor, upgrade, rollback, recovery, CI, and support paths pass;
5. gate-critical producer-consumer contracts have positive and negative tests;
6. Mission, Atlas, Command, AO2, Control Plane, and assurance readbacks agree
   on canonical identities and authority;
7. no unresolved critical security or data-integrity finding remains;
8. pilot claims are supported by approved evidence rather than fixtures;
9. every repository is clean and synchronized and all task branches and
   worktrees are reconciled;
10. the final manifest independently rehashes; and
11. the final result is either `READY_FOR_SEPARATE_RELEASE_AUTHORIZATION` or
    `NO_RELEASE_RECOMMENDED`.

Completion does not authorize publication.

### Exact Initial Actions

1. Create the fresh Mission record and record its objective digest, supervisor
   source SHA, binary SHA-256, workspace root, and external state root.
2. Run the provider-free 14-repository inventory and compare it with the
   Architecture registry.
3. Compile and validate a fresh Month 1 Blueprint pack and bounded build
   authorization from this approved objective, the inventory, and current
   authority constraints.
4. Import the Blueprint authorization into Atlas and create a fresh Month 1
   workgraph identity with 8-12 bounded nodes.
5. Validate the AO2 `v0.5.7` reconciliation handoff and make it the first
   dependency-safe candidate node if still current.
6. Import the first Atlas readback, reconcile all Mission views, and preserve
   one exact next action.

The first execution readback must include the Mission ID, objective digest,
supervisor SHA and binary digest, Blueprint pack and authorization identities
and digests, Month 1 workgraph identity, current route, authority statement,
first node, exact next action, lease, state root, and
`final_response_allowed=false`.
