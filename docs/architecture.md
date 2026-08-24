# AO Next Architecture

AO Next is a separate Rust modular monolith supervised by AO Mission. Mission retains objective identity, checkpoints, and terminal reconciliation while AO Next owns strict intake, one-worker direct execution, effect admission, product verification, evidence, and recovery.

The N7 execution path prepares a deterministic Git seed and non-authorizing receipt before dispatch. The operator then issues one `ao.next.n7-execution-authority.v1` document bound to that receipt and observed Git base. The Engine validates the document before provider intent and again at each fresh effect admission. Each authorized native effect records create-only intent before execution and content-addressed completion afterward; intent without completion is unknown and cannot be retried. One journal lifecycle validator enforces provider, effect, verification, and terminal order on recovery, appends, checkpoints, verifier records, and terminal publication.

```text
operator objective and requested scope
        -> strict intake validation
        -> prepared Git receipt
        -> post-preparation N7 execution authority
        -> one runtime adapter and one worker
        -> structured effect broker
        -> authorized local workspace effects
        -> mechanical and product verifiers
        -> content-addressed evidence
        -> terminal readback for Mission reconciliation
```

The deterministic kernel owns capability checks, program and path admission, limits, verifier configuration, evidence retention, checkpoint identity, and terminal classification. The worker may inspect, edit, test, and repair within those boundaries, but cannot alter them.

Future provider trials use two explicit paths. N7 normalizes a bounded provider envelope into structured AO actions, admits each effect, runs the sealed command verifier, and records trusted usage. N4 gives native Codex workspace-write access, then runs the same verifier and records the same measurement fields. Both paths preserve one worker and bind the sealed corpus, task, trial, source, workspace, model, prompt, policy, verifier, adapter, and runtime identities before process spawn.

Hidden tests remain outside worker workspaces and authority roots. Verifier commands are fixed by their profile digest and run after worker activity. The harness scans the final workspace for hidden-file digests before it can report success.

The launch candidate has no dynamic worker creation, runtime graph growth, permanent planner or reviewer roles, workflow compiler, or generic workflow language. Those remain future questions only if paired evaluation shows the direct candidate repeatedly needs them.

Prompt and tool instructions begin small for each model profile. Instructions are added only after repeated measured failure. Verification, safety, permissions, and evidence remain deterministic harness responsibilities rather than prompt conventions.

Fresh capture retention writes and synchronizes raw files and the canonical incomplete index before the journal records `provider_output_retained`. The Engine then publishes the staged final name and records publication and verification. `recover-live` accepts incomplete-only and final-only crash states from retained bytes. It reads the prepared receipt, N7 authority, journal, and Git identity without starting or resolving a provider. Provider intent without retained output and effect intent without completion remain terminally unknown. Completed effects can proceed to verification after authority expiry; a fresh effect cannot.
