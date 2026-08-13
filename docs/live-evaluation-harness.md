# Live Evaluation Harness

`run-current-ao-baseline` records an N0 trial through AO2's native sandbox adapter and digest-approved patch path. `run-direct-baseline` records an N4 trial through native Codex. `run-live` records an N7 trial through the one-worker AO Next engine. None runs unless the operator process already contains this exact gate:

```text
AO_NEXT_LIVE_PROVIDER_CALLS=operator-authorized
```

The command checks the gate before reading `--input` or resolving an executable. AO Next never sets the gate.

## Input

The commands accept `--input <path>` pointing to one strict `ao.next.live-run-input.v1` JSON value. Duplicate keys and unknown fields are rejected. The value contains:

- the complete sealed v2 corpus and selected task, trial index, schedule position, trial ID, and unique workspace instance ID;
- the exact run request and command-verifier profile;
- paths to the source snapshot, semantic objective, visible fixtures, hidden-test tree, and adapter-turn output schema.
- one empty operator-owned raw-capture directory outside every worker authority root;
- for N0 only, a digest-bound AO2 program and provider program binding.

The run request must bind the corpus source, workspace seed, objective, model, prompt, policy, verifier, adapter, and runtime identities. Live intake derives the minimum trusted-usage envelope as `2 * context_limit + 2 * output_limit`. Multiplication and addition are checked; zero model limits, arithmetic overflow, or a lower `max_tokens` rejects the input before workspace preparation or provider spawn. For `context_limit=262144` and `output_limit=20000`, the minimum is `564288`.

`preflight-live-input` performs this intake without provider authority and without mutating the live workspace. The exact live command alone creates and verifies the deterministic Git repository immediately before provider spawn.

The workspace must match the sealed product snapshot before Git metadata exists. The harness rejects root or nested `.git` metadata, linked-worktree pointers, submodule markers, symlinks, path escapes, and non-regular entries. It then creates one ordinary repository at the workspace root, a fixed branch, and a deterministic seed commit whose message binds the sealed seed digest. Empty greenfield seeds use an empty commit. Git runs with fixed process-local identity, timestamps, configuration, and environment rather than host-global configuration. Immediately before provider spawn, the harness rechecks the canonical repository root, common directory, branch, `HEAD`, and clean porcelain status. The objective, visible fixtures, hidden tests, verifier profile, and output schema are re-read under byte and path bounds. Hidden tests must be outside every worker authority root.

N7 supports the existing Codex and Claude structured adapters. Provider processes run read-only; workspace mutation passes through structured effect admission. The live N7 runner admits one provider process per row and rejects a second dispatch before spawn. N4 uses Codex's native workspace-write sandbox. N0 asks AO2 to create its disposable sandbox, invokes one exact Codex process there, previews the patch, and applies only the digest-approved patch to the trial workspace. Every variant runs the sealed command verifier without a shell.

## Output and status

Each command writes one `ao.next.live-run-record.v1` JSON value to stdout. The record contains the terminal state, one v2 measurement, canonical Git identity, ordered capture digests, private capture-index digest, verifier-report digest, bounded AO2 preview/apply diagnostics where applicable, and a digest over the record material. Usage comes from the provider envelope. Model-authored token counters are discarded.

Provider output retention is the first post-provider operation. Exact bounded stdout and stderr use create-only owner-only files in the supplied empty private capture directory. A create-only temporary index is published atomically by hard link only after its run, trial, workspace, runtime, order, status, byte-count, and digest bindings are complete. The harness verifies the published index before normalization or any control step and never persists a second success-path copy.

The verified capture must contain one trusted terminal usage envelope. AO Next rejects missing, duplicate, malformed, negative, non-integer, or overflowed counters. It also requires `cached_input_tokens <= input_tokens` and `reasoning_tokens <= output_tokens`. The checked sum of input, cached input, reasoning, and output tokens must not exceed the sealed `max_tokens`. Only then may N0 run AO2 preview, AO2 apply, and the verifier; N7 may admit effects and run the verifier; and N4 may run the verifier and assemble public evidence. N4's native workspace-write sandbox can modify the workspace inside the provider process, before AO Next receives and retains its output. This hosted-equivalence exception cannot be reordered; invalid N4 usage still produces no verifier call, measurement, or live-run record.

Provider, token-envelope, AO2 preview, AO2 apply, verifier, and evidence failures preserve the captured bytes plus private terminal-stage metadata; retained bytes do not convert a control failure into a valid measurement. Token-envelope metadata binds the sanitized counters, checked total when available, limit, and capture digest without copying provider text. AO2 diagnostics bind the exact program digest, sanitized structured command, target and sandbox identities, exit status, elapsed time, bounded stdout and stderr, sizes, and digests.

Exit statuses are stable:

- `0`: passed;
- `2`: CLI usage error;
- `3`: invalid or drifted input;
- `4`: runtime or effect failure;
- `5`: deterministic verification failure;
- `6`: interruption;
- `7`: evidence or hidden-material failure;
- `8`: live authority denied.

The harness reports one trial. `evaluate-live` requires all 27 provider-origin rows: three tasks, three variants, and three counterbalanced trials. It requires the exact operator gate. `evaluate` remains offline-only and cannot return `AO_NEXT_LIVE_EVALUATION_PASSED`.
