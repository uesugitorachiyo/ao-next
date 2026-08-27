# Correlation chains

AO Mission owns the strict `ao.mission.correlation-chain.v0.1` contract. A
chain binds one Mission record's `mission_id` and objective `correlation_id` to
the exact bytes imported from downstream AO components.

Build and validate a chain before importing its artifact:

```sh
ao-mission correlation build \
  --mission <mission-id> \
  --artifact blueprint-authorization=<authorization.json> \
  --artifact atlas-workgraph=<workgraph.json> \
  --out <correlation-chain.json>

ao-mission correlation validate --path <correlation-chain.json>

ao-mission import blueprint-authorization \
  --mission <mission-id> \
  --path <authorization.json> \
  --correlation-chain <correlation-chain.json>
```

Use the neutral import when the artifact is correlation evidence but does not
implement a legacy semantic import contract:

```sh
ao-mission import correlation-evidence \
  --mission <mission-id> \
  --path <evidence.json> \
  --correlation-chain <correlation-chain.json> \
  --correlation-role <exact-chain-role>
```

Both chain and role are required. The role must resolve to the imported
artifact's exact digest. Each role can be imported once, and multiple distinct
roles may be recorded. This path appends only the artifact ref, chain reference,
and correlated-import binding; it does not change Mission status, route, phase,
steps, route history, blockers, next action, approval state, or authority.
Legacy semantic imports continue to require a chain role exactly equal to their
import kind.

`--artifact` may be repeated. Build output is sorted by role and is
deterministic for identical artifact bytes and paths. Each JSON artifact must
identify its producer with exactly one of `schema`, `schema_version`, or
`contract_version`; those contract labels are producer metadata and are never
instance provenance. Correlation requires `correlation_id`, `mission_id`, a
real component-specific `id` / `*_id`, or `action_digest`. Exact field
locations use RFC 6901 JSON Pointers such as `/provenance/request_id` or
`/approval/action_digest`, including `~0` and `~1` escaping for literal key
characters. All eligible top-level and nested identifiers are considered
together; ambiguous native identifiers and schema-only artifacts are
rejected.

An artifact can instead contain the SHA-256 digest of another supplied
artifact under `sha256`, `*_sha256`, or `*_digest`. Lowercase raw 64-hex and
`sha256:<64-hex>` values are accepted and normalized to the prefixed form.
Mission records the exact JSON Pointer in `parent_digest_field`; multiple
possible parent links are rejected.

Validation uses exact JSON object decoding for the chain and artifacts,
rejecting duplicate keys at every depth, case-variant contract fields,
unexpected chain fields, null required values, and trailing JSON. It
recomputes every artifact digest and producer, resolves the exact recorded
native or parent field, and rejects missing, duplicate, oversized, symlinked,
or non-regular files. Artifact reads are limited to 16 MiB and use
no-follow/nonblocking or identity-safe handles with pathname identity and size
checks before and after reading. Chain output is created exclusively and must
not already exist, so a symlink, hard link, or replacement race is never
followed or truncated. All chain and entry authority fields must remain false.
Known chain, reference, and persisted Mission correlation contracts are
enforced intrinsically by the executable, so validation does not depend on the
current working directory or runtime access to this repository's schema files.
Persisted decoding also rejects missing or null required false-authority fields,
unknown fields, duplicate keys, and case variants. Generic contract,
objective-workflow, archive, governance, and gateway decoding also reject
duplicate keys recursively, so last-value-wins authority fields cannot be
certified as safe.

Incremental chains can contain only the evidence available at an intermediate
import. AO Mission persists a digest-bound semantic reference for every
validated chain. The strict
`ao.mission.correlation-chain-reference.v0.1` contract includes the full chain
digest, Mission identity, entries, exact provenance paths, false-authority
boundary, and a deterministic `reference_digest`. Every reference entry commits
its locator state and locator digest; correlated imports must match that
persisted commitment. The live locator commitment covers the role, artifact
digest, canonical path, and locator state, while the import separately binds
the chain and reference digests. Record load, list, archive
validation/import, and final reconciliation recompute these commitments and
fail closed on changed state. See
`examples/valid/correlation-chain-reference.json` for the persisted shape.

The Mission record and checkpoint update for an import is one recoverable
transaction. Continuation adds its matching event-loop decision to that same
transaction. AO Mission serializes writers with a per-Mission operating-system
lock, compares the exact pre-image before writing, durably journals every
participating pre-image, and atomically replaces same-directory files. After
all candidate files are durable, it rewrites the journal to an explicit
committed state before best-effort cleanup. Every related read path validates
and recovers a surviving journal before exposing state. Prepared recovery is
idempotent and restores every exact pre-image; committed recovery preserves
the candidates and only finishes cleanup. Malformed journals, checkpoint
semantics, record/checkpoint disagreement, or event-decision disagreement fail
closed without restoration.

Final correlated reconciliation requires one supplied or persisted chain to
cover every correlation-bound import:

```sh
ao-mission final reconcile \
  --mission <mission-id> \
  --correlation-chain <complete-correlation-chain.json>
```

When every import is already bound to one persisted chain, a caller-supplied
chain must have that exact chain digest. Imports made through multiple partial
chains may be consolidated only by one canonical role-sorted chain containing
exactly the imported roles. Each consolidated entry must match the
digest-bound producer and provenance semantics in its original persisted
reference; this does not replace or rewrite any original chain identity.

Mission always reopens and rehashes each live persisted import path, including
when the caller supplies a complete chain whose own locators point to valid
copies. Archive export changes a redacted binding to the explicit
`archive_redacted` locator state, transforms the persisted reference entry,
retains a commitment to the source live locator, and recomputes both the archive
locator and reference commitments. Only that integrity-valid state can use an
exact caller-supplied chain to rehydrate a role, after role, artifact digest,
source locator commitment, and chain digest all match. A live binding
containing the redaction sentinel, a copied or
changed live artifact, an incomplete chain, or a changed reference returns a
blocked final reconciliation. Imports without `--correlation-chain` and
missions without correlation-bound imports retain their existing behavior.
Public-safe archive redaction recognizes absolute POSIX paths after
punctuation, Windows drive paths, and backslash UNC paths while preserving
relative and protocol-relative references such as `//localhost/app.js`.
