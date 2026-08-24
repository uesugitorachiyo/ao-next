use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

use ao_next_core::adapter::AdapterIdentity;
use ao_next_core::contracts::{
    AuthorityEnvelope, Capability, Digest, EffectKind, EffectRequest, ExternalEffectPolicy,
    ModelProfile, NetworkPolicy, RunLimits, RunRequest, SourceIdentity, StructuredCommand,
    VerifierProfile, WorkspaceIdentity,
};
use ao_next_core::evidence::{
    ArtifactSpec, ArtifactStore, EvidenceError, StoreLimits, digest_bytes, seal_verified_run,
    verify_evidence, verify_sealed_run,
};
use ao_next_core::recovery::{
    Checkpoint, CheckpointIdentity, CheckpointJournal, JournalEvent, JournalEventKind,
    RecoveryError, write_durable_event_log,
};
use ao_next_core::strict_json::{canonical_digest, canonical_json_bytes, decode_strict_json};
use ao_next_core::verifier::{
    LocalProductVerifier, ProductVerifier, VerificationPlan, VerifiedWorkspace, VerifierRegistry,
};
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

fn passing_command() -> StructuredCommand {
    #[cfg(unix)]
    let (program, args) = ("/usr/bin/true".into(), Vec::new());
    #[cfg(windows)]
    let (program, args) = (
        std::env::var("ComSpec").expect("Windows command interpreter"),
        vec!["/D".into(), "/C".into(), "exit 0".into()],
    );
    StructuredCommand {
        program,
        args,
        timeout_ms: 1_000,
    }
}

fn request(root: &Path) -> RunRequest {
    let verifier_command = passing_command();
    RunRequest {
        schema_version: "ao.next.run-request.v1".into(),
        run_id: "run-evidence-01".into(),
        objective: "Verify and retain local evidence".into(),
        source: SourceIdentity {
            repository: "fixture".into(),
            head: digest(ZERO_DIGEST),
        },
        workspace: WorkspaceIdentity {
            workspace_id: "workspace-evidence-01".into(),
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
            capabilities: BTreeSet::from([Capability::RunLocalProgram]),
            allowed_roots: vec![root.to_path_buf()],
            allowed_programs: BTreeSet::from([verifier_command.program.clone()]),
            network: NetworkPolicy::Denied,
            allowed_network_hosts: BTreeSet::new(),
            external_effects: ExternalEffectPolicy::Denied,
        },
        verifier_profile: VerifierProfile {
            profile_id: "complete-local".into(),
            profile_digest: digest(ONE_DIGEST),
            commands: vec![verifier_command],
            required_artifacts: vec![PathBuf::from("result.txt")],
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

struct Fixture {
    recovery: TempDir,
    request: RunRequest,
    journal: CheckpointJournal,
}

fn fixture() -> Fixture {
    let recovery = TempDir::new().expect("recovery");
    let request = request(recovery.path());
    let journal = CheckpointJournal::new(recovery.path().join("journal"), 16 * 1024)
        .expect("execution journal");
    Fixture {
        recovery,
        request,
        journal,
    }
}

fn effect(request: &RunRequest) -> EffectRequest {
    EffectRequest {
        effect_id: "effect-01".into(),
        run_id: request.run_id.clone(),
        kind: EffectKind::WriteFile,
        program: None,
        args: Vec::new(),
        paths: vec!["product.txt".into()],
        timeout_ms: 0,
        input_digest: digest_bytes(b"ao.next.file-does-not-exist.v1"),
        content: Some("product\n".into()),
    }
}

fn adapter_identity() -> AdapterIdentity {
    AdapterIdentity {
        runtime: "scripted".into(),
        model_identifier: "fixture-model".into(),
        adapter_version: "scripted-v1".into(),
        worker_id: "worker-01".into(),
    }
}

fn verification_plan(json_digest: Digest) -> VerificationPlan {
    VerificationPlan {
        commands: vec![passing_command()],
        required_files: vec![PathBuf::from("result.txt")],
        strict_json_files: vec![PathBuf::from("result.json")],
        digest_expectations: BTreeMap::from([(PathBuf::from("result.json"), json_digest)]),
        max_file_bytes: 4_096,
    }
}

#[test]
fn command_file_json_and_digest_verifiers_produce_one_bound_report() {
    let workspace = TempDir::new().expect("workspace");
    std::fs::write(workspace.path().join("result.txt"), b"verified\n").expect("result file");
    let json_bytes = br#"{"answer":42}"#;
    std::fs::write(workspace.path().join("result.json"), json_bytes).expect("json file");
    let json_digest = ao_next_core::evidence::digest_bytes(json_bytes);
    let plan = verification_plan(json_digest);
    let mut registry = VerifierRegistry::new();
    let profile_digest = registry
        .register("complete-local", plan)
        .expect("register plan");
    let mut request = request(workspace.path());
    request.verifier_profile.profile_digest = profile_digest;
    let verified_workspace = VerifiedWorkspace::new(workspace.path(), &[workspace.path().into()])
        .expect("verified workspace");
    let verifier = LocalProductVerifier::new(
        &request.run_id,
        &registry,
        timestamp("2026-08-05T12:00:00Z"),
    );

    let report = verifier
        .verify(&verified_workspace, &request.verifier_profile)
        .expect("verification report");

    assert!(report.passed);
    assert_eq!(report.results.len(), 4);
    assert!(report.results.iter().all(|result| result.passed));
    assert_eq!(
        report.verifier_profile_digest,
        request.verifier_profile.profile_digest
    );
}

#[test]
fn missing_or_altered_verifier_inputs_cannot_pass() {
    let workspace = TempDir::new().expect("workspace");
    let json_bytes = br#"{"answer":41}"#;
    std::fs::write(workspace.path().join("result.json"), json_bytes).expect("json file");
    let plan = verification_plan(digest(ZERO_DIGEST));
    let mut registry = VerifierRegistry::new();
    let profile_digest = registry
        .register("complete-local", plan)
        .expect("register plan");
    let mut request = request(workspace.path());
    request.verifier_profile.profile_digest = profile_digest;
    let verified_workspace = VerifiedWorkspace::new(workspace.path(), &[workspace.path().into()])
        .expect("verified workspace");
    let verifier = LocalProductVerifier::new(
        &request.run_id,
        &registry,
        timestamp("2026-08-05T12:00:00Z"),
    );

    let report = verifier
        .verify(&verified_workspace, &request.verifier_profile)
        .expect("verification report");

    assert!(!report.passed);
    assert!(
        report
            .results
            .iter()
            .any(|result| { result.verifier_id == "file:result.txt" && !result.passed })
    );
    assert!(
        report
            .results
            .iter()
            .any(|result| { result.verifier_id == "digest:result.json" && !result.passed })
    );
}

#[test]
fn artifact_store_is_content_addressed_idempotent_and_preserves_original_ref() {
    let workspace = TempDir::new().expect("workspace");
    let evidence = TempDir::new().expect("evidence");
    let artifact = workspace.path().join("artifact.txt");
    std::fs::write(&artifact, b"artifact bytes").expect("artifact fixture");
    let store = ArtifactStore::new(
        evidence.path(),
        vec![workspace.path().to_path_buf()],
        StoreLimits {
            max_artifact_bytes: 1_024,
            max_total_bytes: 4_096,
        },
    )
    .expect("artifact store");
    let spec = ArtifactSpec {
        artifact_id: "artifact".into(),
        path: artifact,
        original_ref: "workspace/artifact.txt".into(),
        media_type: "text/plain".into(),
        producer: "fixture".into(),
        input_digests: vec![digest(ZERO_DIGEST)],
    };

    let first = store.retain(&spec).expect("first retain");
    let second = store.retain(&spec).expect("idempotent retain");

    assert_eq!(first, second);
    assert_eq!(first.original_ref, "workspace/artifact.txt");
    assert_eq!(
        first.content_ref,
        format!("artifacts/sha256/{}", &first.digest.as_str()[7..])
    );
    assert_eq!(
        std::fs::read(evidence.path().join(&first.content_ref)).expect("retained bytes"),
        b"artifact bytes"
    );
}

#[test]
fn evidence_rejects_outside_oversized_symlink_non_regular_and_altered_artifacts() {
    let workspace = TempDir::new().expect("workspace");
    let outside = TempDir::new().expect("outside");
    let evidence = TempDir::new().expect("evidence");
    let store = ArtifactStore::new(
        evidence.path(),
        vec![workspace.path().to_path_buf()],
        StoreLimits {
            max_artifact_bytes: 4,
            max_total_bytes: 8,
        },
    )
    .expect("artifact store");
    let make_spec = |path: PathBuf| ArtifactSpec {
        artifact_id: "negative".into(),
        path,
        original_ref: "negative".into(),
        media_type: "application/octet-stream".into(),
        producer: "fixture".into(),
        input_digests: Vec::new(),
    };

    let outside_file = outside.path().join("outside");
    std::fs::write(&outside_file, b"x").expect("outside fixture");
    assert!(matches!(
        store.retain(&make_spec(outside_file.clone())),
        Err(EvidenceError::PathOutsideAllowedRoots(_))
    ));

    let oversized = workspace.path().join("oversized");
    std::fs::write(&oversized, b"12345").expect("oversized fixture");
    assert!(matches!(
        store.retain(&make_spec(oversized)),
        Err(EvidenceError::ArtifactOversized { .. })
    ));

    assert!(matches!(
        store.retain(&make_spec(workspace.path().to_path_buf())),
        Err(EvidenceError::NonRegularFile(_))
    ));

    #[cfg(unix)]
    {
        let link = workspace.path().join("link");
        std::os::unix::fs::symlink(&outside_file, &link).expect("symlink fixture");
        assert!(matches!(
            store.retain(&make_spec(link)),
            Err(EvidenceError::SymlinkNotAllowed(_))
        ));
    }

    let valid = workspace.path().join("valid");
    std::fs::write(&valid, b"good").expect("valid fixture");
    let entry = store.retain(&make_spec(valid)).expect("retain valid");
    let manifest = ao_next_core::contracts::ArtifactManifest {
        schema_version: "ao.next.artifact-manifest.v1".into(),
        run_id: "run-evidence-01".into(),
        source: request(workspace.path()).source,
        entries: vec![entry.clone()],
    };
    std::fs::write(evidence.path().join(&entry.content_ref), b"evil").expect("alter retained");
    assert!(matches!(
        verify_evidence(evidence.path(), &manifest, 8),
        Err(EvidenceError::DigestMismatch { .. })
    ));
}

#[test]
fn passed_verification_without_retained_evidence_produces_no_terminal_readback() {
    let workspace = TempDir::new().expect("workspace");
    let evidence = TempDir::new().expect("evidence");
    let request = request(workspace.path());
    let store = ArtifactStore::new(
        evidence.path(),
        vec![workspace.path().to_path_buf()],
        StoreLimits {
            max_artifact_bytes: 4,
            max_total_bytes: 8,
        },
    )
    .expect("artifact store");
    let report = ao_next_core::contracts::VerifierReport {
        schema_version: "ao.next.verifier-report.v1".into(),
        run_id: request.run_id.clone(),
        verifier_profile_digest: request.verifier_profile.profile_digest.clone(),
        started_at: timestamp("2026-08-05T12:00:00Z"),
        completed_at: timestamp("2026-08-05T12:00:00Z"),
        passed: true,
        results: Vec::new(),
    };
    let oversized = workspace.path().join("oversized");
    std::fs::write(&oversized, b"12345").expect("oversized fixture");
    let spec = ArtifactSpec {
        artifact_id: "oversized".into(),
        path: oversized,
        original_ref: "oversized".into(),
        media_type: "text/plain".into(),
        producer: "fixture".into(),
        input_digests: Vec::new(),
    };

    let result = seal_verified_run(
        &request,
        &adapter_identity(),
        &report,
        &store,
        &[spec],
        timestamp("2026-08-05T12:00:01Z"),
    );

    assert!(matches!(
        result,
        Err(EvidenceError::ArtifactOversized { .. })
    ));
    assert!(!evidence.path().join("terminal-readback.json").exists());
}

#[test]
fn sealed_run_audit_rejects_altered_verifier_and_terminal_digests() {
    let workspace = TempDir::new().expect("workspace");
    let evidence = TempDir::new().expect("evidence");
    let artifact = workspace.path().join("artifact.txt");
    std::fs::write(&artifact, b"ok").expect("artifact fixture");
    let request = request(workspace.path());
    let store = ArtifactStore::new(
        evidence.path(),
        vec![workspace.path().to_path_buf()],
        StoreLimits {
            max_artifact_bytes: 1_024,
            max_total_bytes: 4_096,
        },
    )
    .expect("artifact store");
    let report = ao_next_core::contracts::VerifierReport {
        schema_version: "ao.next.verifier-report.v1".into(),
        run_id: request.run_id.clone(),
        verifier_profile_digest: request.verifier_profile.profile_digest.clone(),
        started_at: timestamp("2026-08-05T12:00:00Z"),
        completed_at: timestamp("2026-08-05T12:00:00Z"),
        passed: true,
        results: Vec::new(),
    };
    let spec = ArtifactSpec {
        artifact_id: "artifact".into(),
        path: artifact,
        original_ref: "artifact.txt".into(),
        media_type: "text/plain".into(),
        producer: "fixture".into(),
        input_digests: Vec::new(),
    };
    seal_verified_run(
        &request,
        &adapter_identity(),
        &report,
        &store,
        std::slice::from_ref(&spec),
        timestamp("2026-08-05T12:00:01Z"),
    )
    .expect("seal valid run");
    verify_sealed_run(evidence.path(), &request, 4_096).expect("valid sealed run");

    let verifier_path = evidence.path().join("verifier-report.json");
    let mut verifier_value: serde_json::Value = decode_strict_json(
        &std::fs::read(&verifier_path).expect("verifier bytes"),
        4_096,
    )
    .expect("verifier JSON");
    verifier_value["completed_at"] = serde_json::json!("2026-08-05T12:00:02Z");
    std::fs::write(
        &verifier_path,
        serde_json::to_vec(&verifier_value).expect("alter verifier"),
    )
    .expect("write altered verifier");
    assert!(matches!(
        verify_sealed_run(evidence.path(), &request, 4_096),
        Err(EvidenceError::DigestMismatch { .. })
    ));

    seal_verified_run(
        &request,
        &adapter_identity(),
        &report,
        &store,
        &[spec],
        timestamp("2026-08-05T12:00:01Z"),
    )
    .expect("reseal valid run");
    let terminal_path = evidence.path().join("terminal-readback.json");
    let mut terminal_value: serde_json::Value = decode_strict_json(
        &std::fs::read(&terminal_path).expect("terminal bytes"),
        4_096,
    )
    .expect("terminal JSON");
    terminal_value["verifier_report_digest"] = serde_json::json!(ZERO_DIGEST);
    std::fs::write(
        &terminal_path,
        serde_json::to_vec(&terminal_value).expect("alter terminal"),
    )
    .expect("write altered terminal");
    assert!(matches!(
        verify_sealed_run(evidence.path(), &request, 4_096),
        Err(EvidenceError::DigestMismatch { .. })
    ));
}

fn recovery_events() -> Vec<JournalEvent> {
    vec![
        JournalEvent {
            schema_version: "ao.next.journal-event.v1".into(),
            sequence: 0,
            kind: JournalEventKind::EffectCommitted {
                effect_id: "effect-01".into(),
            },
        },
        JournalEvent {
            schema_version: "ao.next.journal-event.v1".into(),
            sequence: 1,
            kind: JournalEventKind::VerificationStarted { attempt: 0 },
        },
        JournalEvent {
            schema_version: "ao.next.journal-event.v1".into(),
            sequence: 2,
            kind: JournalEventKind::VerifierRecorded {
                report_digest: digest(ONE_DIGEST),
            },
        },
    ]
}

fn journal_event(sequence: u64, kind: JournalEventKind) -> JournalEvent {
    JournalEvent {
        schema_version: "ao.next.journal-event.v1".into(),
        sequence,
        kind,
    }
}

fn provider_lifecycle_events() -> Vec<JournalEvent> {
    vec![
        journal_event(
            0,
            JournalEventKind::ProviderRequestIntent {
                prepared_run_digest: digest_bytes(b"prepared"),
                execution_authority_digest: digest_bytes(b"authority"),
            },
        ),
        journal_event(
            1,
            JournalEventKind::ProviderProcessStarted {
                invocation_digest: digest_bytes(b"invocation"),
            },
        ),
        journal_event(
            2,
            JournalEventKind::ProviderOutputRetained {
                raw_capture_digest: digest_bytes(b"raw-capture"),
            },
        ),
        journal_event(
            3,
            JournalEventKind::ProviderCaptureIndexPublished {
                index_digest: digest_bytes(b"capture-index"),
            },
        ),
        journal_event(
            4,
            JournalEventKind::ProviderCaptureVerified {
                index_digest: digest_bytes(b"capture-index"),
            },
        ),
        journal_event(
            5,
            JournalEventKind::AdapterTurnNormalized {
                turn_digest: digest_bytes(b"adapter-turn"),
            },
        ),
    ]
}

fn checkpoint_for_events(request: &RunRequest, sequence: u64, events_digest: Digest) -> Checkpoint {
    Checkpoint {
        schema_version: "ao.next.checkpoint.v1".into(),
        run_id: request.run_id.clone(),
        sequence,
        identity: CheckpointIdentity::from_request(request).expect("checkpoint identity"),
        committed_effects: BTreeSet::new(),
        events_digest,
        recorded_at: timestamp("2026-08-05T12:00:02Z"),
    }
}

#[test]
fn interrupted_n7_resume_skips_committed_effect_and_requires_durable_verifier_events() {
    let workspace = TempDir::new().expect("workspace");
    let recovery = TempDir::new().expect("recovery");
    let mut request = request(workspace.path());
    request.run_id = "run-n7-recovery-01".into();
    request.model_profile.runtime = "codex".into();
    request.model_profile.model_identifier = "fixed-live-model".into();
    request.model_profile.adapter_version = "ao-next-process-v1".into();
    let identity = CheckpointIdentity::from_request(&request).expect("checkpoint identity");
    let events_path = recovery.path().join("events.jsonl");
    let events_digest =
        write_durable_event_log(&events_path, &recovery_events(), 4_096).expect("durable events");
    let checkpoint = Checkpoint {
        schema_version: "ao.next.checkpoint.v1".into(),
        run_id: request.run_id.clone(),
        sequence: 3,
        identity: identity.clone(),
        committed_effects: BTreeSet::from(["effect-01".into()]),
        events_digest,
        recorded_at: timestamp("2026-08-05T12:00:02Z"),
    };
    let journal = CheckpointJournal::new(recovery.path(), 4_096).expect("journal");
    journal
        .commit(&checkpoint, &events_path)
        .expect("checkpoint commit");

    let plan = journal
        .resume(&identity, &["effect-01".into(), "effect-02".into()])
        .expect("resume plan");

    assert_eq!(plan.skipped_committed_effects, vec!["effect-01"]);
    assert_eq!(plan.remaining_effects, vec!["effect-02"]);

    let incomplete_path = recovery.path().join("incomplete.jsonl");
    write_durable_event_log(&incomplete_path, &recovery_events()[..1], 4_096)
        .expect("incomplete durable events");
    let mut incomplete = checkpoint;
    incomplete.events_digest =
        ao_next_core::evidence::digest_file(&incomplete_path, 4_096).expect("incomplete digest");
    assert!(matches!(
        journal.commit(&incomplete, &incomplete_path),
        Err(RecoveryError::VerifierEventMissing)
    ));
}

#[test]
fn recovery_rejects_every_changed_identity_and_checkpoint_digest() {
    let workspace = TempDir::new().expect("workspace");
    let recovery = TempDir::new().expect("recovery");
    let request = request(workspace.path());
    let identity = CheckpointIdentity::from_request(&request).expect("checkpoint identity");
    let events_path = recovery.path().join("events.jsonl");
    let events_digest =
        write_durable_event_log(&events_path, &recovery_events(), 4_096).expect("durable events");
    let checkpoint = Checkpoint {
        schema_version: "ao.next.checkpoint.v1".into(),
        run_id: request.run_id.clone(),
        sequence: 3,
        identity: identity.clone(),
        committed_effects: BTreeSet::from(["effect-01".into()]),
        events_digest,
        recorded_at: timestamp("2026-08-05T12:00:02Z"),
    };
    let journal = CheckpointJournal::new(recovery.path(), 4_096).expect("journal");
    journal
        .commit(&checkpoint, &events_path)
        .expect("checkpoint commit");

    let mut changed_identities = Vec::new();
    for field in 0..6 {
        let mut changed = identity.clone();
        match field {
            0 => changed.request_digest = digest(ONE_DIGEST),
            1 => changed.source_digest = digest(ONE_DIGEST),
            2 => changed.workspace_digest = digest(ZERO_DIGEST),
            3 => changed.policy_digest = digest(ONE_DIGEST),
            4 => changed.model_profile_digest = digest(ZERO_DIGEST),
            5 => changed.verifier_profile_digest = digest(ZERO_DIGEST),
            _ => unreachable!(),
        }
        changed_identities.push(changed);
    }
    for changed in changed_identities {
        assert!(matches!(
            journal.resume(&changed, &["effect-01".into()]),
            Err(RecoveryError::IdentityMismatch)
        ));
    }

    let checkpoint_path = recovery.path().join("checkpoint.json");
    let mut value: serde_json::Value = decode_strict_json(
        &std::fs::read(&checkpoint_path).expect("checkpoint bytes"),
        4_096,
    )
    .expect("checkpoint JSON");
    value["sequence"] = serde_json::json!(99);
    std::fs::write(
        &checkpoint_path,
        serde_json::to_vec(&value).expect("altered checkpoint"),
    )
    .expect("write altered checkpoint");
    assert!(matches!(
        journal.resume(&identity, &["effect-01".into()]),
        Err(RecoveryError::CheckpointDigestMismatch { .. })
    ));
}

#[test]
fn provider_capture_events_are_ordered_before_effect_intent() {
    let fixture = fixture();
    let prepared = digest_bytes(b"prepared");
    let invocation = digest_bytes(b"invocation");
    let raw = digest_bytes(b"raw-capture");
    let index = digest_bytes(b"capture-index");
    let turn = digest_bytes(b"adapter-turn");

    fixture
        .journal
        .record_provider_request_intent(&fixture.request, &prepared, &digest_bytes(b"authority"))
        .expect("intent");
    fixture
        .journal
        .record_provider_process_started(&fixture.request, &invocation)
        .expect("started");
    fixture
        .journal
        .record_provider_output_retained(&fixture.request, &raw)
        .expect("retained");
    fixture
        .journal
        .record_provider_capture_published(&fixture.request, &index)
        .expect("published");
    fixture
        .journal
        .record_provider_capture_verified(&fixture.request, &index)
        .expect("verified");
    fixture
        .journal
        .record_adapter_turn_normalized(&fixture.request, &turn)
        .expect("normalized");

    let state = fixture
        .journal
        .provider_state(&fixture.request)
        .expect("state");
    assert_eq!(state.prepared_run_digest, Some(prepared));
    assert_eq!(state.invocation_digest, Some(invocation));
    assert_eq!(state.capture_index_digest, Some(index));
    assert_eq!(state.adapter_turn_digest, Some(turn));
    assert!(state.provider_process_started);
}

#[test]
fn provider_intent_binds_execution_authority() {
    let fixture = fixture();
    let prepared_digest = digest_bytes(b"prepared");
    let authority = serde_json::json!({"authority_id": "n7-authority-01"});
    let authority_digest = canonical_digest(&authority).expect("authority digest");

    fixture
        .journal
        .record_provider_request_intent(&fixture.request, &prepared_digest, &authority_digest)
        .expect("provider intent");

    assert_eq!(
        fixture
            .journal
            .provider_state(&fixture.request)
            .expect("provider state")
            .execution_authority_digest,
        Some(authority_digest),
    );
}

#[test]
fn provider_intent_execution_authority_digest_mutation_fails_closed() {
    let fixture = fixture();
    fixture
        .journal
        .record_provider_request_intent(
            &fixture.request,
            &digest_bytes(b"prepared"),
            &digest_bytes(b"authority"),
        )
        .expect("provider intent");
    let event_path = std::fs::read_dir(fixture.recovery.path().join("journal/execution-events"))
        .expect("execution events")
        .next()
        .expect("provider event")
        .expect("event entry")
        .path();
    let mut event: serde_json::Value = decode_strict_json(
        &std::fs::read(&event_path).expect("provider event bytes"),
        4_096,
    )
    .expect("provider event JSON");
    event["kind"]["execution_authority_digest"] =
        serde_json::json!(digest_bytes(b"substituted-authority"));
    std::fs::write(
        &event_path,
        canonical_json_bytes(&event).expect("mutated provider event"),
    )
    .expect("mutated provider event write");

    assert!(matches!(
        fixture.journal.provider_state(&fixture.request),
        Err(RecoveryError::EventDigestMismatch)
    ));
}

#[test]
fn read_only_provider_state_rejects_missing_journal_without_creation() {
    let recovery = TempDir::new().expect("recovery");
    let request = request(recovery.path());
    let journal_root = recovery.path().join("missing-journal");

    assert!(
        CheckpointJournal::open_bound(&journal_root, 16 * 1024, &request).is_err(),
        "missing journal opened as request-bound"
    );
    assert!(
        !journal_root.exists(),
        "read-only open created journal root"
    );
}

#[test]
fn read_only_provider_state_rejects_missing_events_without_creation() {
    let recovery = TempDir::new().expect("recovery");
    let request = request(recovery.path());
    let journal_root = recovery.path().join("journal");
    let journal = CheckpointJournal::new(&journal_root, 16 * 1024).expect("journal");
    journal.bind_request(&request).expect("request binding");
    assert!(CheckpointJournal::open_bound(&journal_root, 16 * 1024, &request).is_err());
    assert!(
        !journal_root.join("execution-events").exists(),
        "read-only provider state created event directory"
    );
}

#[test]
fn read_only_provider_state_rejects_tampered_identity_without_rewrite() {
    let fixture = fixture();
    let identity_path = fixture
        .recovery
        .path()
        .join("journal/execution-identity.json");
    std::fs::write(&identity_path, b"{}").expect("tampered identity");

    assert!(
        CheckpointJournal::open_bound(
            fixture.recovery.path().join("journal"),
            16 * 1024,
            &fixture.request,
        )
        .is_err()
    );
    assert_eq!(std::fs::read(identity_path).expect("identity bytes"), b"{}");
}

fn record_complete_provider_lifecycle(fixture: &Fixture) {
    let index = digest_bytes(b"capture-index");
    fixture
        .journal
        .record_provider_request_intent(
            &fixture.request,
            &digest_bytes(b"prepared"),
            &digest_bytes(b"authority"),
        )
        .expect("provider intent");
    fixture
        .journal
        .record_provider_process_started(&fixture.request, &digest_bytes(b"invocation"))
        .expect("provider start");
    fixture
        .journal
        .record_provider_output_retained(&fixture.request, &digest_bytes(b"raw"))
        .expect("provider output");
    fixture
        .journal
        .record_provider_capture_published(&fixture.request, &index)
        .expect("capture published");
    fixture
        .journal
        .record_provider_capture_verified(&fixture.request, &index)
        .expect("capture verified");
    fixture
        .journal
        .record_adapter_turn_normalized(&fixture.request, &digest_bytes(b"turn"))
        .expect("turn normalized");
}

#[cfg(windows)]
fn mutation_completed(result: std::io::Result<()>, action: &str) -> bool {
    match result {
        Ok(()) => true,
        Err(error)
            if error.kind() == std::io::ErrorKind::PermissionDenied
                || matches!(error.raw_os_error(), Some(5 | 32)) =>
        {
            false
        }
        Err(error) => panic!("{action}: {error}"),
    }
}

#[cfg(not(windows))]
fn mutation_completed(result: std::io::Result<()>, action: &str) -> bool {
    result.unwrap_or_else(|error| panic!("{action}: {error}"));
    true
}

#[test]
fn existing_only_journal_rejects_same_byte_identity_swap_before_terminal_append() {
    let fixture = fixture();
    record_complete_provider_lifecycle(&fixture);
    let journal_root = fixture.recovery.path().join("journal");
    let journal =
        CheckpointJournal::open_bound(&journal_root, 16 * 1024, &fixture.request).expect("journal");
    journal
        .begin_verification(&fixture.request)
        .expect("verification start");
    journal
        .record_verifier(&fixture.request, &digest_bytes(b"report"))
        .expect("verifier record");
    let identity_path = journal_root.join("execution-identity.json");
    let identity_bytes = std::fs::read(&identity_path).expect("identity bytes");
    if !mutation_completed(std::fs::remove_file(&identity_path), "remove identity") {
        return;
    }
    std::fs::write(&identity_path, identity_bytes).expect("replacement identity");
    let events_before = std::fs::read_dir(journal_root.join("execution-events"))
        .expect("execution events")
        .count();

    assert!(
        journal
            .publish_terminal_record(&fixture.request, br#"{"terminal":"passed"}"#)
            .is_err(),
        "replacement identity authorized terminal publication"
    );
    assert_eq!(
        std::fs::read_dir(journal_root.join("execution-events"))
            .expect("execution events")
            .count(),
        events_before
    );
    assert!(
        std::fs::read_dir(&journal_root)
            .expect("journal root")
            .all(|entry| !entry
                .expect("journal entry")
                .file_name()
                .to_string_lossy()
                .starts_with("terminal-"))
    );
}

#[test]
fn existing_only_journal_does_not_recreate_deleted_event_directory() {
    let fixture = fixture();
    record_complete_provider_lifecycle(&fixture);
    let journal_root = fixture.recovery.path().join("journal");
    let journal =
        CheckpointJournal::open_bound(&journal_root, 16 * 1024, &fixture.request).expect("journal");
    let events = journal_root.join("execution-events");
    if !mutation_completed(std::fs::remove_dir_all(&events), "remove execution events") {
        return;
    }

    assert!(journal.begin_verification(&fixture.request).is_err());
    assert!(!events.exists(), "existing-only journal recreated events");
}

#[test]
fn existing_only_journal_rejects_same_byte_event_directory_swap() {
    let fixture = fixture();
    record_complete_provider_lifecycle(&fixture);
    let journal_root = fixture.recovery.path().join("journal");
    let journal =
        CheckpointJournal::open_bound(&journal_root, 16 * 1024, &fixture.request).expect("journal");
    let events = journal_root.join("execution-events");
    let replacement = journal_root.join("replacement-events");
    std::fs::create_dir(&replacement).expect("replacement directory");
    for entry in std::fs::read_dir(&events).expect("execution events") {
        let entry = entry.expect("event entry");
        std::fs::copy(entry.path(), replacement.join(entry.file_name())).expect("copy event");
    }
    let original = journal_root.join("original-events");
    if !mutation_completed(std::fs::rename(&events, &original), "move original events") {
        return;
    }
    std::fs::rename(&replacement, &events).expect("install replacement events");

    assert!(
        journal.provider_state_read_only(&fixture.request).is_err(),
        "replacement event directory retained journal authority"
    );
}

#[test]
fn existing_only_journal_rejects_deleted_bound_event_file() {
    let fixture = fixture();
    record_complete_provider_lifecycle(&fixture);
    let journal_root = fixture.recovery.path().join("journal");
    let journal =
        CheckpointJournal::open_bound(&journal_root, 16 * 1024, &fixture.request).expect("journal");
    let event = std::fs::read_dir(journal_root.join("execution-events"))
        .expect("execution events")
        .map(|entry| entry.expect("event entry").path())
        .max()
        .expect("last event");
    if !mutation_completed(std::fs::remove_file(&event), "remove event") {
        return;
    }

    assert!(
        journal.provider_state_read_only(&fixture.request).is_err(),
        "shorter event prefix retained journal authority"
    );
    assert!(!event.exists(), "existing-only journal recreated event");
}

#[cfg(windows)]
#[test]
fn open_bound_rejects_windows_reparse_journal_paths() {
    let root_fixture = fixture();
    record_complete_provider_lifecycle(&root_fixture);
    let link = root_fixture.recovery.path().join("journal-link");
    match std::os::windows::fs::symlink_dir(root_fixture.recovery.path().join("journal"), &link) {
        Ok(()) => {}
        Err(error)
            if error.kind() == std::io::ErrorKind::PermissionDenied
                || error.raw_os_error() == Some(1314) =>
        {
            return;
        }
        Err(error) => panic!("journal reparse root: {error}"),
    }

    assert!(CheckpointJournal::open_bound(&link, 16 * 1024, &root_fixture.request).is_err());

    let identity = fixture();
    record_complete_provider_lifecycle(&identity);
    let identity_path = identity
        .recovery
        .path()
        .join("journal/execution-identity.json");
    let identity_target = identity.recovery.path().join("identity-target.json");
    std::fs::copy(&identity_path, &identity_target).expect("identity target");
    std::fs::remove_file(&identity_path).expect("remove identity");
    std::os::windows::fs::symlink_file(&identity_target, &identity_path).expect("identity reparse");
    assert!(
        CheckpointJournal::open_bound(
            identity.recovery.path().join("journal"),
            16 * 1024,
            &identity.request,
        )
        .is_err()
    );

    let directory = fixture();
    record_complete_provider_lifecycle(&directory);
    let event_directory = directory.recovery.path().join("journal/execution-events");
    let event_target = directory.recovery.path().join("event-target");
    std::fs::rename(&event_directory, &event_target).expect("move event directory");
    std::os::windows::fs::symlink_dir(&event_target, &event_directory)
        .expect("event directory reparse");
    assert!(
        CheckpointJournal::open_bound(
            directory.recovery.path().join("journal"),
            16 * 1024,
            &directory.request,
        )
        .is_err()
    );

    let event = fixture();
    record_complete_provider_lifecycle(&event);
    let event_directory = event.recovery.path().join("journal/execution-events");
    let event_path = std::fs::read_dir(&event_directory)
        .expect("execution events")
        .next()
        .expect("event")
        .expect("event entry")
        .path();
    let event_target = event.recovery.path().join("event-target.json");
    std::fs::copy(&event_path, &event_target).expect("event target");
    std::fs::remove_file(&event_path).expect("remove event");
    std::os::windows::fs::symlink_file(&event_target, &event_path).expect("event reparse");
    assert!(
        CheckpointJournal::open_bound(
            event.recovery.path().join("journal"),
            16 * 1024,
            &event.request,
        )
        .is_err()
    );
}

#[test]
fn provider_intent_without_capture_is_unknown_and_cannot_restart() {
    let fixture = fixture();
    fixture
        .journal
        .record_provider_request_intent(
            &fixture.request,
            &digest_bytes(b"prepared"),
            &digest_bytes(b"authority"),
        )
        .expect("intent");
    let state = fixture
        .journal
        .provider_state(&fixture.request)
        .expect("state");
    assert!(state.outcome_unknown());
    assert!(
        fixture
            .journal
            .provider_may_start(&fixture.request)
            .is_err()
    );
}

#[test]
fn pristine_request_binding_rejects_any_existing_event() {
    let fixture = fixture();
    fixture
        .journal
        .record_provider_request_intent(
            &fixture.request,
            &digest_bytes(b"prepared"),
            &digest_bytes(b"authority"),
        )
        .expect("provider intent");

    assert!(matches!(
        fixture.journal.bind_pristine_request(&fixture.request),
        Err(RecoveryError::EventSequenceInvalid)
    ));
}

#[test]
fn effect_intent_requires_normalized_adapter_turn() {
    let fixture = fixture();
    fixture
        .journal
        .record_provider_request_intent(
            &fixture.request,
            &digest_bytes(b"prepared"),
            &digest_bytes(b"authority"),
        )
        .expect("provider intent");

    assert!(
        fixture
            .journal
            .record_effect_intent(&fixture.request, &effect(&fixture.request))
            .is_err()
    );
}

#[test]
fn exact_effect_set_rejects_journal_only_intent() {
    let fixture = fixture();
    let index = digest_bytes(b"capture-index");
    fixture
        .journal
        .record_provider_request_intent(
            &fixture.request,
            &digest_bytes(b"prepared"),
            &digest_bytes(b"authority"),
        )
        .expect("provider intent");
    fixture
        .journal
        .record_provider_process_started(&fixture.request, &digest_bytes(b"invocation"))
        .expect("provider start");
    fixture
        .journal
        .record_provider_output_retained(&fixture.request, &digest_bytes(b"raw"))
        .expect("provider output");
    fixture
        .journal
        .record_provider_capture_published(&fixture.request, &index)
        .expect("capture published");
    fixture
        .journal
        .record_provider_capture_verified(&fixture.request, &index)
        .expect("capture verified");
    fixture
        .journal
        .record_adapter_turn_normalized(&fixture.request, &digest_bytes(b"turn"))
        .expect("turn normalized");
    let expected = effect(&fixture.request);
    let mut extra = expected.clone();
    extra.effect_id = "journal-only-effect".into();
    extra.paths = vec!["journal-only.txt".into()];
    fixture
        .journal
        .record_effect_intent(&fixture.request, &extra)
        .expect("journal-only intent");

    assert!(matches!(
        fixture.journal.effect_states(&fixture.request, &[expected]),
        Err(RecoveryError::EffectIdentityMismatch)
    ));
}

#[test]
fn provider_event_reordering_digest_drift_and_duplicates_fail_closed() {
    let fixture = fixture();
    assert!(
        fixture
            .journal
            .record_provider_output_retained(&fixture.request, &digest_bytes(b"raw"))
            .is_err()
    );
    fixture
        .journal
        .record_provider_request_intent(
            &fixture.request,
            &digest_bytes(b"prepared"),
            &digest_bytes(b"authority"),
        )
        .expect("intent");
    assert!(
        fixture
            .journal
            .record_provider_request_intent(
                &fixture.request,
                &digest_bytes(b"other"),
                &digest_bytes(b"other-authority"),
            )
            .is_err()
    );
    fixture
        .journal
        .record_provider_process_started(&fixture.request, &digest_bytes(b"invocation"))
        .expect("started");
    fixture
        .journal
        .record_provider_output_retained(&fixture.request, &digest_bytes(b"raw"))
        .expect("retained");
    fixture
        .journal
        .record_provider_capture_published(&fixture.request, &digest_bytes(b"index"))
        .expect("published");
    assert!(
        fixture
            .journal
            .record_provider_capture_verified(&fixture.request, &digest_bytes(b"other-index"))
            .is_err()
    );
}

#[test]
fn provider_intent_blocks_verification_start() {
    let fixture = fixture();
    fixture
        .journal
        .record_provider_request_intent(
            &fixture.request,
            &digest_bytes(b"prepared"),
            &digest_bytes(b"authority"),
        )
        .expect("intent");

    assert!(
        fixture
            .journal
            .begin_verification(&fixture.request)
            .is_err()
    );
}

#[test]
fn verification_rejects_unknown_effects_and_later_effect_intents() {
    let unknown = fixture();
    let pending = effect(&unknown.request);
    unknown
        .journal
        .record_effect_intent(&unknown.request, &pending)
        .expect("effect intent");
    assert!(
        unknown
            .journal
            .begin_verification(&unknown.request)
            .is_err(),
        "verification started with an unknown effect"
    );

    let verified = fixture();
    verified
        .journal
        .begin_verification(&verified.request)
        .expect("verification start");
    assert!(
        verified
            .journal
            .effect_state(&verified.request, &effect(&verified.request))
            .is_err(),
        "fresh effect remained eligible after verification"
    );
    assert!(
        verified
            .journal
            .record_effect_intent(&verified.request, &effect(&verified.request))
            .is_err(),
        "effect intent followed verification"
    );
}

#[test]
fn checkpoint_rejects_wrong_attempt_and_non_terminal_last_sequences() {
    for (name, events) in [
        (
            "wrong-attempt",
            vec![
                journal_event(0, JournalEventKind::VerificationStarted { attempt: 7 }),
                journal_event(
                    1,
                    JournalEventKind::VerifierRecorded {
                        report_digest: digest_bytes(b"report"),
                    },
                ),
            ],
        ),
        (
            "event-after-terminal",
            vec![
                journal_event(0, JournalEventKind::VerificationStarted { attempt: 0 }),
                journal_event(
                    1,
                    JournalEventKind::VerifierRecorded {
                        report_digest: digest_bytes(b"report"),
                    },
                ),
                journal_event(
                    2,
                    JournalEventKind::TerminalPublished {
                        record_digest: digest_bytes(b"terminal"),
                    },
                ),
                journal_event(3, JournalEventKind::VerificationStarted { attempt: 1 }),
                journal_event(
                    4,
                    JournalEventKind::VerifierRecorded {
                        report_digest: digest_bytes(b"later-report"),
                    },
                ),
            ],
        ),
    ] {
        let recovery = TempDir::new().expect("recovery");
        let request = request(recovery.path());
        let event_path = recovery.path().join(format!("{name}.jsonl"));
        let events_digest =
            write_durable_event_log(&event_path, &events, 16 * 1024).expect("event log");
        let checkpoint = checkpoint_for_events(&request, events.len() as u64, events_digest);
        let journal =
            CheckpointJournal::new(recovery.path().join("journal"), 16 * 1024).expect("journal");

        assert!(
            journal.commit(&checkpoint, &event_path).is_err(),
            "accepted {name} lifecycle"
        );
    }
}

#[test]
fn provider_checkpoint_rejects_incomplete_lifecycle_with_terminal() {
    let recovery = TempDir::new().expect("recovery");
    let request = request(recovery.path());
    let events = vec![
        journal_event(
            0,
            JournalEventKind::ProviderRequestIntent {
                prepared_run_digest: digest_bytes(b"prepared"),
                execution_authority_digest: digest_bytes(b"authority"),
            },
        ),
        journal_event(1, JournalEventKind::VerificationStarted { attempt: 0 }),
        journal_event(
            2,
            JournalEventKind::VerifierRecorded {
                report_digest: digest_bytes(b"report"),
            },
        ),
        journal_event(
            3,
            JournalEventKind::TerminalPublished {
                record_digest: digest_bytes(b"terminal"),
            },
        ),
    ];
    let events_path = recovery.path().join("events.jsonl");
    let events_digest =
        write_durable_event_log(&events_path, &events, 16 * 1024).expect("event log");
    let checkpoint = checkpoint_for_events(&request, events.len() as u64, events_digest);
    let journal = CheckpointJournal::new(recovery.path().join("journal"), 16 * 1024)
        .expect("checkpoint journal");

    assert!(journal.commit(&checkpoint, &events_path).is_err());
}

#[test]
fn provider_checkpoint_rejects_unknown_event_fields() {
    let recovery = TempDir::new().expect("recovery");
    let request = request(recovery.path());
    let mut events = provider_lifecycle_events();
    events.push(journal_event(
        6,
        JournalEventKind::VerificationStarted { attempt: 0 },
    ));
    events.push(journal_event(
        7,
        JournalEventKind::VerifierRecorded {
            report_digest: digest_bytes(b"report"),
        },
    ));
    let mut bytes = Vec::new();
    for (index, event) in events.iter().enumerate() {
        if index == 0 {
            let mut value = serde_json::to_value(event).expect("event value");
            value["kind"]["unexpected"] = serde_json::json!(true);
            bytes.extend_from_slice(&canonical_json_bytes(&value).expect("extended event"));
        } else {
            bytes.extend_from_slice(&canonical_json_bytes(event).expect("event"));
        }
        bytes.push(b'\n');
    }
    let events_path = recovery.path().join("events.jsonl");
    std::fs::write(&events_path, &bytes).expect("event log");
    let checkpoint = checkpoint_for_events(&request, events.len() as u64, digest_bytes(&bytes));
    let journal = CheckpointJournal::new(recovery.path().join("journal"), 16 * 1024)
        .expect("checkpoint journal");

    assert!(journal.commit(&checkpoint, &events_path).is_err());
}

#[test]
fn append_only_execution_journal_rejects_identity_effect_and_sequence_drift() {
    let recovery = TempDir::new().expect("recovery");
    let request = request(recovery.path());
    let journal =
        CheckpointJournal::new(recovery.path().join("journal"), 4_096).expect("execution journal");
    let effect = ao_next_core::contracts::EffectRequest {
        effect_id: "effect-01".into(),
        run_id: request.run_id.clone(),
        kind: ao_next_core::contracts::EffectKind::WriteFile,
        program: None,
        args: Vec::new(),
        paths: vec!["product.txt".into()],
        timeout_ms: 0,
        input_digest: digest_bytes(b"ao.next.file-does-not-exist.v1"),
        content: Some("product\n".into()),
    };
    journal
        .record_effect_intent(&request, &effect)
        .expect("durable intent");

    let mut changed_request = request.clone();
    changed_request.run_id = "changed-run".into();
    assert!(matches!(
        journal.effect_state(&changed_request, &effect),
        Err(RecoveryError::IdentityMismatch)
    ));

    let mut changed_effect = effect.clone();
    changed_effect.content = Some("changed\n".into());
    assert!(matches!(
        journal.effect_state(&request, &changed_effect),
        Err(RecoveryError::EffectIdentityMismatch)
    ));

    let events = recovery.path().join("journal/execution-events");
    let event_path = std::fs::read_dir(&events)
        .expect("events")
        .next()
        .expect("one event")
        .expect("event entry")
        .path();
    let original = std::fs::read(&event_path).expect("event bytes");
    let mut altered = original.clone();
    altered.push(b' ');
    std::fs::write(&event_path, altered).expect("digest drift");
    assert!(matches!(
        journal.effect_state(&request, &effect),
        Err(RecoveryError::EventDigestMismatch)
    ));
    std::fs::write(&event_path, original).expect("restore event");

    let name = event_path
        .file_name()
        .and_then(|value| value.to_str())
        .expect("event name");
    std::fs::rename(
        &event_path,
        events.join(format!("{:020}{}", 1, &name[20..])),
    )
    .expect("sequence drift");
    assert!(matches!(
        journal.effect_state(&request, &effect),
        Err(RecoveryError::EventSequenceInvalid)
    ));
}

#[test]
fn append_only_execution_journal_rejects_non_ascii_event_name_without_panicking() {
    let recovery = TempDir::new().expect("recovery");
    let request = request(recovery.path());
    let journal_root = recovery.path().join("journal");
    let journal = CheckpointJournal::new(&journal_root, 4_096).expect("execution journal");
    let events = journal_root.join("execution-events");
    std::fs::create_dir_all(&events).expect("events");
    let unsafe_name = format!("{:020}-{}éxxxx", 0, "a".repeat(63));
    assert_eq!(unsafe_name.len(), 90);
    std::fs::write(events.join(unsafe_name), b"{}").expect("invalid event");
    let effect = ao_next_core::contracts::EffectRequest {
        effect_id: "effect-01".into(),
        run_id: request.run_id.clone(),
        kind: ao_next_core::contracts::EffectKind::WriteFile,
        program: None,
        args: Vec::new(),
        paths: vec!["product.txt".into()],
        timeout_ms: 0,
        input_digest: digest_bytes(b"ao.next.file-does-not-exist.v1"),
        content: Some("product\n".into()),
    };
    assert!(matches!(
        journal.effect_state(&request, &effect),
        Err(RecoveryError::EventSequenceInvalid)
    ));
}

#[test]
fn verification_resume_and_terminal_publication_are_identity_bound_and_idempotent() {
    let recovery = TempDir::new().expect("recovery");
    let request = request(recovery.path());
    let journal = CheckpointJournal::new(recovery.path().join("journal"), 16 * 1024)
        .expect("execution journal");

    journal
        .begin_verification(&request)
        .expect("verification start");
    journal
        .begin_verification(&request)
        .expect("same interrupted verification resumes");
    journal
        .record_verifier(&request, &digest(ONE_DIGEST))
        .expect("verifier record");

    let terminal = br#"{"schema_version":"ao.next.test-terminal.v1"}"#;
    let digest = journal
        .publish_terminal_record(&request, terminal)
        .expect("terminal publication");
    assert_eq!(
        journal
            .publish_terminal_record(&request, terminal)
            .expect("idempotent terminal"),
        digest
    );
    assert!(matches!(
        journal.publish_terminal_record(&request, b"{}"),
        Err(RecoveryError::EventDigestMismatch)
    ));
}

#[test]
fn terminal_file_without_event_is_reused_and_event_is_appended_once() {
    let recovery = TempDir::new().expect("recovery");
    let request = request(recovery.path());
    let root = recovery.path().join("journal");
    let journal = CheckpointJournal::new(&root, 16 * 1024).expect("execution journal");
    journal
        .begin_verification(&request)
        .expect("verification start");
    journal
        .record_verifier(&request, &digest(ONE_DIGEST))
        .expect("verifier record");
    let terminal = br#"{"schema_version":"ao.next.test-terminal.v1","wall_clock_ms":41}"#;
    let digest = digest_bytes(terminal);
    let digest_hex = digest
        .as_str()
        .strip_prefix("sha256:")
        .expect("digest prefix");
    let terminal_path = root.join(format!("terminal-{digest_hex}.json"));
    std::fs::write(&terminal_path, terminal).expect("orphan terminal bytes");

    assert_eq!(
        journal
            .recover_terminal_record(&request)
            .expect("recover terminal")
            .expect("terminal bytes"),
        terminal
    );
    assert_eq!(
        journal
            .recover_terminal_record(&request)
            .expect("idempotent recovery")
            .expect("terminal bytes"),
        terminal
    );
    assert_eq!(
        std::fs::read(terminal_path).expect("terminal file"),
        terminal
    );
}

#[test]
fn checkpoint_identity_binds_the_exact_request() {
    let workspace = TempDir::new().expect("workspace");
    let request = request(workspace.path());
    let identity = CheckpointIdentity::from_request(&request).expect("identity");
    assert_eq!(
        identity.request_digest,
        canonical_digest(&request).expect("request digest")
    );
}

#[test]
fn oversized_event_log_is_rejected_before_any_file_is_written() {
    let recovery = TempDir::new().expect("recovery");
    let path = recovery.path().join("oversized.jsonl");
    let error =
        write_durable_event_log(&path, &recovery_events(), 1).expect_err("oversized log rejected");
    assert!(matches!(error, RecoveryError::Oversized { limit: 1, .. }));
    assert!(!path.exists());
}
