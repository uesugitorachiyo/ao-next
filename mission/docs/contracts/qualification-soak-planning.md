# Qualification soak planning contract

`ao-mission qualification soak-plan --fixture <path> --json` consumes
`ao.mission.soak-plan-input.v1` and emits
`ao.mission.soak-plan-readback.v1`. It is a deterministic read-only planner and
validator. It may report activation eligibility, but it does not execute tests,
schedule nodes, approve work, mutate repositories, call providers, publish,
release, deploy, or advance authority.

The fixture binds a plan and Mission identity, exact AO Mission source head,
race or non-race execution-profile identity and digest, explicit test
classification, measured duration history, partition and node budgets, repeat
policy, retry policy, timeout policy, lease bounds, activation binding, and
safety boundaries. Input is limited to a 1 MiB regular non-symlink file.
Unknown fields, duplicate keys, trailing JSON, traversal, malformed JSON, and
oversized input are rejected before planning.

Every runnable test is classified as `regular` or `scale` before partitioning.
A scale test declares a positive bounded workload dimension and can request and
receive repeat count one only. Repeated regular work is split from scale work
when a requested partition contains both. The readback reports requested and
effective repeat counts plus the amplification decision.

Duration history is bound to test identity, exact source head,
execution-profile digest, and integer milliseconds. Each history set contains
3-64 finite positive integer samples. The deterministic conservative estimate
sorts each set, selects its nearest-rank 95th percentile, multiplies it by the
effective repeat count with checked integer arithmetic, sums the test estimates,
and adds declared setup and safety overhead once per planned partition. Each
estimate must fit its per-attempt timeout. Its maximum-attempt allowance must fit
the total node timeout and node budget. The retry-inclusive aggregate described
below must fit the aggregate lease maximum.

Retry policy may set `maximum_total_retries`. Omission or explicit `null`
preserves the legacy conservative aggregate bound: the sum of every planned
partition estimate multiplied by `maximum_attempts`. Zero permits no aggregate
retry slots, while a positive value K permits at most K aggregate retry slots.
Retry-slot capacity is the planned partition count multiplied by
`maximum_attempts - 1`. A negative value produces `retry_budget_invalid`; a
value above capacity produces `retry_budget_exceeds_attempt_capacity`. Either
invalid value denies activation and retains the conservative legacy aggregate
bound.

For a valid cap, each planned partition estimate is repeated
`maximum_attempts - 1` times to form retry-slot costs. The costs are sorted
descending, and the largest K are added to the base duration.
`lease_budget.total_planned_ms` remains that base duration, while
`lease_budget.total_planned_with_retry_ms` reports the capped aggregate bound.
Per-partition `retry_allowance_ms`, per-attempt timeout, total-node timeout, and
node-budget enforcement remain the full per-partition maximum-attempt bounds.
A valid capped aggregate above the lease maximum still produces
`lease_budget_exceeded`.

The policy digest binds budgets, repeat policy, retry policy, timeout policy,
lease policy, and safety boundaries. It must be recorded before the
`pre_activation` state. Retry policy must preserve node identity, test set,
scale dimension, repeat count, source head, execution profile, and the original
phase clock, and it must require evidence after every failed attempt.
Because the optional cap is part of retry policy, a present value is bound by
both the canonical input digest and policy digest; `omitempty` preserves the
legacy digest representation when the field is omitted.

An execution system must enforce a present cap as a total retry-count limit as
well as retain every per-partition bound. It must also enforce the reported
`total_planned_with_retry_ms` aggregate duration. The fixed canary requires a
present cap to equal its authority record's `maximum_retry_count`; a mismatch
produces `retry_budget_authority_mismatch` before any child process is
reachable. The planner itself remains read-only and performs no runtime work.

Contract conflicts produce a sorted exact conflict-code list,
`activation_allowed=false`, and one remediation action. Even an eligible
readback remains `safe_to_execute=false`, `executes_work=false`,
`approves_work=false`, `mutates_repositories=false`, `calls_providers=false`,
`publishes=false`, `releases=false`, `deploys=false`,
`advances_authority=false`, and `rsi_remains_denied=true`.
