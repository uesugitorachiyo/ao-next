# Canonical Terminal Index Consumer

AO Mission consumes `ao.canonical-terminal-index.v1` as a read-only
reconciliation artifact. It independently verifies the index digest, schema
digest, every source-artifact digest, mission identity, ordered lineage,
regular-file and path containment rules, counts, lease classification, conflict
codes, safety boundaries, readiness, return gate, and exact next action.

The importer accepts at most 128 artifacts, 1 MiB per artifact, and 16 MiB in
total. It rejects malformed JSON, duplicate keys, symlinks, traversal,
out-of-root paths, identity drift, digest changes, non-monotonic lineage,
semantic contradictions, and any execution, approval, mutation, provider,
publication, release, deployment, or authority-advance flag.

Imports are durable and idempotent by index digest. A different second digest is
rejected before the state file changes. Inspect, checkpoint, event-index, and
Command-compatible views all read the same persisted reconciliation record.
The import never schedules work or changes an AO Mission record.

The generic `status`, `mission inspect`, `mission dashboard`, and `command
status` views accept an optional `--terminal-state` path. They validate the
persisted import state, exact Mission identity, freshness against the Mission
record, terminal counts, return gate, and denied safety boundaries before
projecting terminal truth. The projection is read-only: it does not rewrite the
Mission record or imported Atlas workgraph. Without `--terminal-state`, all
four commands retain their existing behavior.

Those generic projections expose the durable `source_record_status`, canonical
`terminal_projection_status`, `terminal_projection_read_only=true`, and the
resulting `effective_operator_status`. A terminal projection may therefore make
the effective operator status `done` while the stored source record remains
`active`, without implying execution, approval, mutation, or authority.

Terminal-index inspect, checkpoint, event-index, and command-readback surfaces
carry the terminal projection status and read-only flag directly. Legacy import
states remain readable: Mission verifies their original state digest before
deriving the new projection fields in memory and never rewrites the retained
state file.

`index_digest` binds the canonical Atlas terminal-index payload. It must be
identical across Mission's `inspect`, `checkpoint`, `event-index`, and
`command-readback` views. The authority-relevant canonical payload must also
agree field-for-field across those views: identities, counts, lease,
completion, timing, conflict codes, readiness, return gate, safety boundaries,
and exact next action. Equal `index_digest` values never excuse a canonical
payload mismatch.

`state_digest` binds the normalized persisted state plus the selected readback
surface. The four valid values are therefore expected to be distinct. Distinct
surface-specific state digests are not a conflict, and consumers must not assert
that all four are equal. Each view must verify its own state digest, then verify
canonical payload agreement and the shared index digest.

```sh
ao-mission terminal-index import \
  --root /path/to/evidence \
  --index /path/to/evidence/canonical-terminal-index.json \
  --state /path/to/read-only-import-state.json

ao-mission terminal-index inspect --state /path/to/read-only-import-state.json
ao-mission terminal-index checkpoint --state /path/to/read-only-import-state.json
ao-mission terminal-index event-index --state /path/to/read-only-import-state.json
ao-mission terminal-index command-readback --state /path/to/read-only-import-state.json

ao-mission status --mission <id> --terminal-state /path/to/read-only-import-state.json --json
ao-mission mission inspect --mission <id> --terminal-state /path/to/read-only-import-state.json --json
ao-mission mission dashboard --mission <id> --terminal-state /path/to/read-only-import-state.json --json
ao-mission command status --mission <id> --terminal-state /path/to/read-only-import-state.json --json
```

Historical Mission evidence can be indexed additively with `terminal-index
historical`. Missing terminal evidence remains `no_canonical_terminal`; it is
never converted into a live objective or automatic continuation request.
