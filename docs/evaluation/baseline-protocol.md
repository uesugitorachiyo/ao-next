# Comparative Baseline Protocol

AO Next compares three variants over identical source, objective, workspace seed, visible fixtures, hidden-test identity, verifier profile, and declared limits:

- N0: current supported AO workflow.
- N4: direct frontier model in its native harness.
- N7: the same task through AO Next.

Each variant runs exactly three times per task in this counterbalanced order:

- Trial 0: N0, N4, N7.
- Trial 1: N4, N7, N0.
- Trial 2: N7, N0, N4.

Every trial uses a new workspace instance reconstructed from the sealed seed. The harness binds the run, trial, schedule position, raw captures, runtime, model, adapter, and all task inputs. Reused runs, trials, captures, or workspace instances invalidate the comparison.

The operator-owned corpus contains a greenfield engineering application, a bounded defect repair, and an artifact-reconciliation task. Hidden tests stay outside worker workspaces and authority roots. The checked-in zero/one corpus is named `synthetic-corpus-v1.json` and cannot decode as the live v2 contract.

The evaluator calculates a median for each task and variant before it calculates cross-task medians. It rejects missing baselines, incomplete token rows, corpus or identity drift, schedule errors, raw-capture mismatch, hidden exposure, timing contradictions, unauthorized effects, and N7 worker or fan-out violations. [The decision policy](decision-policy.md) defines the score gates. Scripted and fake-process rows cannot produce `AO_NEXT_LIVE_EVALUATION_PASSED`.

Provider calls, credentials, network access, remote mutation, publication, release, deployment, AO migration, and superiority claims are outside local qualification authority.
