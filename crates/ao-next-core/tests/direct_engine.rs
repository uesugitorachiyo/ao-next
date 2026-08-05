use std::collections::{BTreeSet, VecDeque};
use std::path::{Path, PathBuf};

use ao_next_core::adapter::scripted::ScriptedAdapter;
use ao_next_core::adapter::{
    AdapterAction, AdapterIdentity, AdapterTurn, ControlMutation, RuntimeAdapter, TokenUsage,
};
use ao_next_core::contracts::{
    AuthorityEnvelope, Digest, EffectKind, EffectRequest, ExternalEffectPolicy, ModelProfile,
    NetworkPolicy, RunLimits, RunRequest, RunState, SourceIdentity, VerifierProfile,
    WorkspaceIdentity,
};
use ao_next_core::effects::LocalEffectBroker;
use ao_next_core::engine::{DirectEngine, EngineVerifier, VerificationOutcome};
use ao_next_core::terminal::{InvalidTransition, RunLifecycle};
use chrono::{DateTime, Utc};
use tempfile::TempDir;

const ZERO_DIGEST: &str = "sha256:0000000000000000000000000000000000000000000000000000000000000000";
const ONE_DIGEST: &str = "sha256:1111111111111111111111111111111111111111111111111111111111111111";

fn timestamp(value: &str) -> DateTime<Utc> {
    DateTime::parse_from_rfc3339(value)
        .expect("fixture timestamp")
        .with_timezone(&Utc)
}

fn digest(value: &str) -> Digest {
    Digest::new(value).expect("fixture digest")
}

fn request(root: &Path) -> RunRequest {
    RunRequest {
        schema_version: "ao.next.run-request.v1".into(),
        run_id: "run-01".into(),
        objective: "Complete the scripted fixture".into(),
        source: SourceIdentity {
            repository: "fixture".into(),
            head: digest(ZERO_DIGEST),
        },
        workspace: WorkspaceIdentity {
            workspace_id: "workspace-01".into(),
            root: root.to_path_buf(),
            seed_digest: digest(ONE_DIGEST),
        },
        model_profile: ModelProfile {
            runtime: "scripted".into(),
            model_identifier: "fixture-model".into(),
            reasoning_effort: "high".into(),
            system_prompt_digest: digest(ZERO_DIGEST),
            tool_contract_digest: digest(ONE_DIGEST),
            context_limit: 32_000,
            output_limit: 4_000,
            adapter_version: "scripted-v1".into(),
        },
        authority: AuthorityEnvelope {
            schema_version: "ao.next.authority-envelope.v1".into(),
            issued_by: "operator".into(),
            issued_at: timestamp("2026-08-05T00:00:00Z"),
            expires_at: timestamp("2026-08-06T00:00:00Z"),
            capabilities: BTreeSet::new(),
            allowed_roots: vec![root.to_path_buf()],
            allowed_programs: BTreeSet::new(),
            network: NetworkPolicy::Denied,
            allowed_network_hosts: BTreeSet::new(),
            external_effects: ExternalEffectPolicy::Denied,
        },
        verifier_profile: VerifierProfile {
            profile_id: "scripted".into(),
            profile_digest: digest(ONE_DIGEST),
            commands: Vec::new(),
            required_artifacts: Vec::new(),
        },
        policy_digest: digest(ZERO_DIGEST),
        limits: RunLimits {
            max_input_bytes: 64 * 1024,
            max_turns: 4,
            max_repair_attempts: 1,
            max_run_ms: 10_000,
            max_effect_timeout_ms: 1_000,
            max_output_bytes: 4_096,
            max_tokens: 1_000,
        },
    }
}

fn identity() -> AdapterIdentity {
    AdapterIdentity {
        runtime: "scripted".into(),
        model_identifier: "fixture-model".into(),
        adapter_version: "scripted-v1".into(),
        worker_id: "worker-01".into(),
    }
}

fn usage() -> TokenUsage {
    TokenUsage {
        input_tokens: 10,
        cached_input_tokens: 2,
        reasoning_tokens: 5,
        output_tokens: 3,
        output_bytes: 64,
    }
}

fn turn(actions: Vec<AdapterAction>) -> AdapterTurn {
    AdapterTurn {
        actions,
        usage: usage(),
        model_claimed_success: false,
        control_mutations: Vec::new(),
    }
}

struct ScriptedVerifier {
    outcomes: VecDeque<VerificationOutcome>,
    calls: usize,
}

impl ScriptedVerifier {
    fn new(outcomes: impl IntoIterator<Item = VerificationOutcome>) -> Self {
        Self {
            outcomes: outcomes.into_iter().collect(),
            calls: 0,
        }
    }
}

impl EngineVerifier for ScriptedVerifier {
    fn verify(&mut self, _: &RunRequest) -> VerificationOutcome {
        self.calls += 1;
        self.outcomes.pop_front().expect("scripted outcome")
    }
}

fn verification(passed: bool) -> VerificationOutcome {
    VerificationOutcome {
        passed,
        report_digest: digest(if passed { ONE_DIGEST } else { ZERO_DIGEST }),
        summary: if passed { "passed" } else { "failed" }.into(),
    }
}

#[test]
fn lifecycle_rejects_invalid_and_terminal_transitions() {
    let mut lifecycle = RunLifecycle::new();
    assert_eq!(
        lifecycle
            .transition(RunState::Running)
            .expect_err("received cannot run directly"),
        InvalidTransition {
            from: RunState::Received,
            to: RunState::Running
        }
    );
    lifecycle
        .transition(RunState::Validated)
        .expect("received to validated");
    lifecycle
        .transition(RunState::Running)
        .expect("validated to running");
    lifecycle
        .transition(RunState::Interrupted)
        .expect("running to interrupted");
    assert_eq!(
        lifecycle
            .transition(RunState::Running)
            .expect_err("terminal cannot resume directly"),
        InvalidTransition {
            from: RunState::Interrupted,
            to: RunState::Running
        }
    );
}

#[test]
fn verified_success_uses_one_worker_identity_for_the_entire_run() {
    let workspace = TempDir::new().expect("workspace");
    let request = request(workspace.path());
    let broker = LocalEffectBroker::new(1_000, 4_096);
    let engine = DirectEngine::new(&broker);
    let mut adapter = ScriptedAdapter::new(identity(), [Ok(turn(vec![AdapterAction::Verify]))]);
    let mut verifier = ScriptedVerifier::new([verification(true)]);

    let outcome = engine.run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Passed);
    assert_eq!(outcome.metrics.turns, 1);
    assert_eq!(verifier.calls, 1);
    let worker_ids: BTreeSet<_> = outcome
        .events
        .iter()
        .filter_map(|event| event.worker_id.as_deref())
        .collect();
    assert_eq!(worker_ids, BTreeSet::from(["worker-01"]));
    assert_eq!(adapter.identity(), identity());
}

#[test]
fn failed_verification_returns_to_the_same_worker_for_one_repair() {
    let workspace = TempDir::new().expect("workspace");
    let request = request(workspace.path());
    let broker = LocalEffectBroker::new(1_000, 4_096);
    let engine = DirectEngine::new(&broker);
    let mut adapter = ScriptedAdapter::new(
        identity(),
        [
            Ok(turn(vec![AdapterAction::Verify])),
            Ok(turn(vec![AdapterAction::Verify])),
        ],
    );
    let mut verifier = ScriptedVerifier::new([verification(false), verification(true)]);

    let outcome = engine.run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Passed);
    assert_eq!(outcome.metrics.turns, 2);
    assert_eq!(outcome.metrics.repair_attempts, 1);
    assert_eq!(verifier.calls, 2);
    assert!(outcome.events.iter().all(|event| {
        event
            .worker_id
            .as_deref()
            .is_none_or(|id| id == "worker-01")
    }));
}

#[test]
fn denied_effect_terminates_without_becoming_an_adapter_retry() {
    let workspace = TempDir::new().expect("workspace");
    let request = request(workspace.path());
    let broker = LocalEffectBroker::new(1_000, 4_096);
    let engine = DirectEngine::new(&broker);
    let mut effect = EffectRequest {
        effect_id: "effect-denied".into(),
        run_id: "run-01".into(),
        kind: EffectKind::RunProgram,
        program: Some("/usr/bin/printf".into()),
        args: vec!["must-not-run".into()],
        paths: Vec::new(),
        timeout_ms: 100,
        input_digest: digest(ZERO_DIGEST),
    };
    effect.paths = vec![PathBuf::from(workspace.path())];
    let mut adapter = ScriptedAdapter::new(
        identity(),
        [
            Ok(turn(vec![AdapterAction::Effect(effect)])),
            Ok(turn(vec![AdapterAction::Verify])),
        ],
    );
    let mut verifier = ScriptedVerifier::new([verification(true)]);

    let outcome = engine.run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Denied);
    assert_eq!(outcome.metrics.turns, 1);
    assert_eq!(verifier.calls, 0);
}

#[test]
fn adapter_failure_and_interrupt_have_distinct_terminal_states() {
    let workspace = TempDir::new().expect("workspace");
    let request = request(workspace.path());
    let broker = LocalEffectBroker::new(1_000, 4_096);
    let engine = DirectEngine::new(&broker);
    let mut failed = ScriptedAdapter::new(
        identity(),
        [Err(ao_next_core::adapter::AdapterError::Runtime(
            "fixture failure".into(),
        ))],
    );
    let mut verifier = ScriptedVerifier::new([]);
    let failed_outcome = engine.run(&request, &mut failed, &mut verifier);
    assert_eq!(failed_outcome.terminal_state, RunState::Failed);
    assert_eq!(
        failed_outcome.failure_code.as_deref(),
        Some("adapter_failure")
    );

    let mut interrupted =
        ScriptedAdapter::new(identity(), [Ok(turn(vec![AdapterAction::Interrupt]))]);
    let interrupted_outcome = engine.run(&request, &mut interrupted, &mut verifier);
    assert_eq!(interrupted_outcome.terminal_state, RunState::Interrupted);
    assert_eq!(
        interrupted_outcome.failure_code.as_deref(),
        Some("adapter_interrupted")
    );
}

#[test]
fn model_success_claim_without_verification_cannot_pass() {
    let workspace = TempDir::new().expect("workspace");
    let mut request = request(workspace.path());
    request.limits.max_turns = 1;
    let broker = LocalEffectBroker::new(1_000, 4_096);
    let engine = DirectEngine::new(&broker);
    let mut claimed = turn(Vec::new());
    claimed.model_claimed_success = true;
    let mut adapter = ScriptedAdapter::new(identity(), [Ok(claimed)]);
    let mut verifier = ScriptedVerifier::new([]);

    let outcome = engine.run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Failed);
    assert_eq!(outcome.failure_code.as_deref(), Some("turn_limit"));
    assert_eq!(verifier.calls, 0);
}

#[test]
fn authority_policy_verifier_and_terminal_mutations_are_rejected() {
    for mutation in [
        ControlMutation::Authority,
        ControlMutation::Policy,
        ControlMutation::Verifier,
        ControlMutation::TerminalState,
    ] {
        let workspace = TempDir::new().expect("workspace");
        let request = request(workspace.path());
        let broker = LocalEffectBroker::new(1_000, 4_096);
        let engine = DirectEngine::new(&broker);
        let mut malicious = turn(vec![AdapterAction::Verify]);
        malicious.control_mutations = vec![mutation];
        let mut adapter = ScriptedAdapter::new(identity(), [Ok(malicious)]);
        let mut verifier = ScriptedVerifier::new([verification(true)]);

        let outcome = engine.run(&request, &mut adapter, &mut verifier);

        assert_eq!(outcome.terminal_state, RunState::Failed);
        assert_eq!(
            outcome.failure_code.as_deref(),
            Some("adapter_control_mutation")
        );
        assert_eq!(verifier.calls, 0);
    }
}

#[test]
fn token_limit_fails_before_another_turn() {
    let workspace = TempDir::new().expect("workspace");
    let mut request = request(workspace.path());
    request.limits.max_tokens = 1;
    request.limits.max_output_bytes = 1;
    let broker = LocalEffectBroker::new(1_000, 4_096);
    let engine = DirectEngine::new(&broker);
    let mut adapter = ScriptedAdapter::new(identity(), [Ok(turn(Vec::new()))]);
    let mut verifier = ScriptedVerifier::new([]);

    let outcome = engine.run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Failed);
    assert_eq!(outcome.failure_code.as_deref(), Some("token_limit"));
    assert_eq!(outcome.metrics.turns, 1);
}

#[test]
fn output_limit_fails_before_another_turn() {
    let workspace = TempDir::new().expect("workspace");
    let mut request = request(workspace.path());
    request.limits.max_tokens = 10_000;
    request.limits.max_output_bytes = 1;
    let broker = LocalEffectBroker::new(1_000, 4_096);
    let engine = DirectEngine::new(&broker);
    let mut adapter = ScriptedAdapter::new(identity(), [Ok(turn(Vec::new()))]);
    let mut verifier = ScriptedVerifier::new([]);

    let outcome = engine.run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Failed);
    assert_eq!(outcome.failure_code.as_deref(), Some("output_limit"));
    assert_eq!(outcome.metrics.turns, 1);
}

#[test]
fn elapsed_time_limit_interrupts_before_an_adapter_turn() {
    let workspace = TempDir::new().expect("workspace");
    let mut request = request(workspace.path());
    request.limits.max_run_ms = 0;
    let broker = LocalEffectBroker::new(1_000, 4_096);
    let engine = DirectEngine::new(&broker);
    let mut adapter = ScriptedAdapter::new(identity(), [Ok(turn(vec![AdapterAction::Verify]))]);
    let mut verifier = ScriptedVerifier::new([verification(true)]);

    let outcome = engine.run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Interrupted);
    assert_eq!(outcome.failure_code.as_deref(), Some("time_limit"));
    assert_eq!(outcome.metrics.turns, 0);
    assert_eq!(verifier.calls, 0);
}
