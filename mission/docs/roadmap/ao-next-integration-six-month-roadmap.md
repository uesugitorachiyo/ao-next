# AO Next Integration Six-Month Roadmap

- Status: stopped after Month 1; `STOP_SUCCESSOR_WORK`
- Decision: [AO Next Windows Successor-Slice Month 1 Decision](ao-next-windows-successor-slice-month1-decision.md)
- Created: 2026-08-21
- Revised: 2026-08-23
- Durable lifecycle owner: AO Mission
- Slice execution owner: AO Next
- Implementation owners: AO Next, AO Mission, AO2, and
  `ao-mission/plugins/ao-stack`
- Trigger: start only after the active AO Stack production-adoption Mission
  closes, or after the operator records an explicit decision that pauses or
  supersedes it.

## Objective

Determine whether a narrow AO Next successor architecture is materially simpler
than extending the existing AO2/Mission execution boundary without weakening
authority, recovery, evidence, compatibility, or rollback guarantees.

Month 1 ended with `STOP_SUCCESSOR_WORK`. Months 2-6 remain unauthorized and
must not start from this roadmap.

Month 1 is a bounded 40-60 focused-hour feasibility slice. Months 2-6 remain
conditional and cannot start unless Month 1 records
`ADVANCE_SUCCESSOR_ARCHITECTURE` and the operator approves a separate bounded
handoff for the next month. AO2 remains available for release,
cross-host, legacy, and rollback work until current-head evidence proves that
another owner covers each required capability. Codex executes bounded
implementation tasks. AO Mission remains the durable program ledger and source
of continuation state.

## Activation Gate

Before Month 1 starts:

- verify the active production-adoption Mission is terminal, or record the
  operator's explicit pause or supersession decision;
- add AO Next to the current AO Architecture repository inventory and authority
  map through a reviewed lifecycle decision;
- inventory exact `main` heads, release state, CI state, and dirty worktrees for
  AO Next, AO Mission, AO2, and AO Architecture;
- confirm that the selected Codex model and reasoning effort are available;
- create one fresh AO Mission record for the feasibility slice; and
- keep provider calls, publication, release, deployment, migration, and
  repository retirement behind separate explicit authority.

This document, a readiness result, or a passing evaluation does not activate
the program by itself.

## Execution Profile

Default implementation profile:

```text
model: gpt-5.6-sol
reasoning_effort: high
```

Use `gpt-5.6-sol` with `xhigh` effort for architecture, authority, recovery,
security, migration, and final adoption decisions. After a contract is stable,
routine documentation, fixtures, and mechanical test work may use
`gpt-5.6-terra` with `medium` effort. Do not silently substitute a model or
effort. Record the requested and resolved profile in every provider-backed run.

## Standing Boundaries

- AO Mission records objectives, routes, checkpoints, imports, and terminal
  reconciliation. It does not execute repository mutations or approve work.
- AO Next owns one-worker bounded engineering execution, deterministic effect
  admission, verification, evidence, and recovery.
- AO2 remains the supported compatibility and rollback backend until a separate
  parity decision retires each responsibility.
- AO Architecture owns repository lifecycle, authority, compatibility, and
  public product claims.
- Codex is an execution client, not durable program state.
- Keep one executable mutation node active at a time. Independent read-only
  verification may run concurrently when evidence remains attributable.
- No dynamic subagent fan-out, generic workflow compiler, self-modifying
  plugin, custom plugin UI, or MCP server is required by this roadmap.
- No direct push to `main`, credential inspection, permission widening,
  release, publication, deployment, or external pilot occurs without separate
  exact-scope authority.

## Month 1: 40-60-Hour Windows Successor Slice

Goal: build one narrow end-to-end Windows engineering journey and use measured
evidence to decide whether AO Next should become the successor architecture or
remain an execution kernel.

Work:

- Give AO Next ownership of the slice's objective envelope, Windows-native
  execution, effect policy, journal, verification, evidence, and local terminal
  result.
- Limit Mission work to durable objective identity, read-only result import,
  and operator continuation. Do not copy broader Mission routing or operator
  surfaces into AO Next.
- Define one append-only, strict, versioned AO Next execution journal from
  existing run, engine, effect, verifier, and recovery identities.
- Durably record run admission before the provider request, each effect intent
  before execution, its authority decision, and its completion or unknown
  outcome.
- Record verifier results and terminal state in the same ordered journal and
  derive checkpoint, evidence, and terminal readback data from it.
- Execute one real Windows bounded engineering journey with exactly one worker.
  Provider execution requires separate exact-scope authority; provider-free
  preparation is not a substitute for the real journey.
- Import the terminal result into Mission without semantic reinterpretation or
  fabricated AO2 terminal artifacts.
- Prove interruption before dispatch, during an effect, after effect commit,
  during verification, before terminal publication, and during Mission import.
- Measure implementation size, retained edge-case tests, recovery correctness,
  evidence round-trip, operator intervention, and the focused time required to
  add one additional Windows capability.

Success criteria:

- One real Windows journey reaches a mechanically verified terminal result and
  a digest-bound Mission read-only import.
- The real N7 path uses the durable journal rather than only qualification
  probes or caller-authored recovery fixtures.
- Crash recovery cannot duplicate a committed effect.
- Journal sequence, identity, and digest drift fail closed.
- Mission consumes the result without translation logic that duplicates AO
  Next policy, recovery, verification, or terminal semantics.
- The slice records focused implementation hours and every required
  measurement at the exact merged heads.

Decision gate:

- `ADVANCE_SUCCESSOR_ARCHITECTURE`: the slice is materially simpler, preserves
  the required safety and readback semantics, and makes the next Windows
  capability cheaper to add. Months 2-6 become eligible for separate bounded
  handoffs; this decision does not authorize or start them.
- `KEEP_AO_NEXT_AS_EXECUTION_KERNEL`: the slice works, but moving surrounding
  ownership creates semantic duplication or no measured simplification.
  Retain AO Next as a bounded execution kernel and stop this roadmap.
- `STOP_SUCCESSOR_WORK`: the slice misses required recovery, evidence,
  authority, Windows, or Mission-import gates. Stop this roadmap and record the
  smallest safe follow-up.

The decision must name exact source heads, measurements, failed or retained
edge cases, translation debt, and rollback. No other verdict permits Month 2.

## Month 2: Mission Reconciliation Hardening

Entry gate: Month 1 recorded `ADVANCE_SUCCESSOR_ARCHITECTURE`.

Goal: harden and generalize the slice's thin Mission import without fabricating
AO2 terminal artifacts, duplicating AO Next semantics, or granting authority.

Work:

- Promote the slice's read-only AO Next journal-prefix import into a strict,
  reusable Mission contract.
- Retain accepted bytes in Mission's content-addressed artifact store.
- Derive a pure terminal projection with source, workspace, request, policy,
  verifier, journal-prefix, and artifact-manifest identities.
- Keep durable Mission source status distinct from projected AO Next status.
- Make exact reimport idempotent and reject changed bytes, stale identities,
  contradictory terminals, unsafe paths, duplicate keys, and oversized input.
- Expose the projection through Mission status, inspect, checkpoint, event
  index, and Command-compatible readback.

Success criteria:

- One exact AO Next journal prefix produces one reproducible Mission projection.
- Import never schedules work, approves work, changes source mission status,
  or grants publication or promotion authority.
- All Mission views agree on identity, status, safety boundaries, and exact
  next action.

## Month 3: Qualified Dual-Backend Routing

Entry gate: Month 2 closed and the successor decision remains current.

Goal: route bounded engineering work through AO Next while preserving AO2 for
capabilities AO Next does not own.

Work:

- Define a Mission backend-selection record with task eligibility, requested
  backend, resolved backend, reason, source heads, contract versions, and
  fallback policy.
- Route only qualified bounded engineering tasks to AO Next.
- Keep release, cross-host, legacy adapter, packaging, publication, and
  unsupported recovery workflows on AO2.
- Require explicit operator approval before switching an accepted task to a
  different backend. Never silently fall back after mutation begins.
- Run shadow comparisons on identical source, objective, workspace seed,
  verifier, and limits without applying both candidates to the same target.
- Prove AO2 rollback after an AO Next pre-dispatch failure and after a verified
  non-mutating interruption.

Success criteria:

- Every route explains why AO Next or AO2 owns the task.
- No task is executed twice and no fallback hides a partial mutation.
- Three real bounded engineering journeys pass with current-head evidence.

## Month 4: Codex Plugin Alpha

Entry gate: Month 3 closed with qualified routing and rollback evidence.

Goal: provide one small Codex workflow for Mission-supervised bounded
engineering tasks.

Work:

- Add one machine-readable `ao capabilities` surface covering installed
  versions, supported backends, schema versions, limits, and authority gates.
- Package one skills-only `ao-stack` plugin under
  `ao-mission/plugins/ao-stack` with a focused
  `run-bounded-engineering-task` skill.
- Have the skill inspect capabilities, preflight authority, select an already
  qualified backend, run one task, inspect evidence, and reconcile with
  Mission.
- Use the current `.codex-plugin/plugin.json` format and a repository-local
  marketplace for testing.
- Keep evaluation-corpus construction, N0/N4/N7 qualification, releases, and
  low-level AO2 controls outside the user-facing plugin.

Success criteria:

- A clean Codex session can complete one supported task from a concise user
  request and return the Mission ID, backend, run ID, terminal state, evidence
  digest, and exact next action.
- Unsupported requests stop with a clear limitation instead of approximating
  an unsafe workflow.
- The alpha adds no MCP server, custom UI, copied global prompts, or archived
  AO Operator dependency.

## Month 5: Reliability And Operator Qualification

Entry gate: Month 4 closed with a usable skills-only alpha.

Goal: prove the integrated path survives normal operator and platform failure.

Work:

- Exercise cancellation, restart, resume, journal truncation, torn writes,
  stale locks, disk exhaustion, verifier failure, provider failure, and result
  import interruption.
- Rehearse clean installation, upgrade, rollback, and removal on supported
  native hosts.
- Verify evidence independently from a clean process and a restored Mission
  store.
- Measure completion time, provider usage, intervention count, duplicate-effect
  count, recovery result, and evidence size for the three Month 3 journeys.
- Run an internal pilot only if the operator separately authorizes provider,
  repository, mutation, cost, and data scope.

Success criteria:

- No unresolved critical recovery, authority, or data-integrity finding.
- Every high-severity deferral has an owner, containment, rollback, and dated
  follow-up.
- Clean-machine and restored-state readbacks agree with source-owner evidence.

## Month 6: Parity And Adoption Decision

Entry gate: Month 5 closed without a critical unresolved safety or data-integrity
finding.

Goal: decide the next backend and plugin scope from current-head evidence.

Work:

- Re-run comparable AO2, direct Codex, and AO Next journeys against exact final
  heads and identical verifier conditions.
- Inventory every AO2 responsibility and assign an implemented owner, retained
  owner, blocker, or explicit non-goal.
- Review plugin activation quality, unsupported-request behavior, and operator
  friction.
- Add a thin local MCP server only if the skills-only alpha has a measured need
  for controlled structured tools or authentication.
- Produce one of:
  - `EXPAND_AO_NEXT_WITH_AO2_FALLBACK`;
  - `RETAIN_DUAL_BACKENDS`;
  - `AO_NEXT_NOT_READY_FOR_ADOPTION`; or
  - `READY_FOR_SEPARATE_AO2_RETIREMENT_PLANNING`.

Success criteria:

- The decision names exact evidence, remaining AO2 owners, rollback path,
  unsupported capabilities, and plugin disposition.
- No decision retires AO2, publishes a plugin, releases software, or changes
  production routing without separate authorization.

## Monthly Operating Contract

After a later month's entry gate passes, the operator must approve a separate
bounded handoff for that month. The entry gate alone grants no execution or
mutation authority. Once that handoff is approved:

1. Verify the preceding terminal index and artifact manifest.
2. Inventory exact source heads and unresolved operator decisions.
3. Create one fresh bounded workgraph with owners, dependencies, acceptance
   criteria, verification commands, rollback, and authority class.
4. Select one dependency-ready mutation node.

After each node:

1. Record implementation, tests, hosted CI, source heads, and artifact digests.
2. Import the source-owner result into Mission.
3. Run one Mission continuation cycle and checkpoint.
4. Reconcile Mission and Command-compatible views.
5. Merge only through a reviewed green pull request, synchronize `main`, and
   clean the isolated task branch and worktree.

At month end, record the terminal index, independently verified manifest,
source-head inventory, measurements, blockers, cleanup, and one exact next
action. A monthly closure does not complete the six-month Mission.

## Program Completion

Month 1 may close the roadmap with `KEEP_AO_NEXT_AS_EXECUTION_KERNEL` or
`STOP_SUCCESSOR_WORK`; those are valid evidence-backed outcomes, not incomplete
programs. If Month 1 records `ADVANCE_SUCCESSOR_ARCHITECTURE`, complete the
program only when all six monthly terminal indexes and manifests verify, every
started node is terminal, source repositories are reconciled, and Month 6
records one allowed decision. Completion does not authorize a provider call,
pilot, release, publication, deployment, production route change, plugin
publication, or AO2 retirement.
