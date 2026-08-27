# AO Next Dual-Process Stage 1-5 Governed Handoff

- Status: approved governed handoff
- Date: 2026-08-24
- Durable lifecycle owner: AO Mission
- Engine and repository implementation owner: AO Next
- Comparison and rollback baseline: AO2
- Final adoption authority: AO Architecture
- Execution client: Codex
- Default controller model: `gpt-5.6-sol`
- Default controller effort: `high`
- Publication authority: not granted
- Release or deployment authority: not granted

This is the canonical executable handoff for Stages 1 through 5 of the
[AO Next Dual-Process Cross-Platform Successor Design](https://github.com/uesugitorachiyo/ao-next/blob/fbfa158681a2542767cc4fbd01368325dfc27286/docs/superpowers/specs/2026-08-23-ao-next-dual-process-cross-platform-successor-design.md).
It supersedes no historical handoff and does not revive the completed
[Windows successor-slice handoff](ao-next-integration-six-month-handoff-prompt.md).

AO Mission owns the cross-stage roadmap, durable stage state, continuation,
read-only imports, and final reconciliation. AO Next owns implementation plans
and source changes inside the AO Next repository. Codex executes the plans and
must never become the only durable record.

## Goal

Move the reviewed AO Next Engine from the Stage-0 closure through:

1. Mission source migration;
2. coordinated two-binary packaging rehearsal;
3. separately authorized real cross-platform qualification;
4. AO2 shadow comparison; and
5. an independent AO Architecture adoption decision.

Every stage has an exact entry gate, tasks, evidence contract, and terminal
result. Continue automatically from one green stage into the next. Stop at the
first failed or missing gate and preserve its exact terminal state.

## Activation Baseline

Revalidate these heads before creating the Stage-1 Mission:

| Repository | Required baseline |
| --- | --- |
| AO Mission | `2b0cb22c3cf05a3d4f8bd035d5d8780ae6567909` |
| AO Next reviewed source | `a3bdad30244834e6019a8fcde7adfada34bb4dc6` |
| AO Next merged `main` | `fbfa158681a2542767cc4fbd01368325dfc27286` |
| AO2 | `68cf6914ae51cb4b638a7441ac05c1b4e86ec6d6` |
| AO Architecture | `a0a5c03850483abad608b3133afc158393ff7ef2` |

Require the retained Stage-0 closure:

- result: `ENGINE_RECOVERY_READY_FOR_MISSION_MIGRATION`;
- PR: `https://github.com/uesugitorachiyo/ao-next/pull/11`;
- hosted Linux, macOS, and Windows: passed;
- physical Windows NTFS: passed;
- setup fake-provider process count: `1`;
- recovery provider process count: `0`;
- live provider calls: `0`;
- post-merge source: `fbfa158681a2542767cc4fbd01368325dfc27286`.

If a head advanced, requalify ancestry, relevant contracts, and closure
evidence. Do not silently pin an older checkout. Preserve unrelated user
branches through isolated worktrees.

Before Stage 1, inspect active AO Mission state. Start this roadmap only when
the current active Mission is terminal, explicitly paused with a recorded
disposition, or explicitly superseded. Otherwise record
`ACTIVE_MISSION_CONFLICT` and stop without creating a competing Mission.

## Continuous Execution Rule

After activation, do not ask conversational questions such as "continue?",
"which approach?", or "may I push?" for work this handoff already places in
scope. Continue through:

- read-only inspection and synchronization;
- provider-free designs and implementation plans;
- isolated source changes and tests;
- reviewed branches and pull requests;
- hosted CI and provider-free physical qualification;
- packaging rehearsals that publish nothing;
- exact evidence readback, merge, cleanup, and stage reconciliation.

Use one mutation task at a time. For each implementation task, dispatch one
fresh implementer and one fresh reviewer with the explicit model and effort in
this handoff. Do not inherit defaults silently. Do not run parallel mutation
agents. Record rulings for plan ambiguities and continue; a ruling must include
the cost if wrong.

Do not ask for authority that is already granted here. Do not invent authority
that is not granted here. If a real journey reaches a prepared receipt and its
exact operator-authored execution-authority file is absent, write the complete
authority-request artifact, record `AUTHORITY_REQUIRED_<PLATFORM>`, and wait
for that external artifact. Do not send a conversational permission question,
self-author the file, reuse another platform's authority, or start a provider.

## Model And Effort Contract

Every dispatch must set both model and reasoning effort explicitly.

| Work class | Implementer | Reviewer |
| --- | --- | --- |
| Ordinary controller and integration coordination | `gpt-5.6-sol`, `high` | not applicable |
| Architecture, authority, recovery, migration semantics, security, adoption | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |
| Multi-file implementation and qualification | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| Stable mechanical docs, fixtures, CI, archive naming | `gpt-5.6-terra`, `medium` | `gpt-5.6-sol`, `high` |
| Real N7 provider process | `gpt-5.6-sol`, `high`, one turn | outside-process review: `gpt-5.6-sol`, `xhigh` |

If a required profile is unavailable, record `MODEL_PROFILE_UNAVAILABLE` and
stop that stage. Do not substitute another model or effort.

## Stage 1: Mission Source Migration

### Entry gate

- Stage-0 closure verified at the baseline above.
- No competing active Mission.
- AO Mission and AO Next implementation worktrees are clean and isolated.
- Existing Go Mission source history and canonical source repository are
  identified without rewriting history.

### Tasks and profiles

| ID | Task | Implementer | Reviewer |
| --- | --- | --- | --- |
| S1.1 | Activate one Stage-1 Mission, bind heads and closure digest, create workgraph | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| S1.2 | Freeze old Mission commands, contracts, fixtures, readbacks, and equivalence corpus | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| S1.3 | Write and commit the AO Next history-preserving migration design and plan | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |
| S1.4 | Import the existing Go Mission source with history into `mission/` | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| S1.5 | Build `ao-next-mission` and a temporary compatibility command | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| S1.6 | Add strict Engine journal-prefix export and checked-in contract vectors | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |
| S1.7 | Add Mission read-only import and pure status projection | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |
| S1.8 | Run old/new equivalence, three-platform provider-free gates, final review, PR, merge, cleanup | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |

### Required behavior

- Preserve Go Mission behavior before adding AO Next integration.
- Keep Engine and Mission separate processes, state roots, and failure domains.
- Mission imports only an operator-selected immutable bundle. It never scans
  Engine state or executes an effect.
- Exact reimport is idempotent. Changed bytes, sequence gaps, unknown fields,
  unsafe paths, digest drift, or terminal contradictions fail before Mission
  state changes.
- Keep durable Mission source status separate from projected Engine status.

### Terminal result

- `MISSION_SOURCE_MIGRATION_READY_FOR_PACKAGING` when old/new readbacks match
  the frozen corpus at one reviewed merged head on all required platforms.
- `STOP_MISSION_SOURCE_MIGRATION` for any unresolved behavior, history,
  contract, import, projection, platform, review, or merge failure.

Proceed to Stage 2 only after independently verifying the successful result.

## Stage 2: Coordinated Candidate Packaging Rehearsal

This stage builds and tests candidate artifacts but publishes nothing.

### Tasks and profiles

| ID | Task | Implementer | Reviewer |
| --- | --- | --- | --- |
| S2.1 | Define package, compatibility-manifest, install, upgrade, rollback, and removal contracts | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |
| S2.2 | Build reproducible AO Next Engine archives for three platforms | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| S2.3 | Build reproducible AO Next Mission archives for three platforms | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| S2.4 | Add mechanical archive naming, matrix CI, and clean-install fixtures | `gpt-5.6-terra`, `medium` | `gpt-5.6-sol`, `high` |
| S2.5 | Generate and verify checksums, SBOMs, provenance, and compatibility manifest | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| S2.6 | Rehearse install, inspect, upgrade, rollback, and removal on clean Windows, macOS, Ubuntu | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| S2.7 | Independent manifest/evidence review, PR, merge, cleanup, reconciliation | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |

Store candidates in run-owned private directories. Do not create a tag,
release, upload, package registry entry, deployment, or public download.

### Terminal result

- `DUAL_PROCESS_PACKAGING_READY_FOR_REAL_QUALIFICATION` when clean-machine
  rehearsal and independent manifest verification pass at one merged head.
- `STOP_DUAL_PROCESS_PACKAGING` for any reproducibility, compatibility,
  install, rollback, removal, manifest, platform, review, or merge failure.

## Stage 3: Real Cross-Platform Qualification

Stage 3 is the first provider-backed stage. This handoff does not itself grant
a provider process.

### Tasks and profiles

| ID | Task | Implementer/runtime | Reviewer |
| --- | --- | --- | --- |
| S3.1 | Prepare exact Windows, macOS, and Ubuntu run receipts and authority-request packets without providers | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| S3.2 | Execute the exact Windows journey after its authority file exists | runtime `gpt-5.6-sol`, `high`, one turn, one worker, one provider process | `gpt-5.6-sol`, `xhigh` |
| S3.3 | Execute the exact macOS journey after its authority file exists | runtime `gpt-5.6-sol`, `high`, one turn, one worker, one provider process | `gpt-5.6-sol`, `xhigh` |
| S3.4 | Execute the exact Ubuntu journey after its authority file exists | runtime `gpt-5.6-sol`, `high`, one turn, one worker, one provider process | `gpt-5.6-sol`, `xhigh` |
| S3.5 | Interrupt and recover one retained capture per platform without another provider | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |
| S3.6 | Import each exact journal prefix into Mission and compare every readback | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| S3.7 | Verify cross-platform manifests, provider counts, effects, terminal evidence, PR/merge state | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |

Each platform authority file must bind the prepared receipt digest, target,
base commit, allowed paths/programs, verifier, token and cost ceiling, one
provider process, one worker, expiry, rollback, and all denied external
effects. No platform authority substitutes for another.

### Terminal result

- `REAL_CROSS_PLATFORM_QUALIFICATION_READY_FOR_SHADOW_COMPARISON` only when all
  three platform journeys, retained recovery, Mission imports, manifests, and
  independent reviews pass with zero duplicate effects.
- `AUTHORITY_REQUIRED_WINDOWS`, `AUTHORITY_REQUIRED_MACOS`, or
  `AUTHORITY_REQUIRED_UBUNTU` when the exact prepared platform lacks its
  operator-authored file. Preserve the receipt and wait without asking.
- `STOP_REAL_CROSS_PLATFORM_QUALIFICATION` for any other unresolved failure.

## Stage 4: AO2 Shadow Comparison

Use frozen bounded tasks and disposable targets. Never apply competing
candidates to the same mutated target and never switch execution systems after
mutation begins.

### Tasks and profiles

| ID | Task | Implementer/runtime | Reviewer |
| --- | --- | --- | --- |
| S4.1 | Freeze comparison protocol, tasks, metrics, targets, and rollback | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |
| S4.2 | Run AO2 comparison tasks under exact existing AO2 authority | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| S4.3 | Run direct Codex comparison tasks under exact task authority | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| S4.4 | Run AO Next comparison tasks under exact prepared authority | runtime `gpt-5.6-sol`, `high`, one turn per task | `gpt-5.6-sol`, `xhigh` |
| S4.5 | Analyze code size, time, provider use, cost, interventions, recovery, translation debt | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| S4.6 | Assign every AO2 responsibility to successor, retained owner, blocker, or non-goal | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |
| S4.7 | Write reviewed adoption proposal, merge evidence, reconcile Mission | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |

AO2 remains installed, supported, and available as rollback throughout this
stage.

### Terminal result

- `AO_NEXT_ADOPTION_PROPOSAL_READY_FOR_ARCHITECTURE_DECISION` when comparison
  evidence and responsibility assignment are complete and independently
  verified.
- `AUTHORITY_REQUIRED_AO2_COMPARISON`,
  `AUTHORITY_REQUIRED_DIRECT_CODEX_COMPARISON`, or
  `AUTHORITY_REQUIRED_AO_NEXT_COMPARISON` when a frozen comparison task lacks
  its exact operator-authored authority. Preserve the task packet and wait
  without asking.
- `STOP_AO_NEXT_SHADOW_COMPARISON` for any unresolved fairness, authority,
  evidence, cost, recovery, compatibility, or review failure.

## Stage 5: Independent Adoption Decision

AO Architecture owns this decision. AO Next and AO Mission may assemble and
verify the packet but may not decide their own adoption.

### Tasks and profiles

| ID | Task | Implementer | Reviewer |
| --- | --- | --- | --- |
| S5.1 | Assemble exact heads, stage closures, comparison evidence, responsibility matrix, rollback | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |
| S5.2 | Write the AO Architecture lifecycle decision proposal and reviewed PR | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |
| S5.3 | Verify hosted checks, merge decision, synchronize repositories | `gpt-5.6-sol`, `high` | `gpt-5.6-sol`, `xhigh` |
| S5.4 | Import the decision into Mission and record final cross-stage reconciliation | `gpt-5.6-sol`, `xhigh` | `gpt-5.6-sol`, `xhigh` |

AO Architecture records exactly one decision:

- `ADVANCE_AO_NEXT_DUAL_PROCESS_SUCCESSOR`;
- `RETAIN_AO2_WITH_AO_NEXT_EXPERIMENTAL`; or
- `STOP_AO_NEXT_SUCCESSOR`.

No decision automatically releases software, changes production routing,
retires AO2, archives repositories, publishes a plugin, or begins production
migration.

## Authority Boundaries

This handoff authorizes continuous provider-free repository work, isolated
branches/worktrees, tests, reviewed pull requests, hosted CI, private packaging
rehearsals, provider-free physical qualification, evidence retention, merge,
cleanup, and Mission reconciliation within the stage contracts above.

It does not authorize:

- any provider process without a separately operator-authored exact authority
  file produced after the prepared receipt;
- credentials, private account data, account or permission changes;
- releases, tags, uploads, publication, deployment, production routing;
- AO2 retirement, repository archival, compatibility removal;
- plugin or MCP publication;
- self-approval, authority generation by a model, or direct pushes to `main`;
- destructive handling of unknown or preserved user work.

A missing provider or comparison authority produces the defined durable
authority-required result. It does not produce a conversational permission
request and does not weaken the gate.

## Per-Task Execution Contract

For every task:

1. Read the owning instructions, contracts, callers, tests, current heads, and
   prior-stage evidence.
2. Record owner, inputs, exact model/effort, authority exclusions, write scope,
   rollback, verification, and evidence root before mutation.
3. Use test-driven development for behavior changes.
4. Keep one mutation task active. Dispatch a fresh independent reviewer after
   each task.
5. Fix every Critical or Important review finding and re-review the fix.
6. Run focused, repository, cross-repository contract, native-platform, hosted
   CI, and independent evidence checks required by the changed surface.
7. Merge only through a reviewed green pull request. Synchronize `main`, run
   post-merge gates, remove only run-owned branches/worktrees, and retain
   evidence.
8. Import the task result into the same Mission, checkpoint, and reconcile
   Mission and Command-compatible views.

Ordinary test, documentation, fixture, CI, and wiring failures are repair work.
Do not stop to ask. A stage stops only for a defined authority-required state,
unsafe or irreconcilable identity/data boundary, unavailable mandatory
infrastructure or model profile, destructive uncertainty, or a plan-defined
terminal failure after bounded repairs.

## First Readback

The first execution response must include:

- active-roadmap disposition and Mission identity;
- new Stage-1 Mission ID and objective digest, or `not created` with the exact
  activation blocker;
- verified Stage-0 closure digest and AO Next merge commit;
- AO Mission, AO Next, AO2, and AO Architecture heads;
- requested and resolved controller model/effort;
- external durable state and evidence roots;
- Stage-1 workgraph identity and first dependency-ready task;
- current authority statement, rollback, and exact next action;
- `final_response_allowed=false`.

Do not claim activation if the entry checks fail. Do not claim a stage complete
from a plan, readback, green partial matrix, or unmerged branch. The final
response must state the exact Stage-5 decision and repeat every authority that
remains denied.
