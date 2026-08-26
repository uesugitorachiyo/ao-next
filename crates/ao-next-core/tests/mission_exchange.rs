#![recursion_limit = "256"]

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};

use ao_next_core::adapter::EffectObservation;
use ao_next_core::contracts::{
    AuthorityEnvelope, Capability, Digest, ExternalEffectPolicy, ModelProfile, NetworkPolicy,
    RunLimits, RunRequest, SourceIdentity, StructuredCommand, VerifierProfile, WorkspaceIdentity,
};
use ao_next_core::mission_exchange::{
    ExecutionJournalPrefix, build_execution_journal_prefix, verify_execution_journal_prefix,
    write_execution_journal_prefix,
};
use ao_next_core::recovery::{CheckpointJournal, JournalEvent, JournalEventKind};
use ao_next_core::strict_json::{
    StrictJsonError, canonical_digest, canonical_json_bytes, decode_strict_json,
};
use chrono::{DateTime, Utc};
use serde_json::{Value, json};
use tempfile::TempDir;

const ZERO_DIGEST: &str = "sha256:0000000000000000000000000000000000000000000000000000000000000000";
const ONE_DIGEST: &str = "sha256:1111111111111111111111111111111111111111111111111111111111111111";

fn fixture(name: &str) -> Vec<u8> {
    std::fs::read(
        Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../tests/fixtures/mission-migration/journal-prefix")
            .join(name),
    )
    .expect("journal-prefix fixture")
}

fn timestamp(value: &str) -> DateTime<Utc> {
    DateTime::parse_from_rfc3339(value)
        .expect("fixture timestamp")
        .with_timezone(&Utc)
}

fn digest(value: &str) -> Digest {
    Digest::new(value).expect("fixture digest")
}

fn request() -> RunRequest {
    let workspace = PathBuf::from("/tmp/ao-next-stage1-journal-prefix");
    RunRequest {
        schema_version: "ao.next.run-request.v1".into(),
        run_id: "run-stage1-export".into(),
        objective: "Export one strict immutable execution journal prefix".into(),
        source: SourceIdentity {
            repository: "local-fixture".into(),
            head: digest(ZERO_DIGEST),
        },
        workspace: WorkspaceIdentity {
            workspace_id: "workspace-stage1-export".into(),
            root: workspace.clone(),
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
            issued_at: timestamp("2026-08-25T00:00:00Z"),
            expires_at: timestamp("2026-08-26T00:00:00Z"),
            capabilities: BTreeSet::from([Capability::ReadWorkspace]),
            allowed_roots: vec![workspace],
            allowed_programs: BTreeSet::new(),
            network: NetworkPolicy::Denied,
            allowed_network_hosts: BTreeSet::new(),
            external_effects: ExternalEffectPolicy::Denied,
        },
        verifier_profile: VerifierProfile {
            profile_id: "fixture-verifier".into(),
            profile_digest: digest(ONE_DIGEST),
            commands: vec![StructuredCommand {
                program: "cargo".into(),
                args: vec!["test".into(), "--workspace".into()],
                timeout_ms: 120_000,
            }],
            required_artifacts: Vec::new(),
        },
        policy_digest: digest(ZERO_DIGEST),
        limits: RunLimits {
            max_input_bytes: 1024 * 1024,
            max_turns: 8,
            max_repair_attempts: 2,
            max_run_ms: 900_000,
            max_effect_timeout_ms: 120_000,
            max_output_bytes: 1024 * 1024,
            max_tokens: 200_000,
        },
    }
}

#[allow(
    clippy::too_many_lines,
    reason = "the checked-in terminal fixture keeps every strict nested field explicit"
)]
fn terminal_record(request: &RunRequest) -> Value {
    let capture_digests = vec![digest(ZERO_DIGEST)];
    let raw_capture_digest = canonical_digest(&capture_digests).expect("capture digest");
    let measurement = json!({
        "schema_version": "ao.next.run-measurement.v2",
        "corpus_digest": ONE_DIGEST,
        "run_id": request.run_id,
        "trial_id": "trial-stage1-export",
        "trial_index": 0,
        "schedule_position": 0,
        "raw_capture_digest": raw_capture_digest,
        "raw_capture_digests": capture_digests,
        "workspace_instance_id": request.workspace.workspace_id,
        "task_id": "stage1-journal-prefix",
        "variant": "N7",
        "source_digest": request.source.head,
        "objective_digest": ZERO_DIGEST,
        "workspace_seed_digest": request.workspace.seed_digest,
        "visible_fixtures_digest": ZERO_DIGEST,
        "hidden_tests_digest": ONE_DIGEST,
        "verifier_profile_digest": request.verifier_profile.profile_digest,
        "runtime": "scripted",
        "runtime_digest": ZERO_DIGEST,
        "model_identifier": request.model_profile.model_identifier,
        "model_digest": ONE_DIGEST,
        "prompt_digest": ZERO_DIGEST,
        "policy_digest": request.policy_digest,
        "adapter_version": request.model_profile.adapter_version,
        "adapter_digest": ONE_DIGEST,
        "measurement_origin": "offline_fixture",
        "provider_usage_trusted": true,
        "tokens": {
            "input_tokens": 11,
            "cached_input_tokens": 7,
            "reasoning_tokens": 5,
            "output_tokens": 13,
            "reported_total_tokens": 36
        },
        "wall_clock_ms": 10,
        "model_wait_ms": 7,
        "worker_turns": 1,
        "repair_attempts": 0,
        "operator_interventions": 0,
        "changed_files": 0,
        "accepted_changed_files": 0,
        "task_success": true,
        "hidden_tests_passed": 1,
        "hidden_tests_total": 1,
        "regressions": 0,
        "unauthorized_effects": 0,
        "evidence_complete": true,
        "evidence_digest_valid": true,
        "recovery_attempted": false,
        "recovery_no_duplicate_effect": false,
        "cross_runtime_agreement": true,
        "worker_count": 1,
        "dynamic_fanout": false,
        "hidden_test_exposure": false
    });
    let git_workspace = json!({
        "repository_root": "/tmp/ao-next-stage1-journal-prefix",
        "common_dir": "/tmp/ao-next-stage1-journal-prefix/.git",
        "head_commit": "0123456789abcdef0123456789abcdef01234567",
        "branch": "ao-next-sealed-seed",
        "control_digest": ZERO_DIGEST,
        "index_digest": ONE_DIGEST
    });
    let mut semantic_measurement = measurement.clone();
    semantic_measurement
        .as_object_mut()
        .expect("measurement object")
        .remove("wall_clock_ms");
    semantic_measurement
        .as_object_mut()
        .expect("measurement object")
        .remove("model_wait_ms");
    let record_digest = canonical_digest(&(
        json!("N7"),
        json!("passed"),
        semantic_measurement,
        json!([ZERO_DIGEST]),
        json!(ONE_DIGEST),
        json!(ONE_DIGEST),
        json!(ONE_DIGEST),
        git_workspace.clone(),
        json!([]),
        json!([]),
    ))
    .expect("record digest");
    json!({
        "schema_version": "ao.next.live-run-record.v1",
        "variant": "N7",
        "terminal_state": "passed",
        "measurement": measurement,
        "capture_digests": [ZERO_DIGEST],
        "raw_capture_index_digest": ONE_DIGEST,
        "verifier_report_digest": ONE_DIGEST,
        "n7_execution_authority_digest": ONE_DIGEST,
        "git_workspace": git_workspace,
        "ao2_control_diagnostics": [],
        "native_effect_observations": [],
        "record_digest": record_digest
    })
}

#[derive(Clone, Copy)]
enum PrefixState {
    Prepared,
    ProviderOutcomeUnknown,
    Passed,
}

fn built_prefix(state: PrefixState) -> ExecutionJournalPrefix {
    let temporary = TempDir::new().expect("temporary journal");
    let root = temporary.path().join("journal");
    let request = request();
    let journal = CheckpointJournal::new(&root, 16 * 1024 * 1024).expect("journal");
    journal.bind_request(&request).expect("request binding");
    match state {
        PrefixState::Prepared => {
            std::fs::create_dir(root.join("execution-events")).expect("empty event directory");
        }
        PrefixState::ProviderOutcomeUnknown => {
            journal
                .record_provider_request_intent(&request, &digest(ZERO_DIGEST), &digest(ONE_DIGEST))
                .expect("provider intent");
            journal
                .record_provider_process_started(&request, &digest(ZERO_DIGEST))
                .expect("provider start");
        }
        PrefixState::Passed => {
            journal
                .begin_verification(&request)
                .expect("verification start");
            journal
                .record_verifier(&request, &digest(ONE_DIGEST))
                .expect("verifier record");
            journal
                .publish_terminal_record(
                    &request,
                    &canonical_json_bytes(&terminal_record(&request)).expect("terminal bytes"),
                )
                .expect("terminal publish");
        }
    }
    let opened =
        CheckpointJournal::open_bound(&root, 16 * 1024 * 1024, &request).expect("bound journal");
    build_execution_journal_prefix(&opened, &request).expect("prefix")
}

#[test]
fn valid_prepared_prefix_verifies_deterministically() {
    let bytes = fixture("valid-prepared.json");
    let prefix: ExecutionJournalPrefix =
        decode_strict_json(&bytes, 16 * 1024 * 1024).expect("strict prefix");
    let request: RunRequest =
        decode_strict_json(&fixture("run-request.json"), 1024 * 1024).expect("strict request");
    assert_eq!(
        prefix.request_digest,
        canonical_digest(&request).expect("digest")
    );
    verify_execution_journal_prefix(&prefix, &request).expect("valid prefix");
    assert_eq!(prefix.schema_version, "ao.next.execution-journal-prefix.v1");
    assert_eq!(prefix.worker_count, 1);
    assert!(!prefix.dynamic_fanout);
}

#[test]
fn checked_in_positive_vectors_verify() {
    let request: RunRequest =
        decode_strict_json(&fixture("run-request.json"), 1024 * 1024).expect("strict request");
    for name in [
        "valid-prepared.json",
        "valid-provider-outcome-unknown.json",
        "valid-passed.json",
    ] {
        let prefix: ExecutionJournalPrefix =
            decode_strict_json(&fixture(name), 16 * 1024 * 1024).expect("strict prefix");
        verify_execution_journal_prefix(&prefix, &request)
            .unwrap_or_else(|error| panic!("verify {name}: {error}"));
    }
}

#[test]
fn semantic_prefix_digest_is_the_declared_ordered_field_tuple() {
    let prefix = built_prefix(PrefixState::Prepared);
    let ordered_fields = json!([
        prefix.schema_version,
        prefix.run_id,
        prefix.request_digest,
        prefix.journal_identity,
        prefix.worker_count,
        prefix.dynamic_fanout,
        prefix.first_sequence,
        prefix.last_sequence,
        prefix.preceding_prefix_digest,
        prefix.events_digest,
        prefix.events,
        prefix.terminal_digest,
        prefix.terminal_record,
        prefix.safe_to_execute,
        prefix.executes_work,
        prefix.approves_work,
        prefix.mutates_repositories,
        prefix.grants_provider_access,
        prefix.publishes_artifacts,
        prefix.releases,
        prefix.deploys,
        prefix.advances_authority
    ]);
    assert_eq!(
        prefix.prefix_digest,
        canonical_digest(&ordered_fields).expect("ordered semantic digest")
    );
}

#[test]
fn checked_in_negative_vectors_fail_closed() {
    let request: RunRequest =
        decode_strict_json(&fixture("run-request.json"), 1024 * 1024).expect("strict request");
    assert!(matches!(
        decode_strict_json::<ExecutionJournalPrefix>(
            &fixture("invalid-duplicate-key.json"),
            16 * 1024 * 1024,
        ),
        Err(StrictJsonError::DuplicateKey(key)) if key == "schema_version"
    ));
    assert!(matches!(
        decode_strict_json::<ExecutionJournalPrefix>(
            &fixture("invalid-unknown-field.json"),
            16 * 1024 * 1024,
        ),
        Err(StrictJsonError::Deserialize(message)) if message.contains("unknown field")
    ));
    for name in [
        "invalid-sequence-gap.json",
        "invalid-digest-drift.json",
        "invalid-identity-drift.json",
        "invalid-terminal-contradiction.json",
    ] {
        let prefix: ExecutionJournalPrefix =
            decode_strict_json(&fixture(name), 16 * 1024 * 1024).expect("strict negative");
        assert!(
            verify_execution_journal_prefix(&prefix, &request).is_err(),
            "verified {name}"
        );
    }
}

#[test]
fn strict_decode_rejects_oversized_trailing_and_wrong_typed_prefixes() {
    let bytes = fixture("valid-prepared.json");
    assert!(matches!(
        decode_strict_json::<ExecutionJournalPrefix>(&bytes, bytes.len() - 1),
        Err(StrictJsonError::Oversized { .. })
    ));
    let mut trailing = bytes.clone();
    trailing.extend_from_slice(b"{}");
    assert!(decode_strict_json::<ExecutionJournalPrefix>(&trailing, 16 * 1024 * 1024).is_err());
    let mut wrong_type: Value = serde_json::from_slice(&bytes).expect("prefix value");
    wrong_type["worker_count"] = json!("1");
    assert!(
        decode_strict_json::<ExecutionJournalPrefix>(
            &serde_json::to_vec(&wrong_type).expect("wrong type"),
            16 * 1024 * 1024,
        )
        .is_err()
    );
    let wrong_casing = String::from_utf8(bytes).expect("prefix UTF-8").replacen(
        "\"schema_version\"",
        "\"Schema_Version\"",
        1,
    );
    assert!(
        decode_strict_json::<ExecutionJournalPrefix>(wrong_casing.as_bytes(), 16 * 1024 * 1024,)
            .is_err()
    );
}

#[test]
fn identity_worker_authority_sequence_lifecycle_and_digest_drift_fail_closed() {
    let request = request();
    let prepared = built_prefix(PrefixState::Prepared);
    let provider = built_prefix(PrefixState::ProviderOutcomeUnknown);
    let passed = built_prefix(PrefixState::Passed);

    let mut mutations = Vec::new();
    let mut changed = prepared.clone();
    changed.schema_version = "ao.next.execution-journal-prefix.v2".into();
    mutations.push(changed);
    let mut changed = prepared.clone();
    changed.run_id.clear();
    mutations.push(changed);
    let mut changed = prepared.clone();
    changed.run_id = "changed-run".into();
    mutations.push(changed);
    let mut changed = prepared.clone();
    changed.request_digest = digest(ZERO_DIGEST);
    mutations.push(changed);
    let mut changed = prepared.clone();
    changed.journal_identity.request_digest = digest(ZERO_DIGEST);
    mutations.push(changed);
    let mut changed = prepared.clone();
    changed.worker_count = 2;
    mutations.push(changed);
    let mut changed = prepared.clone();
    changed.dynamic_fanout = true;
    mutations.push(changed);
    for field in [
        "safe_to_execute",
        "executes_work",
        "approves_work",
        "mutates_repositories",
        "grants_provider_access",
        "publishes_artifacts",
        "releases",
        "deploys",
        "advances_authority",
    ] {
        let mut changed = prepared.clone();
        match field {
            "safe_to_execute" => changed.safe_to_execute = true,
            "executes_work" => changed.executes_work = true,
            "approves_work" => changed.approves_work = true,
            "mutates_repositories" => changed.mutates_repositories = true,
            "grants_provider_access" => changed.grants_provider_access = true,
            "publishes_artifacts" => changed.publishes_artifacts = true,
            "releases" => changed.releases = true,
            "deploys" => changed.deploys = true,
            "advances_authority" => changed.advances_authority = true,
            _ => unreachable!(),
        }
        mutations.push(changed);
    }
    let mut changed = prepared.clone();
    changed.preceding_prefix_digest = Some(digest(ZERO_DIGEST));
    mutations.push(changed);
    let mut changed = provider.clone();
    changed.first_sequence = 1;
    mutations.push(changed);
    let mut changed = provider.clone();
    changed.last_sequence = Some(0);
    mutations.push(changed);
    let mut changed = provider.clone();
    changed.events[1].sequence = 3;
    mutations.push(changed);
    let mut changed = provider.clone();
    changed.events_digest = digest(ZERO_DIGEST);
    mutations.push(changed);
    let mut changed = passed.clone();
    changed.events.push(JournalEvent {
        schema_version: "ao.next.journal-event.v1".into(),
        sequence: 3,
        kind: JournalEventKind::VerificationStarted { attempt: 1 },
    });
    changed.last_sequence = Some(3);
    mutations.push(changed);
    let mut changed = passed.clone();
    changed.terminal_record = None;
    mutations.push(changed);
    let mut changed = passed.clone();
    changed.terminal_digest = Some(digest(ZERO_DIGEST));
    mutations.push(changed);
    let mut changed = prepared;
    changed.prefix_digest = digest(ZERO_DIGEST);
    mutations.push(changed);

    for (index, changed) in mutations.iter().enumerate() {
        assert!(
            verify_execution_journal_prefix(changed, &request).is_err(),
            "accepted mutation {index}"
        );
    }
}

#[test]
fn invalid_effect_and_terminal_lifecycles_fail_before_digest_checks() {
    let request = request();
    let mut prefix = built_prefix(PrefixState::Prepared);
    let observation = EffectObservation {
        effect_id: "effect-01".into(),
        status: 0,
        stdout: Vec::new(),
        stderr: Vec::new(),
        output_digest: digest(ZERO_DIGEST),
    };
    prefix.events = vec![JournalEvent {
        schema_version: "ao.next.journal-event.v1".into(),
        sequence: 0,
        kind: JournalEventKind::EffectCompleted { observation },
    }];
    prefix.last_sequence = Some(0);
    assert!(verify_execution_journal_prefix(&prefix, &request).is_err());

    let mut prefix = built_prefix(PrefixState::Prepared);
    prefix.events = vec![
        JournalEvent {
            schema_version: "ao.next.journal-event.v1".into(),
            sequence: 0,
            kind: JournalEventKind::EffectIntent {
                effect_id: "effect-01".into(),
                effect_digest: digest(ONE_DIGEST),
            },
        },
        JournalEvent {
            schema_version: "ao.next.journal-event.v1".into(),
            sequence: 1,
            kind: JournalEventKind::VerificationStarted { attempt: 0 },
        },
    ];
    prefix.last_sequence = Some(1);
    assert!(verify_execution_journal_prefix(&prefix, &request).is_err());

    let mut prefix = built_prefix(PrefixState::Prepared);
    prefix.events = vec![JournalEvent {
        schema_version: "ao.next.journal-event.v1".into(),
        sequence: 0,
        kind: JournalEventKind::TerminalPublished {
            record_digest: digest(ZERO_DIGEST),
        },
    }];
    prefix.last_sequence = Some(0);
    assert!(verify_execution_journal_prefix(&prefix, &request).is_err());
}

#[test]
fn repeated_builds_and_writes_are_deterministic_and_create_only() {
    let first = built_prefix(PrefixState::ProviderOutcomeUnknown);
    let second = built_prefix(PrefixState::ProviderOutcomeUnknown);
    assert_eq!(first.prefix_digest, second.prefix_digest);
    assert_eq!(
        canonical_json_bytes(&first).expect("first bytes"),
        canonical_json_bytes(&second).expect("second bytes")
    );

    let temporary = TempDir::new().expect("temporary output");
    let path = temporary.path().join("prefix.json");
    write_execution_journal_prefix(&path, &first).expect("first write");
    assert_eq!(
        std::fs::read(&path).expect("written prefix"),
        canonical_json_bytes(&first).expect("canonical prefix")
    );
    assert!(write_execution_journal_prefix(&path, &second).is_err());
}

#[test]
fn build_rejects_orphan_and_noncanonical_terminal_bytes_without_repair() {
    let request = request();
    for orphan in [true, false] {
        let temporary = TempDir::new().expect("temporary journal");
        let root = temporary.path().join("journal");
        let journal = CheckpointJournal::new(&root, 16 * 1024 * 1024).expect("execution journal");
        journal.bind_request(&request).expect("request binding");
        journal
            .begin_verification(&request)
            .expect("verification start");
        journal
            .record_verifier(&request, &digest(ONE_DIGEST))
            .expect("verifier record");
        let bytes = br#"{ "terminal": "passed" }"#;
        if orphan {
            let terminal_digest = ao_next_core::evidence::digest_bytes(bytes);
            let name = format!(
                "terminal-{}.json",
                terminal_digest
                    .as_str()
                    .strip_prefix("sha256:")
                    .expect("digest prefix")
            );
            std::fs::write(root.join(name), bytes).expect("orphan terminal");
        } else {
            journal
                .publish_terminal_record(&request, bytes)
                .expect("published terminal");
        }
        let opened = CheckpointJournal::open_bound(&root, 16 * 1024 * 1024, &request)
            .expect("bound journal");
        assert!(
            build_execution_journal_prefix(&opened, &request).is_err(),
            "accepted orphan={orphan} noncanonical terminal"
        );
    }
}
