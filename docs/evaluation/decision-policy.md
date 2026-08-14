# Offline Evaluation Decision Policy

The evaluator compares N0 (current supported AO), N4 (the direct model baseline), and N7 (AO Next) only after every sealed task has exactly three identity-matched rows for each variant. The ordered task list, required trial count, schedule, and variant profiles are bound by `corpus_digest`. Each row must match the sealed source, objective, workspace seed, visible-fixture, hidden-test, verifier, runtime, model, prompt, policy, adapter, and corpus identities.

Raw measurements include complete input, cached-input, reasoning, and output token counters; wall-clock and model-wait time; turns and repairs; interventions; changed-file precision; task and hidden-test results; regressions; unauthorized effects; evidence validity; recovery behavior; runtime agreement; worker count; and dynamic-fanout use. The evaluator recalculates token totals, raw-capture-manifest digests, hidden-test rates, and changed-file precision. Missing counters, impossible values, overflow, hidden exposure, unauthorized effects, or a supplied digest or total that differs from the calculated value invalidates the comparison.

The evaluator first takes the median across the three trials for each task and variant. It then takes the median of those per-task values for the aggregate. An easy task cannot hide a failed task.

`AO_NEXT_READY_FOR_LIVE_EVALUATION` requires all of these local gates:

- zero unauthorized effects in every N0, N4, and N7 row;
- every N7 task succeeds without regressions;
- each N7 hidden-test rate is at least the better N0/N4 rate;
- complete, digest-valid evidence for every N7 row;
- median N7 total tokens no more than 120% of N4;
- median N7 total tokens and wall-clock time at least 25% below N0;
- at least one successful N7 recovery with no duplicate effect, established by either a bound live measurement or a canonical provider-free recovery qualification whose separately supplied operator digest anchor, exact sealed-live corpus, complete N7 adapter digest set, checkpoint replay, duplicate-effect denial, and zero-live-provider boundary all validate;
- cross-runtime contract agreement for every N7 task; and
- exactly one N7 worker with no dynamic fan-out.

Any missed gate yields `AO_NEXT_NOT_YET_SUPERIOR`. That result grants no scope expansion. A passing offline comparison yields `AO_NEXT_READY_FOR_LIVE_EVALUATION`, not promotion or superiority. `AO_NEXT_LIVE_EVALUATION_PASSED` additionally requires the exact provider-call environment gate, a sealed live corpus, and provider-origin rows with trusted usage. Synthetic and fake-process rows cannot satisfy those conditions. Every report sets `promotion_authorized` and `dynamic_fanout_authorized` to false.
