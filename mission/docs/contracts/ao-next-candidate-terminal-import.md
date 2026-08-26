# AO Next Candidate Terminal Import

`ao-mission import ao-next-terminal --mission <id> --path <json>` consumes one `ao.next.live-run-record.v1` N7 record as untrusted candidate evidence.

The importer:

- reads one bounded regular non-symlink file;
- rejects duplicate keys, unknown top-level or measurement fields, unsupported schemas or variants, malformed digests, multiple workers, dynamic fan-out, hidden-test exposure, unauthorized effects, and a passing terminal without complete valid evidence;
- retains the exact original bytes beneath the Mission content-addressed artifact store;
- makes exact digest reimport idempotent across locator changes and rejects any contradictory replacement; and
- exposes `ao.mission.ao-next-candidate-projection.v1` beneath Mission evidence and Command status.

The projection is read-only. Import does not change durable Mission status, route, phase, blockers, or exact next action. It does not schedule or execute work, approve an effect, fabricate an AO2 or canonical terminal-index artifact, publish, promote, release, deploy, migrate, or grant provider or credential authority.

AO Next remains the producer and schema owner. Mission validates only the fields needed to preserve candidate identity, evidence completeness, and authority denial; it retains all accepted producer bytes without reinterpreting AO Next policy, recovery, verification, or terminal semantics.

## Execution Journal Prefix Import

`ao-next-mission --home <mission-root> import ao-next-journal-prefix --mission <id> --path <prefix.json>` consumes one immutable `ao.next.execution-journal-prefix.v1` file. The temporary `ao-mission` compatibility command accepts the same arguments.

Mission requires a clean absolute locator outside its state root. Unix reads stay descriptor-relative. Windows keeps every validated ancestor handle open without delete sharing until the leaf read completes, rejects reparse points, and compares final handle paths so case, canonical, and 8.3 aliases cannot bypass state-root exclusion. Mission accepts only a bounded regular file and retains the accepted locator unchanged in the artifact reference. It stores the exact input bytes under `artifacts/sha256/<digest>` after strict JSON, lifecycle, terminal, and digest validation.

The import adds `ao.mission.ao-next-journal-projection.v1` to Mission evidence and Command status. The status is one of `prepared`, `provider_intent_recorded`, `provider_outcome_unknown`, `provider_captured`, `effect_outcome_unknown`, `effects_pending`, `verifying`, `passed`, `failed`, or `stopped`. Denied and interrupted terminal records both project to `stopped`.

Exact-byte reimport is idempotent and keeps the first accepted locator. Mission holds the cross-process Mission lock across the same-run conflict check and artifact retention. A concurrent changed digest fails before object creation, so the rejected caller leaves no orphan. The record-only transaction changes only the artifact reference and journal projection. It preserves `updated_at_utc`, checkpoint bytes, event-decision bytes, durable Mission status, route, phase, blockers, next action, workgraph, and the separate AO Next candidate projection.

The journal projection is read-only. Mission does not scan Engine state, resume recovery, execute effects, call providers, approve work, mutate a repository, or grant publication, release, deployment, migration, adoption, or credential authority.
