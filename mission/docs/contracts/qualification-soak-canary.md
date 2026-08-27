# Qualification soak canary contract

`ao-mission qualification soak-canary` is a bounded operational consumer for
one fixed ten-node local qualification canary. It is separate from
`qualification soak-plan`: the planner remains read-only and reports planning
eligibility only. Execution also requires an independently digest-bound
`ao.mission.soak-canary-authority.v1` record.

The command consumes strict, bounded JSON for the plan input, authority,
command catalog, and activation manifest. It rejects unknown fields, duplicate
keys, trailing JSON, oversized files, symlinks, special files, changed
digests, and paths outside the authority-bound evidence root. Validation
rebuilds the planner readback and recomputes every authority, catalog, and
activation digest before the executor is reachable.

```sh
ao-mission qualification soak-canary \
  --plan /path/to/plan.json \
  --authority /path/to/authority.json \
  --catalog /path/to/catalog.json \
  --activation /path/to/activation.json \
  --checkpoint /path/to/evidence/checkpoints/checkpoint.json \
  --evidence-root /path/to/evidence \
  --repository-root /path/to/ao-mission \
  --validate-only \
  --json
```

Remove `--validate-only` only for an explicitly authorized canary. Execution
requires an unmodified running binary whose embedded Go build information
contains the exact `vcs.revision` bound by the plan, catalog, authority, and
activation manifest. No Git process or other repository-verifier child is
launched. An in-process Git reader supports normal `.git` directories and
gitfiles, requires current `HEAD` to equal the approved revision, and rejects
staged, tracked-worktree, or untracked changes during validation and
immediately before and after each approved Go launch. The verifier uses only
the Go standard library. `scripts/soak-canary-offline-stdlib-test.sh` compiles
and exercises it with an empty module cache and all module lookup disabled.

Preactivation also creates a deterministic typed repository snapshot. The
pure-Go walker includes regular files, directories, and untracked content,
binds symlinks by link text without following them, rejects special files and
bounded-limit violations, and excludes `.git`. The activation binds both the
source-provenance digest and the complete snapshot digest. Mission recomputes
the snapshot during validation and immediately before and after every approved
Go launch. A changed, missing, malformed, or mismatched snapshot stops the run
and is recorded in the attempt checkpoint.

## Fixed execution boundary

The command catalog contains exactly one approved scale test and nine approved
regular tests. Every command uses an absolute regular `go` executable whose
bytes match its SHA-256, repository-relative working directory `.`, package
`./internal/mission`, race mode, an exact anchored test or subtest expression,
`-json`, a millisecond timeout, and the approved effective repeat count. The
executable digest is rechecked immediately before and after every launch.

Commands are passed to `exec.CommandContext` as argv. No shell is involved.
The manifest cannot select another executable, package, test, working
directory, environment variable, or network mode. The environment always
binds `GOTOOLCHAIN=local`, `GOPROXY=off`, `GOSUMDB=off`, and `GOVCS=*:off`.
`HOME`, `TMPDIR`, `GOCACHE`, and `GOTMPDIR` are deterministic campaign-owned
directories beneath a non-symlink evidence root. Host `HOME`, `TMPDIR`,
`GOCACHE`, `GOMODCACHE`, `GOENV`, `CGO_ENABLED`, and `PATH` values are not
inherited.

The scale test runs with repeat one and cannot retry. Regular tests run with
repeat three. One authority-bound regular node records a single
`transient_infrastructure` attempt before process creation and may then launch
its unchanged command once. This yields eleven attempt records and ten child
process launches for ten completed nodes.

## Checkpoint and evidence

Before `Start`, Mission persists a signed launch reservation. A successful
start adds a signed running checkpoint. While `Wait` blocks, Mission adds a
signed heartbeat checkpoint every five minutes without changing the original
phase start; tests may inject a shorter interval. Completion adds a signed
terminal attempt checkpoint. Restart never relaunches a reserved or running
attempt whose launch truth is indeterminate. The only restart progression is
from the designated retry node's exact pre-spawn
`transient_infrastructure` attempt one to attempt two. Every other failed or
incomplete regular attempt is terminal, and scale remains nonretryable.
If `Start` succeeds but the running checkpoint cannot be persisted, Mission
cancels and synchronously reaps the child, records the observed result in its
failed summary, attempts a completed checkpoint, and leaves the last durable
reservation fail-closed when persistence remains unavailable.

Every attempt binds the original phase start, source head, source provenance,
repository snapshots before and after execution, plan and policy digests,
execution profile, command catalog, authority, activation manifest, argv,
node, partition, test, repeat count, and safety boundaries. Atomic checkpoints
preserve a signed reservation/running/heartbeat/completion event chain,
completed-node set, controlled retry consumption, and scale reservation
consumption. Semantic validation reconstructs every attempt from the
activation and catalog, including retry identity and scale dimension, rather
than trusting re-signed checkpoint fields.

Stdout and stderr are bounded before persistence and recorded with relative
paths, byte counts, truncation state, and SHA-256. Reconciliation reopens each
artifact through the bounded regular-file reader, reparses the digest-matching
Go JSON stdout, and requires its exact event counts to equal both the attempt
record and approved repeat. Exact matching pass events must equal one for the
scale test and three for each regular test. Actual duration must remain within
the planned estimate, attempt timeout, total-node timeout, node budget,
aggregate allowance, lease, and 45-minute authority wall. The child context is
bounded by the smallest remaining attempt, node, retry, lease, and hard-wall
allowance. Child elapsed time and total attempt elapsed time are recorded
separately; total attempt time is finalized only after output persistence,
post-run snapshot verification, and executable revalidation. A post-child
failure is checkpointed as a completed truthful attempt.

## Terminal truth

The operational summary alone reports
`local_test_execution_performed=true` and the child-process launch count.
Planner and terminal-reader surfaces remain read-only and non-executing.
Mission's `inspect`, `checkpoint`, `event-index`, and `command-readback`
surfaces share one canonical payload and `index_digest`; each surface has its
own valid `state_digest`. Distinct surface digests are expected and do not
indicate disagreement.

Terminal artifacts bind the authority, activation, catalog, source provenance,
repository snapshot, final checkpoint, attempt, launch, retry, pass, duration,
and local-execution truth. They preserve the exact lease minimum, target, and
maximum. The final next action is
`Bounded canary complete; no further execution is authorized.` Mission writes
a nonterminal provisional summary first and promotes it to `run-summary.json`
only after the canonical terminal bundle imports successfully and all four
surface readbacks agree. The public CLI emits a completed JSON or text summary
only after that promotion; persistence failure emits only signed nonterminal
truth.

The checked-in activation examples demonstrate a valid self-digest and a
digest-mismatch rejection. The invalid validation matrix records the bounded
representative rejection cases and their exact conflict codes or transport
errors. These are contract examples, not reusable execution authority. A real
activation must be regenerated for the exact repository head, executable,
evidence root, handoff digest, phase start, plan, and command catalog.
