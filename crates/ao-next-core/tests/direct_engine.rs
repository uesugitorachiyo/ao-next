use std::cell::Cell;
use std::collections::{BTreeSet, VecDeque};
use std::path::{Path, PathBuf};

use ao_next_core::adapter::scripted::ScriptedAdapter;
use ao_next_core::adapter::{
    AdapterAction, AdapterIdentity, AdapterTurn, ControlMutation, RuntimeAdapter, TokenUsage,
};
use ao_next_core::contracts::{
    AuthorityEnvelope, Capability, Digest, EffectKind, EffectRequest, ExternalEffectPolicy,
    ModelProfile, N7ExecutionAuthority, NetworkPolicy, RunLimits, RunRequest, RunState,
    SourceIdentity, VerifierProfile, WorkspaceIdentity,
};
use ao_next_core::effects::{
    AuthorizedEffect, EffectBroker, EffectBrokerError, EffectOutput, LocalEffectBroker,
};
use ao_next_core::engine::{DirectEngine, EngineVerifier, VerificationOutcome};
use ao_next_core::evidence::digest_bytes;
use ao_next_core::policy::PolicyDenial;
use ao_next_core::recovery::CheckpointJournal;
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

fn expired_n7_execution_authority(root: &Path) -> N7ExecutionAuthority {
    let now = Utc::now();
    N7ExecutionAuthority {
        schema_version: "ao.next.n7-execution-authority.v1".into(),
        authority_id: "expired-authority".into(),
        issued_by: "operator".into(),
        prepared_run_digest: digest_bytes(b"prepared"),
        preparation_input_digest: digest_bytes(b"input"),
        preparation_request_digest: digest_bytes(b"request"),
        base_commit: "0123456789abcdef0123456789abcdef01234567".into(),
        workspace_identity_digest: digest_bytes(b"workspace-identity"),
        workspace_digest: digest_bytes(b"workspace"),
        workspace_root: root.to_path_buf(),
        requested_authority_digest: digest_bytes(b"requested-authority"),
        write_scope_digest: digest_bytes(b"write-scope"),
        issued_at: now - chrono::Duration::hours(2),
        expires_at: now - chrono::Duration::hours(1),
        provider_process_allowance: 1,
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

struct FileVerifier {
    path: PathBuf,
    expected: Vec<u8>,
    calls: usize,
}

struct FailAfterMutationBroker {
    inner: LocalEffectBroker,
    executions: Cell<usize>,
}

impl EffectBroker for FailAfterMutationBroker {
    fn authorize(
        &self,
        request: &EffectRequest,
        authority: &AuthorityEnvelope,
    ) -> Result<AuthorizedEffect, PolicyDenial> {
        self.inner.authorize(request, authority)
    }

    fn execute_authorized(
        &self,
        authorized: &AuthorizedEffect,
    ) -> Result<EffectOutput, EffectBrokerError> {
        self.inner.execute_authorized(authorized)?;
        self.executions.set(self.executions.get() + 1);
        Err(std::io::Error::other("interrupted after mutation").into())
    }
}

impl EngineVerifier for FileVerifier {
    fn verify(&mut self, _: &RunRequest) -> VerificationOutcome {
        self.calls += 1;
        let passed = std::fs::read(&self.path).is_ok_and(|bytes| bytes == self.expected);
        VerificationOutcome {
            passed,
            report_digest: digest(if passed { ONE_DIGEST } else { ZERO_DIGEST }),
            summary: if passed { "passed" } else { "failed" }.into(),
        }
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
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
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
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
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
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let engine = DirectEngine::new(&broker);
    let mut effect = EffectRequest {
        effect_id: "effect-denied".into(),
        run_id: "run-01".into(),
        kind: EffectKind::RunProgram,
        program: Some("/usr/bin/printf".into()),
        content: None,
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
fn n7_rg_discovery_is_denied_without_effects_or_verification() {
    let workspace = TempDir::new().expect("workspace");
    let source = workspace.path().join("source.txt");
    std::fs::write(&source, b"unchanged").expect("source fixture");
    let mut request = request(workspace.path());
    request
        .authority
        .capabilities
        .insert(Capability::RunLocalProgram);
    request.authority.allowed_programs.insert("rg".into());
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let engine = DirectEngine::new(&broker);
    let effect = EffectRequest {
        effect_id: "inspect-workspace-files".into(),
        run_id: request.run_id.clone(),
        kind: EffectKind::RunProgram,
        program: Some("rg".into()),
        content: None,
        args: vec!["--files".into()],
        paths: vec![workspace.path().to_path_buf()],
        timeout_ms: 100,
        input_digest: digest(ZERO_DIGEST),
    };
    let mut adapter = ScriptedAdapter::new(
        identity(),
        [Ok(turn(vec![
            AdapterAction::Effect(effect),
            AdapterAction::Verify,
        ]))],
    );
    let mut verifier = ScriptedVerifier::new([verification(true)]);

    let outcome = engine.run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Denied);
    assert_eq!(outcome.failure_code.as_deref(), Some("effect_denied"));
    assert_eq!(verifier.calls, 0);
    assert_eq!(std::fs::read(source).expect("source bytes"), b"unchanged");
}

#[test]
fn ordered_native_writes_complete_before_same_turn_verification() {
    let workspace = TempDir::new().expect("workspace");
    let target = workspace.path().join("product.txt");
    let mut request = request(workspace.path());
    request.limits.max_turns = 1;
    request
        .authority
        .capabilities
        .insert(Capability::WriteWorkspace);
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let engine = DirectEngine::new(&broker);
    let create = EffectRequest {
        effect_id: "create-product".into(),
        run_id: request.run_id.clone(),
        kind: EffectKind::WriteFile,
        program: None,
        args: Vec::new(),
        paths: vec!["product.txt".into()],
        timeout_ms: 0,
        input_digest: digest_bytes(b"ao.next.file-does-not-exist.v1"),
        content: Some("first\n".into()),
    };
    let replace = EffectRequest {
        effect_id: "replace-product".into(),
        input_digest: digest_bytes(b"first\n"),
        content: Some("second\n".into()),
        ..create.clone()
    };
    let mut adapter = ScriptedAdapter::new(
        identity(),
        [Ok(turn(vec![
            AdapterAction::Effect(create),
            AdapterAction::Effect(replace),
            AdapterAction::Verify,
        ]))],
    );
    let mut verifier = FileVerifier {
        path: target.clone(),
        expected: b"second\n".to_vec(),
        calls: 0,
    };

    let outcome = engine.run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Passed);
    assert_eq!(verifier.calls, 1);
    assert_eq!(std::fs::read(target).expect("product bytes"), b"second\n");
}

#[test]
fn denied_or_duplicate_native_write_prevents_verification() {
    let workspace = TempDir::new().expect("workspace");
    let first = workspace.path().join("first.txt");
    let second = workspace.path().join("second.txt");
    let mut request = request(workspace.path());
    request.limits.max_turns = 1;
    request
        .authority
        .capabilities
        .insert(Capability::WriteWorkspace);
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let engine = DirectEngine::new(&broker);
    let create = EffectRequest {
        effect_id: "duplicate-effect".into(),
        run_id: request.run_id.clone(),
        kind: EffectKind::WriteFile,
        program: None,
        args: Vec::new(),
        paths: vec!["first.txt".into()],
        timeout_ms: 0,
        input_digest: digest_bytes(b"ao.next.file-does-not-exist.v1"),
        content: Some("first".into()),
    };
    let duplicate = EffectRequest {
        paths: vec!["second.txt".into()],
        ..create.clone()
    };
    let mut adapter = ScriptedAdapter::new(
        identity(),
        [Ok(turn(vec![
            AdapterAction::Effect(create),
            AdapterAction::Effect(duplicate),
            AdapterAction::Verify,
        ]))],
    );
    let mut verifier = ScriptedVerifier::new([verification(true)]);

    let outcome = engine.run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Denied);
    assert_eq!(outcome.failure_code.as_deref(), Some("duplicate_effect"));
    assert_eq!(verifier.calls, 0);
    assert!(first.is_file());
    assert!(!second.exists());
}

#[test]
fn durable_intent_without_completion_is_unknown_and_never_retried() {
    let workspace = TempDir::new().expect("workspace");
    let recovery = TempDir::new().expect("recovery");
    let target = workspace.path().join("product.txt");
    let mut request = request(workspace.path());
    request.limits.max_turns = 1;
    request
        .authority
        .capabilities
        .insert(Capability::WriteWorkspace);
    let effect = EffectRequest {
        effect_id: "create-product".into(),
        run_id: request.run_id.clone(),
        kind: EffectKind::WriteFile,
        program: None,
        args: Vec::new(),
        paths: vec!["product.txt".into()],
        timeout_ms: 0,
        input_digest: digest_bytes(b"ao.next.file-does-not-exist.v1"),
        content: Some("product\n".into()),
    };
    let broker = FailAfterMutationBroker {
        inner: LocalEffectBroker::new(1_000, 4_096, 4_096),
        executions: Cell::new(0),
    };
    let engine = DirectEngine::new(&broker);
    let journal = CheckpointJournal::new(recovery.path(), 16 * 1024).expect("journal");

    let mut first_adapter = ScriptedAdapter::new(
        identity(),
        [Ok(turn(vec![AdapterAction::Effect(effect.clone())]))],
    );
    let mut first_verifier = ScriptedVerifier::new([]);
    let first = engine.run_durable(&request, &mut first_adapter, &mut first_verifier, &journal);

    assert_eq!(first.terminal_state, RunState::Interrupted);
    assert_eq!(
        first.failure_code.as_deref(),
        Some("effect_completion_unknown")
    );
    assert_eq!(
        std::fs::read(&target).expect("mutated target"),
        b"product\n"
    );
    assert_eq!(broker.executions.get(), 1);

    let mut resumed_adapter =
        ScriptedAdapter::new(identity(), [Ok(turn(vec![AdapterAction::Effect(effect)]))]);
    let mut resumed_verifier = ScriptedVerifier::new([]);
    let resumed = engine.run_durable(
        &request,
        &mut resumed_adapter,
        &mut resumed_verifier,
        &journal,
    );

    assert_eq!(resumed.terminal_state, RunState::Interrupted);
    assert_eq!(
        resumed.failure_code.as_deref(),
        Some("effect_completion_unknown")
    );
    assert_eq!(broker.executions.get(), 1);
}

#[test]
fn n7_rechecks_execution_authority_immediately_before_effect_intent() {
    let workspace = TempDir::new().expect("workspace");
    let recovery = TempDir::new().expect("recovery");
    let target = workspace.path().join("product.txt");
    let mut request = request(workspace.path());
    request.limits.max_turns = 1;
    request
        .authority
        .capabilities
        .insert(Capability::WriteWorkspace);
    let effect = EffectRequest {
        effect_id: "create-product".into(),
        run_id: request.run_id.clone(),
        kind: EffectKind::WriteFile,
        program: None,
        args: Vec::new(),
        paths: vec!["product.txt".into()],
        timeout_ms: 0,
        input_digest: digest_bytes(b"ao.next.file-does-not-exist.v1"),
        content: Some("product\n".into()),
    };
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let journal = CheckpointJournal::new(recovery.path(), 16 * 1024).expect("journal");
    let mut adapter =
        ScriptedAdapter::new(identity(), [Ok(turn(vec![AdapterAction::Effect(effect)]))]);
    let mut verifier = ScriptedVerifier::new([]);

    let outcome = DirectEngine::new(&broker).run_durable_n7(
        &request,
        &mut adapter,
        &mut verifier,
        &journal,
        &expired_n7_execution_authority(workspace.path()),
    );

    assert_eq!(outcome.terminal_state, RunState::Denied);
    assert_eq!(
        outcome.failure_code.as_deref(),
        Some("execution_authority_not_current")
    );
    assert!(!target.exists());
    assert!(matches!(
        journal.effect_state(
            &request,
            &EffectRequest {
                effect_id: "create-product".into(),
                run_id: request.run_id.clone(),
                kind: EffectKind::WriteFile,
                program: None,
                args: Vec::new(),
                paths: vec!["product.txt".into()],
                timeout_ms: 0,
                input_digest: digest_bytes(b"ao.next.file-does-not-exist.v1"),
                content: Some("product\n".into()),
            }
        ),
        Ok(ao_next_core::recovery::JournalEffectState::Fresh)
    ));
}

#[test]
fn durable_completion_is_reused_without_executing_the_effect_again() {
    let workspace = TempDir::new().expect("workspace");
    let recovery = TempDir::new().expect("recovery");
    let target = workspace.path().join("product.txt");
    let mut request = request(workspace.path());
    request.limits.max_turns = 1;
    request
        .authority
        .capabilities
        .insert(Capability::WriteWorkspace);
    let effect = EffectRequest {
        effect_id: "create-product".into(),
        run_id: request.run_id.clone(),
        kind: EffectKind::WriteFile,
        program: None,
        args: Vec::new(),
        paths: vec!["product.txt".into()],
        timeout_ms: 0,
        input_digest: digest_bytes(b"ao.next.file-does-not-exist.v1"),
        content: Some("product\n".into()),
    };
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let engine = DirectEngine::new(&broker);
    let journal = CheckpointJournal::new(recovery.path(), 16 * 1024).expect("journal");

    for _ in 0..2 {
        let mut adapter = ScriptedAdapter::new(
            identity(),
            [Ok(turn(vec![
                AdapterAction::Effect(effect.clone()),
                AdapterAction::Verify,
            ]))],
        );
        let mut verifier = FileVerifier {
            path: target.clone(),
            expected: b"product\n".to_vec(),
            calls: 0,
        };
        let outcome = engine.run_durable(&request, &mut adapter, &mut verifier, &journal);
        assert_eq!(outcome.terminal_state, RunState::Passed);
        assert_eq!(verifier.calls, 1);
    }

    assert_eq!(std::fs::read(target).expect("product"), b"product\n");
}

#[test]
fn durable_provider_turn_is_normalized_before_verification() {
    let workspace = TempDir::new().expect("workspace");
    let recovery = TempDir::new().expect("recovery");
    let request = request(workspace.path());
    let journal = CheckpointJournal::new(recovery.path(), 16 * 1024).expect("journal");
    let prepared = digest_bytes(b"prepared run");
    let invocation = digest_bytes(b"provider invocation");
    let raw = digest_bytes(b"provider output");
    let index = digest_bytes(b"capture index");
    journal
        .record_provider_request_intent(&request, &prepared)
        .expect("provider intent");
    journal
        .record_provider_process_started(&request, &invocation)
        .expect("provider started");
    journal
        .record_provider_output_retained(&request, &raw)
        .expect("provider output retained");
    journal
        .record_provider_capture_published(&request, &index)
        .expect("capture published");
    journal
        .record_provider_capture_verified(&request, &index)
        .expect("capture verified");
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let mut adapter = ScriptedAdapter::new(identity(), [Ok(turn(vec![AdapterAction::Verify]))]);
    let mut verifier = ScriptedVerifier::new([verification(true)]);

    let outcome =
        DirectEngine::new(&broker).run_durable(&request, &mut adapter, &mut verifier, &journal);

    assert_eq!(outcome.terminal_state, RunState::Passed);
    let state = journal.provider_state(&request).expect("provider state");
    assert!(state.adapter_turn_digest.is_some());
}

#[test]
fn durable_provider_turn_journal_failure_stops_before_verification() {
    let workspace = TempDir::new().expect("workspace");
    let recovery = TempDir::new().expect("recovery");
    let request = request(workspace.path());
    let journal = CheckpointJournal::new(recovery.path(), 16 * 1024).expect("journal");
    let index = digest_bytes(b"capture index");
    journal
        .record_provider_request_intent(&request, &digest_bytes(b"prepared run"))
        .expect("provider intent");
    journal
        .record_provider_process_started(&request, &digest_bytes(b"provider invocation"))
        .expect("provider started");
    journal
        .record_provider_output_retained(&request, &digest_bytes(b"provider output"))
        .expect("provider output retained");
    journal
        .record_provider_capture_published(&request, &index)
        .expect("capture published");
    journal
        .record_provider_capture_verified(&request, &index)
        .expect("capture verified");
    std::fs::write(
        recovery.path().join("execution-events/invalid.json"),
        b"invalid",
    )
    .expect("invalid journal event");
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let mut adapter = ScriptedAdapter::new(identity(), [Ok(turn(vec![AdapterAction::Verify]))]);
    let mut verifier = ScriptedVerifier::new([verification(true)]);

    let outcome =
        DirectEngine::new(&broker).run_durable(&request, &mut adapter, &mut verifier, &journal);

    assert_eq!(outcome.terminal_state, RunState::Failed);
    assert_eq!(outcome.failure_code.as_deref(), Some("journal_failure"));
    assert_eq!(verifier.calls, 0);
}

#[test]
fn admitted_native_read_is_observed_before_the_same_worker_continues() {
    let workspace = TempDir::new().expect("workspace");
    let source = workspace.path().join("source.txt");
    std::fs::write(&source, b"visible source\n").expect("source fixture");
    let mut request = request(workspace.path());
    request
        .authority
        .capabilities
        .insert(Capability::ReadWorkspace);
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let engine = DirectEngine::new(&broker);
    let effect = EffectRequest {
        effect_id: "effect-executed".into(),
        run_id: request.run_id.clone(),
        kind: EffectKind::ReadFile,
        program: None,
        content: None,
        args: Vec::new(),
        paths: vec!["source.txt".into()],
        timeout_ms: 0,
        input_digest: digest_bytes(b"visible source\n"),
    };
    let mut adapter = ScriptedAdapter::new(
        identity(),
        [
            Ok(turn(vec![AdapterAction::Effect(effect)])),
            Ok(turn(vec![AdapterAction::Verify])),
        ],
    );
    let mut verifier = ScriptedVerifier::new([verification(true)]);

    let outcome = engine.run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Passed);
    assert_eq!(adapter.contexts().len(), 2);
    let observations = &adapter.contexts()[1].effect_observations;
    assert_eq!(observations.len(), 1);
    assert_eq!(observations[0].effect_id, "effect-executed");
    assert_eq!(observations[0].status, 0);
    assert_eq!(observations[0].stdout, b"visible source\n");
    assert!(observations[0].stderr.is_empty());
    let serialized = serde_json::to_value(&outcome).expect("outcome JSON");
    assert_eq!(
        serialized["effect_observations"][0]["effect_id"],
        "effect-executed"
    );
    assert_eq!(
        serialized["effect_observations"][0]["output_digest"],
        observations[0].output_digest.as_str()
    );
    assert!(outcome.events.iter().any(|event| matches!(
        &event.kind,
        ao_next_core::engine::EngineEventKind::EffectCompleted(observation)
            if observation.effect_id == "effect-executed"
    )));
}

#[test]
fn missing_parent_write_is_denied_before_verification() {
    let workspace = TempDir::new().expect("workspace");
    let mut request = request(workspace.path());
    request
        .authority
        .capabilities
        .insert(Capability::WriteWorkspace);
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let engine = DirectEngine::new(&broker);
    let effect = EffectRequest {
        effect_id: "effect-missing-parent".into(),
        run_id: request.run_id.clone(),
        kind: EffectKind::WriteFile,
        program: None,
        content: Some("product".into()),
        args: Vec::new(),
        paths: vec!["missing/product.txt".into()],
        timeout_ms: 0,
        input_digest: digest_bytes(b"ao.next.file-does-not-exist.v1"),
    };
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
    assert_eq!(outcome.failure_code.as_deref(), Some("effect_denied"));
    assert_eq!(outcome.metrics.turns, 1);
    assert_eq!(verifier.calls, 0);
}

#[test]
fn adapter_failure_and_interrupt_have_distinct_terminal_states() {
    let workspace = TempDir::new().expect("workspace");
    let request = request(workspace.path());
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
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
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
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
        let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
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
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
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
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
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
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let engine = DirectEngine::new(&broker);
    let mut adapter = ScriptedAdapter::new(identity(), [Ok(turn(vec![AdapterAction::Verify]))]);
    let mut verifier = ScriptedVerifier::new([verification(true)]);

    let outcome = engine.run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Interrupted);
    assert_eq!(outcome.failure_code.as_deref(), Some("time_limit"));
    assert_eq!(outcome.metrics.turns, 0);
    assert_eq!(verifier.calls, 0);
}
