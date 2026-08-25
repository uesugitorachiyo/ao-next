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
