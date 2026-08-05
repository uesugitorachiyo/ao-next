# AO Mission Compatibility

AO Mission's current `ao.canonical-terminal-index.v1` consumer cannot safely import one `ao.next.terminal-readback.v1` record. The Mission consumer independently validates lease, root, and optional terminal artifacts, recomputes counts and lease constraints, and checks their ordered lineage. AO Next's single-run readback does not contain those artifacts. Filling the missing fields would manufacture evidence.

The bounded proposal is a separate read-only `ao.mission.candidate-terminal-readback.v1` importer. It would retain the exact candidate bytes at their digest, preserve the original locator, reject digest drift and terminal contradictions, and expose the record only for operator reconciliation. Import would not approve work, execute Mission work, grant authority, publish, promote the candidate, or reinterpret the record as a canonical terminal index.

The candidate implements and tests that import rule as an in-memory compatibility model. It does not modify AO Mission. The exact mapping fixture is `tests/fixtures/mission/compatibility-report-v1.json`.
