# Python Repair Product Gate

`ao-mission issue-repair product-gate` is a read-only validator for one sealed Python repair evaluation. It converts strict evidence into three independent decisions without executing a repair or changing Mission state.

## Invocation

```sh
ao-mission issue-repair product-gate \
  --root /path/to/sealed-root \
  --manifest /path/to/sealed-root/manifest.json \
  --json
```

The manifest uses `ao.mission.python-repair-product-gate-manifest.v1`. It binds the expected gate, repository, issue, source commit, source tree, candidate tree, correlation identity, record digests, and exactly one direct-child evidence file. The evidence uses `ao.mission.python-repair-product-gate-evidence.v1`; every identity and record digest must equal the manifest value.

Both files must be regular files of at most 64 KiB. The command rejects duplicate keys, unknown fields, malformed JSON, symlinks, hardlinks, traversal, digest or size drift, and paths outside the canonical root.

## Required Evidence

The evidence binds:

- gate, repository, issue, source, candidate, correlation, and source-tree identities;
- selection time, record digest, and deterministic selection status;
- a failing RED reproduction with nonzero observed exit, exact fixture/output/evidence digests, and no network, Git history, oracle, credentials, or external effects;
- a sealed candidate tree and patch, exact seal and suite digests, passing focused and applicable suites, baseline comparison, and deterministic replay;
- a strict v3 AO2 repair pack with zero failed rows;
- optional governed qualification and process-lifecycle records when their bindings are present;
- security routing, independent score, negative-mutation coverage, and freshness;
- explicit zero values for provider calls, third-party mutation, publication, deployment, release, credential changes, and authority expansion.

Optional qualification and lifecycle bindings are all-or-nothing. A digest without its matching evidence, or evidence without its digest, is rejected. The gate is L1-only and rejects any authority level other than `L1`.

## Decisions

The output schema is `ao.mission.python-repair-product-gate-result.v1` and reports:

- `technical_repair_decision`: whether the repair itself is technically supported;
- `governed_qualification_decision`: whether the optional governed qualification and lifecycle contracts are complete;
- `release_decision`: `eligible_for_separate_authorization` only when governed qualification and lifecycle both pass; otherwise `not_qualified`.

A positive release decision is not release authority. The output always reports execution, approval, repository mutation, deployment, publication, and authority advancement as false. Atlas remains responsible for sequencing work; Mission only validates and reconciles the imported result.
