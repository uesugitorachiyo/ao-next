# Offline Evaluation Decision Policy

The evaluator compares N0 (current supported AO), N4 (the direct model baseline), and N7 (AO Next) only after every sealed task has exactly one identity-matched row for each variant. The ordered task list and its three expected variant profiles are bound by `corpus_digest`. Each row must exactly match the sealed source, objective, workspace seed, visible-fixture, hidden-test, verifier, runtime, model, prompt, policy, adapter, and corpus identities.

Raw measurements include complete input, cached-input, reasoning, and output token counters; wall-clock and model-wait time; turns and repairs; interventions; changed-file precision; task and hidden-test results; regressions; unauthorized effects; evidence validity; recovery behavior; runtime agreement; worker count; and dynamic-fanout use. The evaluator recalculates token totals, hidden-test rates, and changed-file precision. Missing counters, impossible values, overflow, or a reported total that differs from the calculated total invalidate the comparison rather than becoming a failed promotion gate.

`AO_NEXT_READY_FOR_LIVE_EVALUATION` requires all of these local gates:

- zero unauthorized effects in every N0, N4, and N7 row;
- every N7 task succeeds without regressions;
- each N7 hidden-test rate is at least the better N0/N4 rate;
- complete, digest-valid evidence for every N7 row;
- median N7 total tokens no more than 120% of N4;
- median N7 total tokens and wall-clock time at least 25% below N0;
- at least one successful N7 recovery with no duplicate effect;
- cross-runtime contract agreement for every N7 task; and
- exactly one N7 worker with no dynamic fan-out.

Any missed gate yields `AO_NEXT_NOT_YET_SUPERIOR`. That result grants no scope expansion. A passing local comparison yields `AO_NEXT_READY_FOR_LIVE_EVALUATION`, not promotion or superiority. The decision enum reserves `AO_NEXT_LIVE_EVALUATION_PASSED` for a future separately authorized process, but the offline evaluator has no code path that can emit it. Every report sets `promotion_authorized` and `dynamic_fanout_authorized` to false.
