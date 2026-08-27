# AO Next Windows Successor-Slice Handoff

- Status: completed; Month 1 recorded `STOP_SUCCESSOR_WORK`
- Decision: [AO Next Windows Successor-Slice Month 1 Decision](roadmap/ao-next-windows-successor-slice-month1-decision.md)
- Durable lifecycle owner: AO Mission
- Slice execution owner: AO Next
- Implementation owners: AO Next and AO Mission
- Comparison and rollback baseline: AO2
- Execution client: Codex
- Default model: `gpt-5.6-sol`
- Default reasoning effort: `high`
- Publication authority: not granted

This is the canonical first-stage handoff for the conditional
[AO Next Integration Six-Month Roadmap](roadmap/ao-next-integration-six-month-roadmap.md).
Its one authorized execution ended with `STOP_SUCCESSOR_WORK`. Do not re-run or
extend it without a new operator-approved handoff.
Run it from a clean AO Mission repository inside the shared parent workspace.
AO Mission owns durable state. Codex executes bounded nodes and must not become
the only record of progress.

This handoff authorizes only the 40-60 focused-hour Windows successor slice and
its decision record. It does not authorize Months 2-6. A separate handoff may
be created only after `ADVANCE_SUCCESSOR_ARCHITECTURE` is recorded and verified.

AO Mission already has an active AO Stack production-adoption program. Do not
start this successor while that program is active unless the operator records
an explicit pause or supersession decision with both Mission identities and an
exact disposition for ready work. Never merge the two objectives implicitly.

## Codex Execution Profile

Before starting work:

1. Select `gpt-5.6-sol` with `high` reasoning effort.
2. Use `xhigh` only for architecture, authority, recovery, security, migration,
   and final adoption decisions.
3. Permit `gpt-5.6-terra` with `medium` effort only for a node already classified
   as routine documentation, fixtures, or mechanical tests after its contract
   is stable.
4. Record the requested and resolved model and effort in the first readback.
5. Fail closed if the requested profile is unavailable. Do not substitute a
   model or effort without explicit operator approval.

No automatic subagent fan-out is authorized. Keep one executable mutation node
active at a time.

## Activation Check

Before creating a Mission record:

- inspect the current active roadmap Mission and prove it is terminal, paused
  by explicit operator decision, or explicitly superseded;
- verify AO Architecture has accepted AO Next into the active repository and
  authority inventory;
- verify clean synchronized `main` heads for AO Mission, AO Next, AO2, and AO
  Architecture, using isolated worktrees around preserved user changes;
- read the current AO Next live-evaluation, recovery, runtime-adapter, Mission
  compatibility, and public-safety evidence;
- inventory current AO2 compatibility, release, cross-host, and rollback
  responsibilities; and
- record authority exclusions for providers, credentials, pilots, releases,
  publication, deployment, production routing, and AO2 retirement.

If any activation condition is absent, stop before implementation and return
the exact missing decision or artifact. Do not create a competing active
Mission.

## Feasibility Objective

Preserve this objective in one fresh Mission record:

> Build one narrow AO Next end-to-end successor slice for a real bounded Windows
> engineering journey. AO Next owns the objective envelope, Windows-native
> execution, effect policy, journal, verification, evidence, and local terminal
> result. Mission owns only durable objective identity, read-only result import,
> and operator continuation. Measure whether the slice is materially simpler
> than extending the current boundary, then stop with one authorized decision.

The slice must not copy the AO2 workflow compiler, cross-host orchestration,
release or promotion logic, broad Mission operator surfaces, or legacy behavior
that the selected journey does not require.

## Slice Deliverable

Implement and verify this exact vertical path:

```text
objective envelope
→ AO Next Windows-native execution
→ deterministic effect policy
→ durable write-ahead journal
→ mechanical verification
→ content-addressed evidence
→ local terminal result
→ thin Mission read-only import
```

Use exactly one runtime worker. Treat effect intent without a durable completion
as unknown and never automatically retry a potentially mutating effect.

Provider-free work may implement and qualify every boundary before the real
journey, but it cannot satisfy the real-journey gate. Stop and request separate
authority before any provider call, credential use, or real repository mutation.

## Time And Measurement Contract

- Budget 40-60 focused implementation hours. Record focused time by terminal
  node rather than padding calendar or elapsed time.
- Stop at 60 focused hours and evaluate the evidence. Continuing requires a new
  operator decision.
- Record implementation lines and files by owning repository, excluding tests,
  generated files, vendored code, and evidence payloads from the primary size
  comparison.
- Record retained positive and negative edge-case tests.
- Record interruption and recovery outcomes before dispatch, during an effect,
  after effect commit, during verification, before terminal publication, and
  during Mission import.
- Record evidence verification and Mission round-trip results by digest.
- Record operator interventions and semantic translation code required by
  Mission.
- Add one bounded Windows capability after the first journey and record the
  focused time and code delta required.

## Per-Node Contract

For each node:

1. Read the owning implementation, callers, tests, contracts, repository
   instructions, and latest exact-head evidence.
2. Record the node owner, accepted inputs, authority exclusions, write scope,
   dependencies, rollback, and verification commands.
3. Add the smallest check that fails before a behavior change and passes after
   it. Documentation-only nodes use link, formatting, and repository readiness
   gates instead.
4. Make one bounded change in one mutable scope.
5. Run focused verification, the owning repository's full applicable gate,
   cross-repository conformance when a contract changes, and hosted CI.
6. Record compact evidence with source SHA, command, exit status, run identity,
   artifact digest, rollback, blocker, and exact next action.
7. Merge only through a reviewed green pull request, synchronize `main`, and
   remove the isolated branch and worktree.
8. Import the result into Mission, run one continuation cycle, checkpoint, and
   reconcile Mission and Command-compatible views.

Failing tests, stale docs, missing fixtures, incomplete wiring, and ordinary CI
failures are repair work. A true blocker is an ungranted explicit authority,
unavailable required infrastructure, irreconcilable identity, unsafe data
boundary, or the same root failure after three bounded repairs with no safe
alternative.

## Authority Boundaries

This handoff may guide provider-free inspection, planning, tests, fixtures,
documentation, isolated source changes, pull requests, hosted CI, and
post-merge readback.

It does not authorize:

- provider calls, credentials, private data, external pilots, or account changes;
- direct pushes to `main` or destructive handling of preserved user work;
- permission, sandbox, policy, capability, or security-boundary weakening;
- release, tag, upload, publication, deployment, or production routing;
- AO2 retirement, repository archival, or compatibility removal;
- dynamic subagent fan-out, live self-modification, or self-approval; or
- treating Mission, Atlas, Command, Sentinel, Promoter, evaluation, or plugin
  readback as authority.

Request separate exact-scope approval when the slice reaches one of these gates.
Absence of authority is a truthful planned state, not permission to bypass it.

## Slice Decision Gate

After the journey, recovery matrix, Mission import, and additional Windows
capability are verified, record exactly one decision:

- `ADVANCE_SUCCESSOR_ARCHITECTURE` only when the slice is materially simpler,
  preserves the required safety and readback semantics, and reduces the cost of
  the additional Windows capability;
- `KEEP_AO_NEXT_AS_EXECUTION_KERNEL` when the slice works but moving surrounding
  ownership adds semantic duplication or no measured simplification; or
- `STOP_SUCCESSOR_WORK` when required recovery, evidence, authority, Windows,
  or Mission-import gates fail.

The decision record must contain exact source heads, focused hours, code-size
measurements, retained edge cases, recovery results, evidence and Mission
digests, intervention count, translation debt, next-capability cost, blockers,
and rollback. Stop after recording and verifying the decision. Do not begin
backend routing, plugin work, reliability expansion, parity work, or Months 2-6.

## First Execution Readback

The first response after activation must include:

- prior active-roadmap disposition and its Mission identity;
- new Mission ID and objective digest;
- AO Mission, AO Next, AO2, and AO Architecture source heads;
- requested and resolved Codex model and reasoning effort;
- exact external durable-state root;
- successor-slice workgraph identity and first dependency-ready node;
- current route and authority statement;
- verification and rollback plan;
- exact next action; and
- `final_response_allowed=false`.

Do not claim the slice started if the activation check did not pass. Do not
claim the slice completed without fresh terminal, manifest, measurement, and
decision verification. The final slice response must state that Months 2-6
remain unauthorized.
