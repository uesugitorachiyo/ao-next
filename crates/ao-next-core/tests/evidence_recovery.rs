use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

use ao_next_core::adapter::AdapterIdentity;
use ao_next_core::contracts::{
    AuthorityEnvelope, Capability, Digest, ExternalEffectPolicy, ModelProfile, NetworkPolicy,
    RunLimits, RunRequest, SourceIdentity, StructuredCommand, VerifierProfile, WorkspaceIdentity,
};
use ao_next_core::effects::LocalEffectBroker;
use ao_next_core::evidence::{
    ArtifactSpec, ArtifactStore, EvidenceError, StoreLimits, seal_verified_run, verify_evidence,
    verify_sealed_run,
};
use ao_next_core::recovery::{
    Checkpoint, CheckpointIdentity, CheckpointJournal, JournalEvent, JournalEventKind,
    RecoveryError, write_durable_event_log,
};
use ao_next_core::strict_json::{canonical_digest, decode_strict_json};
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

fn request(root: &Path) -> RunRequest {
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
            allowed_programs: BTreeSet::from(["/usr/bin/true".into()]),
            network: NetworkPolicy::Denied,
            allowed_network_hosts: BTreeSet::new(),
            external_effects: ExternalEffectPolicy::Denied,
        },
        verifier_profile: VerifierProfile {
            profile_id: "complete-local".into(),
            profile_digest: digest(ONE_DIGEST),
            commands: vec![StructuredCommand {
                program: "/usr/bin/true".into(),
                args: Vec::new(),
                timeout_ms: 1_000,
            }],
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
        commands: vec![StructuredCommand {
            program: "/usr/bin/true".into(),
            args: Vec::new(),
            timeout_ms: 1_000,
        }],
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
    let broker = LocalEffectBroker::new(1_000, 4_096);
    let verifier = LocalProductVerifier::new(
        &request.run_id,
        &request.authority,
        &broker,
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
    let broker = LocalEffectBroker::new(1_000, 4_096);
    let verifier = LocalProductVerifier::new(
        &request.run_id,
        &request.authority,
        &broker,
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
            kind: JournalEventKind::VerifierRecorded {
                report_digest: digest(ONE_DIGEST),
            },
        },
    ]
}

#[test]
fn interrupted_resume_skips_committed_effect_and_requires_durable_verifier_events() {
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
        sequence: 2,
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
        sequence: 2,
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
