# Mission Source Migration Contract

- Status: Stage 1 contract; implementation is incomplete until the S1.8 exit gate
- Engine implementation: Rust workspace at the repository root
- Mission implementation: Go module under `mission/`
- Canonical Mission source: `uesugitorachiyo/ao-mission` at
  `05567fdd7c3fc64814ca4122b3f431d4ed9aaded`
- Frozen behavior corpus: [`../tests/fixtures/mission-migration/corpus-v1.json`](../tests/fixtures/mission-migration/corpus-v1.json)
- Corpus schema: `ao.next.mission-equivalence-corpus.v1`
- Corpus semantic digest:
  `sha256:c82ae75a836c8a0c94686087a98dc2c5b7a525c59afcfa52fcbb7a2b1a3ed428`

This contract refines the approved
[dual-process successor design](superpowers/specs/2026-08-23-ao-next-dual-process-cross-platform-successor-design.md)
for the Mission source migration. It does not claim that the migration,
packaging, cross-platform qualification, release, deployment, or adoption has
completed.

## Product Boundary

AO Next remains two products in one source repository:

| Product | Implementation | Responsibility |
| --- | --- | --- |
| Engine | Rust | Validate execution authority, run one worker, admit effects, verify outcomes, retain evidence, recover, and publish terminal records |
| Mission | Go | Retain objective and lifecycle state, import an operator-selected Engine prefix, and expose a read-only projection |

The products run as separate native processes. They do not share memory,
databases, locks, private state roots, provider credentials, or failure domains.
Repository co-location, one source commit, or a compatible package set grants
no execution, provider, effect, approval, release, deployment, publication, or
adoption authority.

The current `ao-next` command remains the Engine compatibility name during
Stage 1. Stage 2 packaging must produce the separate `ao-next-engine` binary.
Stage 1 adds `ao-next-mission` from the imported Go module and retains
`ao-mission` as a temporary compatibility command for old/new qualification.

## History Import

The import uses core Git commands and one two-parent merge commit:

```sh
AO_MISSION_REPOSITORY=https://github.com/uesugitorachiyo/ao-mission.git
git remote add ao-mission-source "$AO_MISSION_REPOSITORY"
git fetch --no-tags ao-mission-source 05567fdd7c3fc64814ca4122b3f431d4ed9aaded
git merge -s ours --no-commit --allow-unrelated-histories \
  05567fdd7c3fc64814ca4122b3f431d4ed9aaded
git read-tree --prefix=mission/ -u \
  05567fdd7c3fc64814ca4122b3f431d4ed9aaded
git commit -m "feat: import AO Mission source history"
```

Preconditions are a clean isolated AO Next task branch, the exact reviewed AO
Next parent selected by the Stage 1 plan, a clean canonical Mission checkout at
the commit above, no existing `mission/` entry, and a verified S1.2 corpus.
Direct mutation of `main` is excluded.

The merge commit must have the selected AO Next head as its first parent and
the canonical Mission commit as its second parent. Its `mission` tree object
must equal the canonical Mission root tree object:

```sh
AO_MISSION_CHECKOUT=/absolute/path/to/clean/ao-mission
git show -s --format=%P HEAD
git merge-base --is-ancestor \
  05567fdd7c3fc64814ca4122b3f431d4ed9aaded HEAD
test "$(git rev-parse HEAD:mission)" = \
  "$(git -C "$AO_MISSION_CHECKOUT" rev-parse 'HEAD^{tree}')"
```

This preserves every canonical Mission commit ID and parent edge. Git history
before the merge remains at its original paths; the merge places the exact
source tree under `mission/` without rewriting that history.

If the import fails review after it has been shared, rollback is a normal
first-parent revert:

```sh
git revert -m 1 "$IMPORT_COMMIT"
```

`IMPORT_COMMIT` is the exact retained two-parent import merge. The revert
removes the imported tree from the candidate line without deleting
or rewriting the retained Mission ancestry. An unshared failed attempt may be
abandoned with its isolated worktree and branch after evidence retention.

### Alternatives

| Approach | Decision and cost |
| --- | --- |
| Core Git merge plus `read-tree --prefix` | Selected. It uses installed core Git, creates one auditable merge, and preserves canonical ancestry and tree bytes. The repository gains unrelated-history ancestry and future upstream syncs need the same explicit subtree discipline. |
| Unsquashed `git subtree add` | Rejected. It can preserve ancestry, but it depends on the contributed `git subtree` command and encourages an unrequested ongoing subtree-sync workflow. |
| `git filter-repo --to-subdirectory-filter` | Rejected. It adds a tool and rewrites every Mission commit ID and path history. |
| Archive, copy, or squashed subtree | Rejected. Each loses canonical ancestry and weakens old/new source provenance. |
| Git submodule | Rejected. It keeps source in a separately resolved checkout and complicates coherent package and offline source verification. |

## Process And State Separation

Operators provide different private state roots to each process:

| Platform | Engine root | Mission root |
| --- | --- | --- |
| Windows | `%LOCALAPPDATA%\AO Next\engine` | `%LOCALAPPDATA%\AO Next\mission` |
| macOS | `${XDG_STATE_HOME:-$HOME/Library/Application Support}/ao-next/engine` | `${XDG_STATE_HOME:-$HOME/Library/Application Support}/ao-next/mission` |
| Ubuntu | `${XDG_STATE_HOME:-$HOME/.local/state}/ao-next/engine` | `${XDG_STATE_HOME:-$HOME/.local/state}/ao-next/mission` |

Stage 1 qualification passes the Mission root through its existing `--home`
option and the Engine root through its existing run-root inputs. The exchange
file lives in an operator-selected root outside both state roots. Mission never
discovers or opens Engine private state. Engine never discovers or opens
Mission private state.

An Engine crash cannot change Mission lifecycle state. A Mission crash cannot
admit an Engine effect or change an Engine journal. Each process revalidates
its own untrusted inputs after restart.

## Journal-Prefix Exchange

Engine exports one canonical JSON file with schema
`ao.next.execution-journal-prefix.v1`. The operator explicitly selects the
existing request-bound journal and a new output file. Export is read-only with
respect to the journal and uses create-new output semantics.

The v1 document contains these fields:

| Field | Type and rule |
| --- | --- |
| `schema_version` | Literal `ao.next.execution-journal-prefix.v1` |
| `run_id` | Non-empty Engine run identity |
| `request_digest` | Lowercase `sha256:` digest of the exact run request |
| `journal_identity` | Exact request, source, workspace, policy, model, and verifier digests from the bound journal |
| `worker_count` | Integer literal `1` |
| `dynamic_fanout` | Boolean literal `false` |
| `first_sequence` | Integer literal `0` |
| `last_sequence` | Last event sequence, or `null` for an empty prepared prefix |
| `preceding_prefix_digest` | Literal `null` in v1; incremental prefix replacement requires a later contract version |
| `events_digest` | Digest of canonical ordered `events` bytes |
| `events` | Complete ordered prefix of strict `ao.next.journal-event.v1` objects |
| `terminal_digest` | Digest recorded by `terminal_published`, or `null` |
| `terminal_record` | Strict retained `ao.next.live-run-record.v1` JSON matching `terminal_digest`, or `null` |
| `safe_to_execute` | Boolean literal `false` |
| `executes_work` | Boolean literal `false` |
| `approves_work` | Boolean literal `false` |
| `mutates_repositories` | Boolean literal `false` |
| `grants_provider_access` | Boolean literal `false` |
| `publishes_artifacts` | Boolean literal `false` |
| `releases` | Boolean literal `false` |
| `deploys` | Boolean literal `false` |
| `advances_authority` | Boolean literal `false` |
| `prefix_digest` | Semantic digest of every preceding field in the listed order |

The prefix contains the events and terminal record required for independent
projection validation. It is a private exchange artifact and must remain
outside tracked source. It does not contain raw provider captures, credentials,
or account identifiers.

Engine validates the exact request binding, lifecycle sequence, file type,
size bounds, event digest, terminal byte digest, and all denied authority flags
before export. Mission then validates the strict terminal record and its
semantics before projection or retention. The two sides reject duplicate keys,
unknown fields, unsafe paths, symlinks, non-regular files, identity drift,
digest drift, sequence gaps, and terminal contradictions.

Mission imports only a caller-supplied regular non-symlink prefix file:

```sh
MISSION_STATE_ROOT=/absolute/path/to/mission-state
MISSION_ID=mission-0123456789abcdef
PREFIX_JSON=/absolute/path/to/operator-selected-prefix.json
ao-next-mission --home "$MISSION_STATE_ROOT" import ao-next-journal-prefix \
  --mission "$MISSION_ID" \
  --path "$PREFIX_JSON"
```

`MISSION_STATE_ROOT` and `PREFIX_JSON` must be clean absolute local paths.
`PREFIX_JSON` must be outside the Mission state root. Relative, drive-relative,
empty, `.` or `..` component, NUL-containing, Windows UNC, and Windows device
namespace locators are rejected. Every existing ancestor and the prefix leaf is
opened without following links; Unix symlinks and Windows reparse points at any
component are rejected. Ancestors must be directories and the leaf must be a
bounded regular file. Spaces and non-ASCII components are accepted. Engine
applies the same component rules to `--journal-root`, `--request`, and `--out`;
the new output must be outside the journal root, its parent must already be a
safe directory, and the leaf must not exist.

Mission retains the exact accepted bytes in its content-addressed artifact
store and preserves the supplied locator as provenance. Exact digest reimport
is idempotent. The retained provenance is the exact accepted absolute locator,
not a rewritten relative path. Mission calculates the candidate digest and
checks the existing run projection before retaining bytes. A different prefix
for the same accepted run is rejected before artifact retention or Mission
state change, leaving no orphan content-addressed object. A later
incremental-prefix contract requires a separate version and is outside Stage 1.

## Pure Projection

Mission validates the whole document before calling the projection function.
The function receives an already decoded prefix value, returns one status, and
performs no filesystem, network, provider, clock, environment, process, or
Mission-store operation.

| Verified prefix state | Projected status |
| --- | --- |
| No journal event | `prepared` |
| `provider_request_intent` is the last event | `provider_intent_recorded` |
| Provider process started without retained output | `provider_outcome_unknown` |
| Provider output retained or normalized, with no effect intent | `provider_captured` |
| At least one effect intent lacks completion | `effect_outcome_unknown` |
| All observed effects completed and verification has not started | `effects_pending` |
| Verification started or a verifier record exists without terminal publication | `verifying` |
| Valid terminal record state `passed` | `passed` |
| Valid terminal record state `failed` | `failed` |
| Valid terminal record state `denied` or `interrupted` | `stopped` |

Mission exposes the projection separately from the durable source record on
Mission inspect and Command-compatible status readbacks. Import never changes
the Mission status, route, phase, blockers, exact next action, or workgraph.
The model, projection, or import readback cannot approve Engine work or advance
authority.

## Compatibility And Validation

The imported source must first pass its existing Go tests, build, vet,
public-safety checks, and production-readiness gate without behavior changes.
`ao-next-mission` and the temporary `ao-mission` command then replay all seven
S1.2 operations in isolated state roots:

- Command-compatible status;
- archive validation and import round trip;
- pause and resume lifecycle;
- accepted and rejected strict contracts;
- accepted public-safety input; and
- rejected public-safety symlink input.

The runner compares stable structured fields, exact exit dispositions, expected
error text, and resulting state. Generated Mission IDs, timestamps, temporary
locators, and their derived digests are bound within each run but normalized
only in the final cross-binary comparison record.

AO Mission currently accepts `contract_version` as an input discriminator,
while a later generic-schema path can require a literal `schema` field. The
imported repository must add one regression test and one discriminator resolver
that accepts `schema`, `schema_version`, or `contract_version`, rejects
conflicting values, and routes the AO Next prefix to its strict intrinsic
validator. This compatibility support must pass before the S1.8 generic
contract-validation gate.

Schema generation and checked-in JSON Schema bytes must match. Positive vectors
cover an empty prepared prefix, a retained provider outcome, and a passed
terminal. Constructed valid prefixes exercise all ten projections; denied and
interrupted terminals each map to `stopped`. Negative vectors and filesystem
tests independently cover duplicate keys, incorrect field casing, trailing
JSON, wrong field types, unknown fields, oversized input, relative or
state-contained locators, symlink or reparse components, non-regular files,
sequence gaps, schema drift, digest drift, request or journal identity drift,
and terminal contradictions. Deterministic replay must produce the same prefix
digest and Mission projection from the same bytes.

## Per-Task Durable Progression

S1.4 through S1.8 each close only after independent review. The controller
retains a typed task result, implementation report, verification manifest, and
review report beneath that task's private evidence node. AO Atlas creates an
evidence-bound `ao.atlas.run-link.v0.1` from those exact files and advances the
latest workgraph with `workgraph complete`, which marks only the matching task
complete. `workgraph next` must return the immediate successor before Mission
imports the run link and updated workgraph.

AO Mission imports the typed `foundry-run-link` and `atlas-workgraph`, creates a
checkpoint, and reconciles Mission and Command views after every reviewed task.
S1.4 makes only S1.5 dependency-ready; S1.5 makes only S1.6 dependency-ready;
S1.6 makes only S1.7 dependency-ready; and S1.7 makes only S1.8
dependency-ready. S1.8 consumes the S1.7-completed workgraph and marks only
S1.8 complete. It never retroactively completes S1.3 through S1.7.

## Stage 1 Exit And Ownership

S1.8 runs provider-free equivalence on Windows x86_64, macOS arm64, and Ubuntu
x86_64 from one exact candidate head. It also runs the existing Stage 0 Rust
recovery gates, the imported Go gates, schema drift, deterministic replay,
public-safety, independent evidence verification, and a broad final review.

Stage 1 reaches `MISSION_SOURCE_MIGRATION_READY_FOR_PACKAGING` only after a
reviewed pull request is green, merged, verified again at the merged head, and
reconciled by AO Mission. Any unresolved history, behavior, import, projection,
platform, review, merge, or reconciliation failure reaches
`STOP_MISSION_SOURCE_MIGRATION`. A local plan, imported tree, green single-host
run, or unmerged branch is not the exit result.

The canonical AO Mission repository remains the source owner through the old/new
equivalence gate. After the reviewed Stage 1 merge and reconciliation, AO Next
owns the imported `mission/` source for Stage 2 candidate packaging. The
canonical repository remains a comparison and rollback source until an
independent later lifecycle decision assigns another role.

Stage 2 packaging acceptance includes read-only
`ao-next-engine capabilities --json` and
`ao-next-mission capabilities --json`. Those commands report versions,
contracts, supported platforms, limits, denied capabilities, projection
versions, and compatible binary ranges without credentials or private paths.
The skills-only plugin remains deferred until after Stage 3. MCP remains
deferred until measured use shows that the stable CLI cannot supply the needed
structured interface.

No part of Stage 1 authorizes a provider call, credential use, release, tag,
upload, publication, deployment, production routing, direct push to `main`,
AO2 retirement, plugin implementation, MCP implementation, or Mission rewrite
in Rust.
