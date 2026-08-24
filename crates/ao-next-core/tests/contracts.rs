use std::collections::{BTreeMap, BTreeSet};
use std::path::PathBuf;

use ao_next_core::contracts::{
    AdapterIdentity, ArtifactEntry, ArtifactManifest, AuthorityEnvelope, Capability, Digest,
    EffectDecision, EffectEvent, EffectKind, EffectRequest, ExternalEffectPolicy,
    IntakeExpectation, ModelProfile, NetworkPolicy, PreparedRunReceipt, RunLimits, RunRequest,
    RunState, SourceIdentity, StructuredCommand, TerminalReadback, VerifierProfile, VerifierReport,
    VerifierResult, WorkspaceIdentity, generated_contract_schemas, validate_authority_current,
    validate_intake, validate_intake_identity,
};
use ao_next_core::strict_json::{
    StrictJsonError, canonical_digest, canonical_json_bytes, decode_strict_json,
};
use chrono::{DateTime, Utc};
use serde::Serialize;
use serde::de::DeserializeOwned;
use serde_json::{Value, json};

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

fn source() -> SourceIdentity {
    SourceIdentity {
        repository: "local-fixture".into(),
        head: digest(ZERO_DIGEST),
    }
}

fn workspace() -> WorkspaceIdentity {
    WorkspaceIdentity {
        workspace_id: "workspace-01".into(),
        root: PathBuf::from("/tmp/ao-next-fixture"),
        seed_digest: digest(ONE_DIGEST),
    }
}

fn authority() -> AuthorityEnvelope {
    AuthorityEnvelope {
        schema_version: "ao.next.authority-envelope.v1".into(),
        issued_by: "operator".into(),
        issued_at: timestamp("2026-08-05T00:00:00Z"),
        expires_at: timestamp("2026-08-06T00:00:00Z"),
        capabilities: BTreeSet::from([
            Capability::ReadWorkspace,
            Capability::WriteWorkspace,
            Capability::RunLocalProgram,
        ]),
        allowed_roots: vec![workspace().root],
        allowed_programs: BTreeSet::from(["cargo".into(), "git".into()]),
        network: NetworkPolicy::Denied,
        allowed_network_hosts: BTreeSet::new(),
        external_effects: ExternalEffectPolicy::Denied,
    }
}

fn model_profile() -> ModelProfile {
    ModelProfile {
        runtime: "scripted".into(),
        model_identifier: "fixture-model".into(),
        reasoning_effort: "high".into(),
        system_prompt_digest: digest(ZERO_DIGEST),
        tool_contract_digest: digest(ONE_DIGEST),
        context_limit: 32_000,
        output_limit: 4_000,
        adapter_version: "scripted-v1".into(),
    }
}

fn verifier_profile() -> VerifierProfile {
    VerifierProfile {
        profile_id: "rust-local".into(),
        profile_digest: digest(ONE_DIGEST),
        commands: vec![StructuredCommand {
            program: "cargo".into(),
            args: vec!["test".into(), "--workspace".into()],
            timeout_ms: 120_000,
        }],
        required_artifacts: vec![PathBuf::from("target/debug/ao-next")],
    }
}

fn request() -> RunRequest {
    RunRequest {
        schema_version: "ao.next.run-request.v1".into(),
        run_id: "run-01".into(),
        objective: "Implement the bounded fixture".into(),
        source: source(),
        workspace: workspace(),
        model_profile: model_profile(),
        authority: authority(),
        verifier_profile: verifier_profile(),
        policy_digest: digest(ZERO_DIGEST),
        limits: RunLimits {
            max_input_bytes: 64 * 1024,
            max_turns: 8,
            max_repair_attempts: 2,
            max_run_ms: 900_000,
            max_effect_timeout_ms: 120_000,
            max_output_bytes: 1024 * 1024,
            max_tokens: 200_000,
        },
    }
}

fn effect_event() -> EffectEvent {
    EffectEvent {
        schema_version: "ao.next.effect-event.v1".into(),
        request: EffectRequest {
            effect_id: "effect-01".into(),
            run_id: "run-01".into(),
            kind: EffectKind::RunProgram,
            program: Some("cargo".into()),
            content: None,
            args: vec!["test".into(), "--workspace".into()],
            paths: vec![PathBuf::from("/tmp/ao-next-fixture")],
            timeout_ms: 120_000,
            input_digest: digest(ZERO_DIGEST),
        },
        decision: EffectDecision::Admitted,
        policy_digest: digest(ONE_DIGEST),
        recorded_at: timestamp("2026-08-05T01:00:00Z"),
        output_digest: Some(digest(ZERO_DIGEST)),
    }
}

fn verifier_report() -> VerifierReport {
    VerifierReport {
        schema_version: "ao.next.verifier-report.v1".into(),
        run_id: "run-01".into(),
        verifier_profile_digest: digest(ONE_DIGEST),
        started_at: timestamp("2026-08-05T01:00:00Z"),
        completed_at: timestamp("2026-08-05T01:00:03Z"),
        passed: true,
        results: vec![VerifierResult {
            verifier_id: "cargo-test".into(),
            passed: true,
            exit_status: Some(0),
            output_digest: digest(ZERO_DIGEST),
            message: "workspace tests passed".into(),
        }],
    }
}

fn artifact_manifest() -> ArtifactManifest {
    ArtifactManifest {
        schema_version: "ao.next.artifact-manifest.v1".into(),
        run_id: "run-01".into(),
        source: source(),
        entries: vec![ArtifactEntry {
            artifact_id: "test-log".into(),
            media_type: "text/plain".into(),
            digest: digest(ZERO_DIGEST),
            content_ref:
                "artifacts/sha256/0000000000000000000000000000000000000000000000000000000000000000"
                    .into(),
            original_ref: "logs/cargo-test.log".into(),
            size_bytes: 42,
            producer: "local-verifier".into(),
            input_digests: vec![digest(ONE_DIGEST)],
        }],
    }
}

fn terminal_readback() -> TerminalReadback {
    TerminalReadback {
        schema_version: "ao.next.terminal-readback.v1".into(),
        run_id: "run-01".into(),
        source: source(),
        workspace: workspace(),
        adapter: AdapterIdentity {
            runtime: "scripted".into(),
            model_identifier: "fixture-model".into(),
            adapter_version: "scripted-v1".into(),
            worker_id: "worker-01".into(),
        },
        request_digest: digest(ZERO_DIGEST),
        policy_digest: digest(ONE_DIGEST),
        verifier_report_digest: digest(ZERO_DIGEST),
        artifact_manifest_digest: digest(ONE_DIGEST),
        terminal_state: RunState::Passed,
        completed_at: timestamp("2026-08-05T01:00:05Z"),
        safety_boundaries: BTreeMap::from([
            ("approves_work".into(), false),
            ("executes_mission_work".into(), false),
            ("grants_authority".into(), false),
        ]),
        exact_next_action: "Await separately authorized live evaluation".into(),
    }
}

fn prepared_run_receipt() -> PreparedRunReceipt {
    PreparedRunReceipt {
        schema_version: "ao.next.prepared-run.v1".into(),
        run_id: "run-prepared-01".into(),
        input_digest: digest(ZERO_DIGEST),
        request_digest: digest(ONE_DIGEST),
        repository_root: PathBuf::from("workspace"),
        common_directory: PathBuf::from("workspace/.git"),
        branch: "ao-next-sealed-seed".into(),
        base_commit: "0123456789abcdef0123456789abcdef01234567".into(),
        control_digest: digest(ZERO_DIGEST),
        index_digest: digest(ONE_DIGEST),
        workspace_digest: digest(ZERO_DIGEST),
        journal_identity_digest: digest(ONE_DIGEST),
        prepared_at: timestamp("2026-08-23T00:00:00Z"),
        expires_at: timestamp("2026-08-24T00:00:00Z"),
        provider_calls: 0,
        safe_to_execute: false,
    }
}

#[test]
fn prepared_run_receipt_round_trips_with_exact_git_identity() {
    let receipt = prepared_run_receipt();
    let bytes = canonical_json_bytes(&receipt).expect("receipt bytes");
    let decoded: PreparedRunReceipt =
        decode_strict_json(&bytes, 64 * 1024).expect("receipt decode");
    assert_eq!(decoded, receipt);
}

#[test]
fn prepared_run_receipt_rejects_unsafe_runtime_values() {
    for (field, invalid) in [
        ("base_commit", serde_json::json!("A".repeat(40))),
        ("provider_calls", serde_json::json!(1)),
        ("safe_to_execute", serde_json::json!(true)),
    ] {
        let mut receipt = serde_json::to_value(prepared_run_receipt()).expect("receipt value");
        receipt[field] = invalid;
        let bytes = serde_json::to_vec(&receipt).expect("invalid receipt bytes");
        assert!(
            decode_strict_json::<PreparedRunReceipt>(&bytes, 64 * 1024).is_err(),
            "unsafe {field} decoded"
        );
    }
}

fn assert_round_trip<T>(value: &T)
where
    T: Serialize + DeserializeOwned + std::fmt::Debug + PartialEq,
{
    let bytes = serde_json::to_vec(value).expect("serialize fixture");
    let decoded: T = decode_strict_json(&bytes, 64 * 1024).expect("strict round trip");
    assert_eq!(&decoded, value);
}

#[test]
fn every_public_contract_round_trips_through_strict_json() {
    assert_round_trip(&request());
    assert_round_trip(&authority());
    assert_round_trip(&effect_event());
    assert_round_trip(&verifier_report());
    assert_round_trip(&artifact_manifest());
    assert_round_trip(&terminal_readback());
}

#[test]
fn duplicate_keys_are_rejected_before_deserialization() {
    let duplicate = br#"{"schema_version":"ao.next.run-request.v1","schema_version":"drift"}"#;
    let error = decode_strict_json::<RunRequest>(duplicate, 1024).expect_err("duplicate rejected");
    assert!(matches!(error, StrictJsonError::DuplicateKey(ref key) if key == "schema_version"));
}

#[test]
fn unknown_contract_fields_are_rejected() {
    let mut value = serde_json::to_value(request()).expect("serialize request");
    value
        .as_object_mut()
        .expect("request object")
        .insert("self_authorize".into(), Value::Bool(true));
    let bytes = serde_json::to_vec(&value).expect("serialize drifted request");
    let error = decode_strict_json::<RunRequest>(&bytes, 64 * 1024).expect_err("unknown rejected");
    assert!(
        matches!(error, StrictJsonError::Deserialize(message) if message.contains("unknown field"))
    );
}

#[test]
fn oversized_input_is_rejected_without_parsing() {
    let bytes = serde_json::to_vec(&request()).expect("serialize request");
    let error =
        decode_strict_json::<RunRequest>(&bytes, bytes.len() - 1).expect_err("size rejected");
    assert!(
        matches!(error, StrictJsonError::Oversized { actual, limit } if actual == bytes.len() && limit == bytes.len() - 1)
    );
}

#[test]
fn malformed_digest_is_rejected() {
    let mut value = serde_json::to_value(request()).expect("serialize request");
    value["source"]["head"] = json!("sha256:ABC");
    let bytes = serde_json::to_vec(&value).expect("serialize drifted request");
    let error = decode_strict_json::<RunRequest>(&bytes, 64 * 1024).expect_err("digest rejected");
    assert!(matches!(error, StrictJsonError::Deserialize(message) if message.contains("digest")));
}

#[test]
fn stale_authority_is_rejected() {
    let request = request();
    let expectation = IntakeExpectation {
        run_id: "run-01".into(),
        source: source(),
        workspace: workspace(),
        now: timestamp("2026-08-06T00:00:00Z"),
    };
    let error = validate_intake(&request, &expectation).expect_err("expired authority rejected");
    assert_eq!(error.to_string(), "authority is not current");
}

#[test]
fn intake_identity_accepts_expired_authority_for_read_only_inspection() {
    let request = request();
    let expectation = IntakeExpectation {
        run_id: request.run_id.clone(),
        source: request.source.clone(),
        workspace: request.workspace.clone(),
        now: request.authority.expires_at,
    };

    validate_intake_identity(&request, &expectation).expect("expired identity remains inspectable");
    assert_eq!(
        validate_authority_current(&request.authority, expectation.now),
        Err(ao_next_core::contracts::IntakeError::AuthorityNotCurrent)
    );
}

#[test]
fn intake_identity_keeps_schema_root_and_interval_checks_after_expiry() {
    let expectation = IntakeExpectation {
        run_id: "run-01".into(),
        source: source(),
        workspace: workspace(),
        now: timestamp("2026-08-07T00:00:00Z"),
    };
    for mutation in ["request-schema", "authority-schema", "root", "interval"] {
        let mut request = request();
        match mutation {
            "request-schema" => request.schema_version = "drifted".into(),
            "authority-schema" => request.authority.schema_version = "drifted".into(),
            "root" => request.authority.allowed_roots.clear(),
            "interval" => request.authority.issued_at = request.authority.expires_at,
            _ => unreachable!(),
        }
        assert!(
            validate_intake_identity(&request, &expectation).is_err(),
            "identity accepted {mutation} drift"
        );
    }
}

#[test]
fn source_and_workspace_identity_drift_are_rejected() {
    let request = request();
    let mut changed_source = source();
    changed_source.head = digest(ONE_DIGEST);
    let source_error = validate_intake(
        &request,
        &IntakeExpectation {
            run_id: "run-01".into(),
            source: changed_source,
            workspace: workspace(),
            now: timestamp("2026-08-05T12:00:00Z"),
        },
    )
    .expect_err("source drift rejected");
    assert_eq!(source_error.to_string(), "source identity mismatch");

    let mut changed_workspace = workspace();
    changed_workspace.workspace_id = "workspace-02".into();
    let workspace_error = validate_intake(
        &request,
        &IntakeExpectation {
            run_id: "run-01".into(),
            source: source(),
            workspace: changed_workspace,
            now: timestamp("2026-08-05T12:00:00Z"),
        },
    )
    .expect_err("workspace drift rejected");
    assert_eq!(workspace_error.to_string(), "workspace identity mismatch");
}

#[test]
fn canonical_digest_uses_sorted_compact_json() {
    let value = json!({"b": 2, "a": 1});
    assert_eq!(
        canonical_digest(&value).expect("canonical digest").as_str(),
        "sha256:43258cff783fe7036d8a43033f830adfc60ec037382473548ac742b888292777"
    );
}

#[test]
fn checked_in_contract_schemas_match_generated_types() {
    let repository_root = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..");
    let generated_contracts = generated_contract_schemas();
    assert!(
        generated_contracts.contains_key("command-verifier-profile-v1.schema.json"),
        "live command verifier contract must be checked in"
    );
    assert!(
        generated_contracts.contains_key("prepared-run-v1.schema.json"),
        "prepared-run receipt contract must be checked in"
    );
    for (file_name, generated) in generated_contracts {
        let path = repository_root.join("docs/contracts").join(file_name);
        let checked_in: Value = serde_json::from_slice(
            &std::fs::read(&path)
                .unwrap_or_else(|error| panic!("read {}: {error}", path.display())),
        )
        .unwrap_or_else(|error| panic!("parse {}: {error}", path.display()));
        assert_eq!(checked_in, generated, "schema drift: {}", path.display());
    }
}
