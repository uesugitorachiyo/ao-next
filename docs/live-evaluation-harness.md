# Live Evaluation Harness

`run-live` records an N7 trial through the one-worker AO Next engine. `run-direct-baseline` records an N4 trial through native Codex. Neither command runs unless the operator process already contains this exact gate:

```text
AO_NEXT_LIVE_PROVIDER_CALLS=operator-authorized
```

The command checks the gate before reading `--input` or resolving an executable. AO Next never sets the gate.

## Input

Both commands accept `--input <path>` pointing to one strict `ao.next.live-run-input.v1` JSON value. Duplicate keys and unknown fields are rejected. The value contains:

- the complete sealed v2 corpus and selected task, trial index, schedule position, trial ID, and unique workspace instance ID;
- the exact run request and command-verifier profile;
- paths to the source snapshot, semantic objective, visible fixtures, hidden-test tree, and adapter-turn output schema.

The run request must bind the corpus source, workspace seed, objective, model, prompt, policy, verifier, adapter, and runtime identities. The workspace must match the sealed source snapshot before process spawn. The objective, visible fixtures, hidden tests, verifier profile, and output schema are re-read under byte and path bounds. Hidden tests must be outside every worker authority root.

N7 supports the existing Codex and Claude structured adapters. Provider processes run read-only; workspace mutation passes through structured effect admission. N4 requires Codex and uses its native workspace-write sandbox. Both variants run the sealed command verifier without a shell.

## Output and status

Each command writes one `ao.next.live-run-record.v1` JSON value to stdout. The record contains the terminal state, one v2 measurement, ordered capture digests, verifier-report digest, and a digest over the record material. Usage comes from the provider envelope. Model-authored token counters are discarded.

Exit statuses are stable:

- `0`: passed;
- `2`: CLI usage error;
- `3`: invalid or drifted input;
- `4`: runtime or effect failure;
- `5`: deterministic verification failure;
- `6`: interruption;
- `7`: evidence or hidden-material failure;
- `8`: live authority denied.

The harness reports one trial. `evaluate` requires all 27 rows: three tasks, three variants, and three counterbalanced trials. Offline and fake-process records remain ineligible for `AO_NEXT_LIVE_EVALUATION_PASSED`.
