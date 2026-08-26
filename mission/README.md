# AO Mission

[![Latest release](https://img.shields.io/github/v/release/uesugitorachiyo/ao-mission?label=latest%20release)](https://github.com/uesugitorachiyo/ao-mission/releases/tag/v0.1.6)

AO Mission is the operator entry point for starting, tracking, routing, and
resuming AO work. It stores durable mission state, records route and checkpoint
history, accepts intent from local CLI and gateway adapters, and imports
readbacks from downstream components. Use it when work must continue across
multiple AO stages or recover after interruption.

## How it fits in AO

- **Primary responsibility:** Mission lifecycle, routing, continuation, and recovery.
- **Inputs:** Operator objectives, gateway intents, scheduler events, and downstream readbacks.
- **Outputs:** Mission records, route decisions, checkpoints, archives, dashboards, and next actions.
- **Upstream:** Operators, Telegram, A2A, and scheduler adapters.
- **Downstream:** AO Blueprint, AO Atlas, AO Foundry, and AO Command.

See the
[AO Architecture guide](https://github.com/uesugitorachiyo/ao-architecture)
and the
[AO Mission component page](https://github.com/uesugitorachiyo/ao-architecture/blob/main/components/ao-mission.md)
for the cross-repository flow.

## AO Next Stage 1 command

`ao-next-mission` is the Stage 1 candidate command in the AO Next repository.
It accepts the same arguments as Mission, including `--home <dir>`, and calls
the same Mission entry point. `ao-mission` remains available here only as the
temporary compatibility command used to verify equivalent behavior.

Run the candidate and compatibility commands with separate state roots. The
Go Mission and Rust Engine remain separate processes, state roots, and failure
domains. Repository co-location grants neither process authority to inspect or
mutate the other's private state, execute providers or effects, publish,
release, deploy, promote AO Next, or retire AO2.

## Install v0.1.6

Prebuilt archives are available for Linux x86_64, macOS aarch64, and Windows
x86_64. The release has no separate checksum file. Verify the downloaded
archive against the GitHub asset digest before extracting it.

On macOS (Apple silicon):

```sh
release=v0.1.6
archive=ao-mission-0.1.6-macos-aarch64.tar.gz
curl -fL -o "$archive" "https://github.com/uesugitorachiyo/ao-mission/releases/download/$release/$archive"
expected="$(gh release view v0.1.6 --repo uesugitorachiyo/ao-mission --json assets --jq '.assets[] | select(.name == "ao-mission-0.1.6-macos-aarch64.tar.gz") | .digest')"
actual="sha256:$(shasum -a 256 "$archive" | awk '{print $1}')"
test "$actual" = "$expected"
tar -xzf "$archive"
mkdir -p "$HOME/.local/bin"
install -m 0755 ao-mission "$HOME/.local/bin/ao-mission"
export PATH="$HOME/.local/bin:$PATH"
```

On Linux x86_64:

```sh
release=v0.1.6
archive=ao-mission-0.1.6-linux-x86_64.tar.gz
curl -fL -o "$archive" "https://github.com/uesugitorachiyo/ao-mission/releases/download/$release/$archive"
expected="$(gh release view v0.1.6 --repo uesugitorachiyo/ao-mission --json assets --jq '.assets[] | select(.name == "ao-mission-0.1.6-linux-x86_64.tar.gz") | .digest')"
actual="sha256:$(sha256sum "$archive" | awk '{print $1}')"
test "$actual" = "$expected"
tar -xzf "$archive"
mkdir -p "$HOME/.local/bin"
install -m 0755 ao-mission "$HOME/.local/bin/ao-mission"
export PATH="$HOME/.local/bin:$PATH"
```

On PowerShell:

```powershell
$release = 'v0.1.6'
$archive = 'ao-mission-0.1.6-windows-x86_64.zip'
Invoke-WebRequest "https://github.com/uesugitorachiyo/ao-mission/releases/download/$release/$archive" -OutFile $archive
$expected = gh release view $release --repo uesugitorachiyo/ao-mission --json assets --jq '.assets[] | select(.name == "ao-mission-0.1.6-windows-x86_64.zip") | .digest'
$actual = "sha256:$((Get-FileHash $archive -Algorithm SHA256).Hash.ToLower())"
if ($actual -ne $expected) { throw "release digest mismatch" }
Expand-Archive $archive -DestinationPath .\ao-mission-0.1.6
$env:Path = "$PWD\ao-mission-0.1.6;$env:Path"
```

Then initialize and inspect a private, isolated state directory:

```sh
tmp_home="$(mktemp -d)"
AO_MISSION_HOME="$tmp_home/state" ao-mission init
AO_MISSION_HOME="$tmp_home/state" ao-mission doctor --json
```

On PowerShell:

```powershell
$missionHome = Join-Path $env:TEMP ('ao-mission-onboarding-' + [guid]::NewGuid().ToString('N'))
$env:AO_MISSION_HOME = $missionHome
ao-mission.exe init
ao-mission.exe doctor --json
```

To build from source instead, install Go 1.26.4 or later, then run
`go install github.com/uesugitorachiyo/ao-mission/cmd/ao-mission@v0.1.6`.

## Commands

```sh
ao-mission init
ao-mission start "<objective>"
ao-mission objective start --objective "<objective>" [--correlation-id <id>]
ao-mission issue-repair supervise --mission <id> --request <request.json> [--json]
ao-mission issue-repair product-gate --root <sealed-root> --manifest <manifest.json> --json
ao-mission mission list [--status <status>] [--route <route>] [--json]
ao-mission mission inspect --mission <id> [--terminal-state <state.json>] [--json]
ao-mission mission history --mission <id> [--json]
ao-mission mission events index [--out <event-index.json>]
ao-mission mission events search [--mission <id>] [--kind <kind>] [--query <text>] [--index <event-index.json>] [--json]
ao-mission mission readiness-bundle --repo <repo>=<summary-path> [--repo <repo>=<summary-path>] [--out <bundle.json>] [--json]
ao-mission mission dashboard --mission <id> [--terminal-state <state.json>] [--compact] [--out <dashboard.json>] [--json]
ao-mission mission verification-bundle --mission <id> [--readiness-bundle <bundle.json>] [--gateway-replay-bundle <bundle.json>] [--out <verification-bundle.json>] [--json]
ao-mission mission compact --mission <id> [--keep-route-history N] [--keep-steps N] [--timeline]
ao-mission continue --mission <id> [--until-done] [--max-iterations N] [--min-nodes N] [--min-minutes N] [--max-minutes N] [--return-only-when <gate>] [--checkpoint-policy <policy>]
ao-mission checkpoint create --mission <id> [--json]
ao-mission checkpoint create --mission <id> --slice S01 --evidence-digest sha256:<digest> [--json]
ao-mission checkpoint inspect --mission <id> [--json]
ao-mission status --mission <id> [--terminal-state <state.json>] [--json]
ao-mission command status --mission <id> [--terminal-state <state.json>] [--json]
ao-mission next --mission <id> [--json]
ao-mission pause --mission <id>
ao-mission resume --mission <id>
ao-mission stop --mission <id>
ao-mission doctor [--json]
ao-mission schedule --mission <id> --every <duration> --event-loop
ao-mission schedule replay --fixture <scheduler-readback-replay.json>
ao-mission schedule alerts --fixture <scheduler-readback-replay.json>
ao-mission schedule recover --mission <id> --fixture <scheduler-readback-replay.json>
ao-mission qualification orchestrate --fixture examples/valid/stack-qualification-orchestration.json
ao-mission qualification soak-plan --fixture examples/valid/soak-plan-mixed.json --json
ao-mission qualification soak-canary --plan <plan.json> --authority <authority.json> --catalog <catalog.json> --activation <activation.json> --checkpoint <checkpoint.json> --evidence-root <dir> --repository-root <ao-mission> --validate-only --json
ao-mission daemon install|status|uninstall
ao-mission telegram serve
ao-mission telegram replay --matrix <matrix.json> --config <telegram-config.json>
ao-mission telegram replay-updates --fixture <telegram-update-replay.json> --config <telegram-config.json>
ao-mission telegram webhook-replay --fixture <telegram-webhook-replay.json> --config <telegram-config.json>
ao-mission telegram role-matrix --config <telegram-config.json> --out <telegram-role-matrix.json>
ao-mission a2a serve [--http] [--once]
ao-mission a2a replay --fixture <a2a-http-integration.json>
ao-mission a2a lifecycle --fixture <a2a-task-lifecycle.json>
ao-mission a2a compatibility --agent-card <a2a-agent-card.json> --http <a2a-http-integration.json> --lifecycle <a2a-task-lifecycle.json> --out <a2a-compatibility.json>
ao-mission a2a streaming-denial --agent-card <a2a-agent-card.json> [--out <a2a-streaming-denial.json>]
ao-mission a2a cancellation-replay --lifecycle <a2a-task-lifecycle.json> [--out <a2a-cancellation-replay.json>]
ao-mission gateway ledger --mission <id> --telegram-updates <fixture> --telegram-config <config> --a2a-http <fixture> --out <ledger.json>
ao-mission gateway replay-bundle --telegram-config <config> --telegram-matrix <matrix> --telegram-webhook <fixture> --telegram-updates <fixture> --a2a-http <fixture> --a2a-lifecycle <fixture> --scheduler <fixture> [--out <bundle.json>] [--json]
ao-mission gateway replay-suite --telegram-config <config> --telegram-webhook <fixture> --telegram-updates <fixture> --a2a-http <fixture> --a2a-lifecycle <fixture> --out <suite.json>
ao-mission gateway readiness-rollup --mission <mission-id> --suite <suite.json> --a2a-compatibility <compatibility.json> --archive-validation <archive-validation.json> --snapshot-diff <snapshot-diff.json> --correlation-id <id> --out <rollup.json>
ao-mission governance snapshot --mission <id>
ao-mission governance diff --before <snapshot.json> --after <snapshot.json>
ao-mission mission archive --mission <id> --out <mission-archive.json>
ao-mission mission validate-archive --path <mission-archive.json> [--out <archive-validation.json>]
ao-mission mission import-archive --path <mission-archive.json>
ao-mission artifacts --mission <id>
ao-mission artifacts manifest --mission <id> [--out <manifest.json>]
ao-mission artifacts validate-manifest --path <manifest.json>
ao-mission correlation build --mission <id> --artifact <role>=<path> [--artifact <role>=<path>] --out <chain.json>
ao-mission correlation validate --path <chain.json>
ao-mission command status --mission <id> [--json]
ao-mission validate contract --path <json>
ao-mission import correlation-evidence --mission <id> --path <json> --correlation-chain <chain.json> --correlation-role <exact-chain-role>
ao-mission import blueprint-authorization --mission <id> --path <json> [--correlation-chain <chain.json>]
ao-mission import atlas-workgraph --mission <id> --path <json>
ao-mission import ao-next-terminal --mission <id> --path <json>
ao-next-mission --home <mission-root> import ao-next-journal-prefix --mission <id> --path <prefix.json>
ao-mission import atlas-recommendation-readback --mission <id> --path <json>
ao-mission import foundry-run-link --mission <id> --path <json>
ao-mission import foundry-final-rollup --mission <id> --path <json>
ao-mission import scheduler-readback --mission <id> --path <json>
ao-mission import scheduler-recovery-readback --mission <id> --path <json>
ao-mission import ledger-compaction-readback --mission <id> --path <json>
ao-mission final rollup --mission <id> [--evidence-root <dir>]
ao-mission final atlas-prompt --mission <id> --event-index <event-index.json> [--evidence-root <dir>] --out <prompt.json>
ao-mission final synthesize --mission <id> --evidence-root <dir>
ao-mission final reconcile --mission <id> [--correlation-chain <chain.json>]
```

`issue-repair product-gate` validates one fresh, digest-bound Python repair qualification record and derives separate technical, governed-qualification, and release decisions. It is read-only: it does not execute a repair, approve work, mutate a repository, or grant release authority. See [the Python repair product-gate contract](docs/contracts/python-repair-product-gate.md).

Compaction readbacks preserve `correlation_id` when the Mission record is
correlated, so the producer output can be imported unchanged. Uncorrelated
legacy records continue to omit that optional field.

By default state is stored under `.ao-mission/`. Use `AO_MISSION_HOME` to choose another state root.
Every command also accepts `--home <dir>` before the command name for explicit local state routing.

The AO Next journal-prefix import accepts one immutable `ao.next.execution-journal-prefix.v1` at a clean absolute locator outside the Mission state root. Mission retains the exact bytes and original locator, then exposes a separate read-only `ao_next_journal_projection` without changing durable Mission lifecycle state or the existing AO Next terminal candidate projection. Exact-byte reimport is idempotent; a changed digest for the same run fails before retention. See [AO Next candidate and journal-prefix imports](docs/contracts/ao-next-candidate-terminal-import.md).

Pass `--evidence-root <dir>` to recommendation-bearing final commands. When it
is omitted, Mission emits `<evidence-root>` rather than selecting a
repository-local evidence directory.

Evidence-bound slice checkpoints accept only ordered `S01` through `S07` and
an exact retained passing-evidence digest. Exact replay is idempotent; a
conflicting digest or skipped slice fails closed. This mode appends a checkpoint
without changing Mission status, route, phase, next action, or authority.

### Content-addressed evidence retention

Imported evidence is durably retained beneath the trusted Mission home at
`artifacts/sha256/<64 lowercase hex digits>`. Mission writes the validated raw
bytes before updating the mission record, deduplicates an exact digest, and
fails closed if an existing digest-addressed object does not contain the exact
same bytes. A v0.2 artifact manifest exposes that contained object as
`content_ref` and records its `sha256:` digest.

The artifact `ref` remains the original locator for provenance. It is not the
retained content location and it does not need to remain readable after the
import; v0.2 manifest validation reads and verifies the retained object instead.
The `ao.mission.artifact-ref.v0.1` identity remains compatible inside the v0.2
manifest. Historical `ao.mission.artifact-manifest.v0.1` manifests remain
supported through their original source-reference validation and do not
require `content_ref`; Mission does not reconstruct a v0.2 retained object from
a legacy source whose bytes are unavailable or have changed.

`AO_MISSION_HOME` is an operator-owned trusted root for Mission state and
retained evidence. Keep it private and do not use an untrusted or shared
writable location. Mission rejects symlinked or non-directory retained-path
components and non-regular retained objects, while hostile same-user
concurrent replacement of the root remains outside this trust boundary.

Retention, artifact manifests, validation, and import readbacks are durable
recording and reconciliation surfaces only. They do not execute work, approve
work, mutate repositories, call providers, publish, release, or advance
authority; their authority flags remain false.

### Objective workflow entry

`ao-mission objective start` is the correlation-bound entry path for a complete
objective workflow. It emits and persists the strict
`ao.mission.objective-workflow-contract.v0.1` contract. If
`--correlation-id` is omitted, Mission derives a stable identifier from the new
mission ID. The legacy `ao-mission start "<objective>"` command and its record
output remain unchanged.

The workflow contract classifies an underspecified objective as
`pending_blueprint`, a workgraph or multi-file objective as `complex`, and a
concrete small objective as `reduced`. These classes begin at AO Blueprint, AO
Atlas, and AO Foundry respectively. Every stage is listed as `required`,
`conditional`, or `omitted`, and the contract includes the exact lifecycle
commands for status, continuation, pause, resume, verification, and final
reconciliation. Contract authority flags remain false: the readback does not
itself execute, approve, or mutate repository work.

Without a correlation chain, correlation-bound missions require imported
evidence to carry the exact same `correlation_id`. Missing or mismatched
identity fails before mission state is updated. Correlation is retained in
continuation steps, checkpoints, event-loop decisions, dashboard events,
import readbacks, and the final verification bundle. Legacy records omit these
additive fields.

For digest-bound cross-component evidence, `ao-mission correlation build`
creates the strict `ao.mission.correlation-chain.v0.1` contract from
component-native identifiers and artifact SHA-256 digests. Supplying that
chain to `ao-mission import` permits native identifiers or explicit digest
links without adding a Mission-specific correlation field to every downstream
artifact. Intermediate imports may use partial chains; final correlated
reconciliation requires one complete, current chain covering every chained
import and rehashes each original imported path. Producer identity accepts
exactly one of `schema`, `schema_version`, or `contract_version`; known nested
digest fields accept raw or `sha256:`-prefixed lowercase SHA-256. See
[Correlation chains](docs/correlation-chains.md) for exact decoding,
persisted-reference integrity, file-race protections, and the operator
sequence.

## Gateway References

The messaging surface follows the same split used by Hermes-style gateways: CLI and messaging platforms are separate entry points into one mission ledger, and messaging commands create intents/readbacks instead of direct mutation. The A2A local gateway exposes an Agent Card with local protocol metadata, structured capability detail, readback-only skills, artifact refs, and task-style readbacks for local interoperability while preserving `mutation_authority=false`.

Telegram is disabled by default. A config file may name the environment variable that contains the real token and a chat allowlist, but ao-mission never prints or persists the token value.

See [Gateway Readback Runbook](docs/gateway-readback-runbook.md) for the fixture-backed command matrix, denied command examples, A2A JSON-RPC parameter checks, and intent-only authority boundary. See [Operator Next Actions](docs/operator-next-actions.md) for concrete next commands after Mission emits route readback, and [Long-Run Operator Runbook](docs/long-run-operator-runbook.md) for doubled 2-3 hour Atlas/Foundry waves.

## Local Private Pilots

Use [Local Private Pilot Workflow](docs/local-private-pilot-workflow.md) when AO Mission supervises a real local codebase where the evidence must stay private and local. The workflow covers evidence directories, provided-library boundaries, artifact guards, public API/C ABI checks, and device runtime evidence.

Reusable templates:

- [Local Private Pilot Evidence Template](docs/templates/local-private-pilot-evidence-template.md)
- [iOS/Xcode Local App Smoke Checklist](docs/templates/ios-xcode-app-smoke-checklist.md)

## Readback Surfaces

`ao-mission qualification soak-plan` validates a bounded exact-head,
execution-profile-bound plan and emits deterministic planning readback only. It
never runs or schedules tests. Scale work stays at effective repeat count one,
regular repeats remain policy-bounded, and activation eligibility fails closed
on classification, measured-history, retry, timeout, lease, digest, or authority
conflicts.
`ao-mission qualification soak-canary` is the separate source-owned consumer
for one explicitly authorized ten-node local canary. It rebuilds and binds the
read-only plan before activation, accepts only the fixed offline Go test
catalog, uses shell-free argv, records atomic digest-chained checkpoints, and
supports validation-only mode with zero child-process launches. Launch intent
is durable before process creation, indeterminate restart state fails closed,
runtime caches stay beneath the evidence root, embedded unmodified Go build
provenance, in-process Git cleanliness verification, and a pure-Go repository
snapshot replace repository-verifier subprocesses, and the final summary is
promoted only after all terminal surfaces agree. See
[Qualification soak canary contract](docs/contracts/qualification-soak-canary.md).
`ao-mission mission events index` builds a durable local `ao.mission.event-index.v0.2` over mission records, route decisions, event-loop decisions, and artifacts, with `index_digest` and `source_digest` fields so loaded indexes fail closed if tampered. `ao-mission mission events search` emits `ao.mission.event-search-readback.v0.1` without granting execution authority. `ao-mission mission readiness-bundle` binds local readiness summaries from sibling AO repos into one digest-backed readback, `ao-mission gateway replay-bundle` binds Scheduler, Telegram, and A2A replay fixtures into one local no-authority matrix, `ao-mission mission dashboard` emits a compact operator readback over mission status and recent indexed events, `ao-mission mission verification-bundle` emits a top-level digest manifest over the event index, dashboard, artifact manifest, readiness bundle, and replay bundle, and `ao-mission doctor` emits `ao.mission.doctor-readback.v0.1` with local store, event, and artifact health only.
