use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};
use std::process::{Command, Output};

use ao_next_core::contracts::{
    AdapterIdentity, AuthorityEnvelope, Capability, Digest, ExternalEffectPolicy, ModelProfile,
    NetworkPolicy, PreparedRunReceipt, RunLimits, RunRequest, RunState, SourceIdentity,
    StructuredCommand, TerminalReadback, VerifierProfile, WorkspaceIdentity,
};
use ao_next_core::evidence::digest_bytes;
use ao_next_core::recovery::CheckpointJournal;
use ao_next_core::strict_json::{canonical_digest, canonical_json_bytes};
use ao_next_core::verifier::{CommandVerifierEntry, CommandVerifierProfile};
use ao_next_eval::comparison::ComparisonRequest;
use ao_next_eval::corpus::{
    CorpusKind, CorpusManifest, EvaluationTask, ScheduleEntry, VariantProfile,
    counterbalanced_schedule,
};
use ao_next_eval::metrics::{ExecutionVariant, MeasurementOrigin, RunMeasurement, TokenRow};
use chrono::{DateTime, Duration, Utc};
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

fn readback() -> TerminalReadback {
    TerminalReadback {
        schema_version: "ao.next.terminal-readback.v1".into(),
        run_id: "run-cli-01".into(),
        source: SourceIdentity {
            repository: "fixture".into(),
            head: digest(ZERO_DIGEST),
        },
        workspace: WorkspaceIdentity {
            workspace_id: "workspace-cli-01".into(),
            root: PathBuf::from("/tmp/ao-next-cli"),
            seed_digest: digest(ONE_DIGEST),
        },
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
        completed_at: timestamp("2026-08-05T12:00:00Z"),
        safety_boundaries: BTreeMap::from([
            ("approves_work".into(), false),
            ("executes_mission_work".into(), false),
            ("grants_authority".into(), false),
            ("publishes".into(), false),
        ]),
        exact_next_action: "Await separately authorized live evaluation".into(),
    }
}

fn run(args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_ao-next"))
        .args(args)
        .output()
        .expect("run ao-next")
}

fn run_without_live_authority(args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_ao-next"))
        .args(args)
        .env_remove("AO_NEXT_LIVE_PROVIDER_CALLS")
        .output()
        .expect("run provider-free ao-next")
}

fn run_with_live_environment(args: &[&str], path: &Path, gate: Option<&str>) -> Output {
    let mut command = Command::new(env!("CARGO_BIN_EXE_ao-next"));
    command.args(args).env("PATH", path);
    match gate {
        Some(value) => {
            command.env("AO_NEXT_LIVE_PROVIDER_CALLS", value);
        }
        None => {
            command.env_remove("AO_NEXT_LIVE_PROVIDER_CALLS");
        }
    }
    command.output().expect("run ao-next")
}

fn write_json(path: &Path, value: &impl serde::Serialize) {
    std::fs::write(path, serde_json::to_vec(value).expect("fixture JSON")).expect("write fixture");
}

fn assert_json_error(output: &Output, expected_status: i32, expected_code: &str) {
    assert_eq!(output.status.code(), Some(expected_status));
    let value: serde_json::Value =
        serde_json::from_slice(&output.stdout).expect("machine JSON error");
    assert_eq!(value["schema_version"], "ao.next.cli-error.v1");
    assert_eq!(value["code"], expected_code);
    assert!(!output.stderr.is_empty());
}

fn run_request_json(root: &Path) -> serde_json::Value {
    serde_json::json!({
        "schema_version": "ao.next.run-request.v1",
        "run_id": "run-cli-scripted-01",
        "objective": "Exercise the offline CLI",
        "source": { "repository": "fixture", "head": ZERO_DIGEST },
        "workspace": {
            "workspace_id": "workspace-cli-scripted-01",
            "root": root,
            "seed_digest": ONE_DIGEST
        },
        "model_profile": {
            "runtime": "scripted",
            "model_identifier": "fixture-model",
            "reasoning_effort": "high",
            "system_prompt_digest": ZERO_DIGEST,
            "tool_contract_digest": ONE_DIGEST,
            "context_limit": 32000,
            "output_limit": 4000,
            "adapter_version": "scripted-v1"
        },
        "authority": {
            "schema_version": "ao.next.authority-envelope.v1",
            "issued_by": "operator",
            "issued_at": "2026-08-05T00:00:00Z",
            "expires_at": "2026-08-06T00:00:00Z",
            "capabilities": [],
            "allowed_roots": [root],
            "allowed_programs": [],
            "network": "denied",
            "allowed_network_hosts": [],
            "external_effects": "denied"
        },
        "verifier_profile": {
            "profile_id": "scripted",
            "profile_digest": ONE_DIGEST,
            "commands": [],
            "required_artifacts": []
        },
        "policy_digest": ZERO_DIGEST,
        "limits": {
            "max_input_bytes": 65536,
            "max_turns": 2,
            "max_repair_attempts": 0,
            "max_run_ms": 10000,
            "max_effect_timeout_ms": 1000,
            "max_output_bytes": 4096,
            "max_tokens": 1000
        }
    })
}

fn ready_comparison_json() -> serde_json::Value {
    let profiles = [
        (ExecutionVariant::N0, "N0"),
        (ExecutionVariant::N4, "N4"),
        (ExecutionVariant::N7, "N7"),
    ]
    .into_iter()
    .map(|(variant, runtime)| VariantProfile {
        variant,
        runtime: runtime.into(),
        runtime_digest: digest_bytes(format!("{runtime}:runtime").as_bytes()),
        model_identifier: "fixture-model".into(),
        model_digest: digest_bytes(format!("{runtime}:model").as_bytes()),
        prompt_digest: digest(ONE_DIGEST),
        policy_digest: digest(ZERO_DIGEST),
        adapter_version: "fixture-v1".into(),
        adapter_digest: digest_bytes(format!("{runtime}:adapter").as_bytes()),
    })
    .collect::<Vec<_>>();
    let task = EvaluationTask {
        task_id: "cli-evaluation".into(),
        task_kind: "bounded_public_defect_repair".into(),
        source_digest: digest(ZERO_DIGEST),
        objective_digest: digest(ONE_DIGEST),
        workspace_seed_digest: digest(ZERO_DIGEST),
        visible_fixtures_digest: digest(ONE_DIGEST),
        hidden_tests_digest: digest(ZERO_DIGEST),
        verifier_profile_digest: digest(ONE_DIGEST),
        variant_profiles: profiles,
    };
    let mut corpus = CorpusManifest {
        schema_version: "ao.next.evaluation-corpus.v2".into(),
        corpus_kind: CorpusKind::SyntheticUnitTest,
        corpus_digest: digest(ZERO_DIGEST),
        required_trial_count: 3,
        schedule: counterbalanced_schedule(),
        tasks: vec![task],
    };
    corpus.corpus_digest = corpus.calculated_digest().expect("corpus digest");
    let task = &corpus.tasks[0];
    let runs = corpus
        .schedule
        .iter()
        .map(|entry| comparison_measurement(&corpus, task, entry))
        .collect();
    serde_json::to_value(ComparisonRequest {
        schema_version: "ao.next.comparison-request.v2".into(),
        corpus,
        runs,
    })
    .expect("comparison JSON")
}

fn comparison_measurement(
    corpus: &CorpusManifest,
    task: &EvaluationTask,
    entry: &ScheduleEntry,
) -> RunMeasurement {
    let (tokens, wall_clock_ms) = match entry.variant {
        ExecutionVariant::N0 => (400, 400),
        ExecutionVariant::N4 => (100, 200),
        ExecutionVariant::N7 => (110, 250),
    };
    let profile = task
        .variant_profiles
        .iter()
        .find(|profile| profile.variant == entry.variant)
        .expect("variant profile");
    let raw_capture_digests = vec![digest_bytes(
        format!(
            "cli-capture-{}-{:?}-{}",
            task.task_id, entry.variant, entry.trial_index
        )
        .as_bytes(),
    )];
    RunMeasurement {
        schema_version: "ao.next.run-measurement.v2".into(),
        corpus_digest: corpus.corpus_digest.clone(),
        run_id: format!(
            "cli-run-{}-{:?}-{}",
            task.task_id, entry.variant, entry.trial_index
        ),
        trial_id: format!(
            "cli-trial-{}-{:?}-{}",
            task.task_id, entry.variant, entry.trial_index
        ),
        trial_index: entry.trial_index,
        schedule_position: entry.schedule_position,
        raw_capture_digest: ao_next_core::strict_json::canonical_digest(&raw_capture_digests)
            .expect("capture manifest"),
        raw_capture_digests,
        workspace_instance_id: format!(
            "cli-workspace-{}-{:?}-{}",
            task.task_id, entry.variant, entry.trial_index
        ),
        task_id: task.task_id.clone(),
        variant: entry.variant,
        source_digest: task.source_digest.clone(),
        objective_digest: task.objective_digest.clone(),
        workspace_seed_digest: task.workspace_seed_digest.clone(),
        visible_fixtures_digest: task.visible_fixtures_digest.clone(),
        hidden_tests_digest: task.hidden_tests_digest.clone(),
        verifier_profile_digest: task.verifier_profile_digest.clone(),
        runtime: profile.runtime.clone(),
        runtime_digest: profile.runtime_digest.clone(),
        model_identifier: profile.model_identifier.clone(),
        model_digest: profile.model_digest.clone(),
        prompt_digest: profile.prompt_digest.clone(),
        policy_digest: profile.policy_digest.clone(),
        adapter_version: profile.adapter_version.clone(),
        adapter_digest: profile.adapter_digest.clone(),
        measurement_origin: MeasurementOrigin::OfflineFixture,
        provider_usage_trusted: true,
        tokens: TokenRow {
            input_tokens: Some(tokens),
            cached_input_tokens: Some(0),
            reasoning_tokens: Some(0),
            output_tokens: Some(0),
            reported_total_tokens: tokens,
        },
        wall_clock_ms,
        model_wait_ms: wall_clock_ms / 2,
        worker_turns: 1,
        repair_attempts: 0,
        operator_interventions: 0,
        changed_files: 1,
        accepted_changed_files: 1,
        task_success: true,
        hidden_tests_passed: 10,
        hidden_tests_total: 10,
        regressions: 0,
        unauthorized_effects: 0,
        evidence_complete: true,
        evidence_digest_valid: true,
        recovery_attempted: entry.variant == ExecutionVariant::N7,
        recovery_no_duplicate_effect: true,
        cross_runtime_agreement: true,
        worker_count: 1,
        dynamic_fanout: false,
        hidden_test_exposure: false,
    }
}

fn scripted_plan(action: &serde_json::Value, passed: bool) -> serde_json::Value {
    serde_json::json!({
        "schema_version": "ao.next.scripted-run-plan.v1",
        "adapter_identity": {
            "runtime": "scripted",
            "model_identifier": "fixture-model",
            "adapter_version": "scripted-v1",
            "worker_id": "worker-cli-01"
        },
        "turns": [{
            "actions": [action],
            "usage": {
                "input_tokens": 1,
                "cached_input_tokens": 0,
                "reasoning_tokens": 1,
                "output_tokens": 1,
                "output_bytes": 16
            },
            "model_claimed_success": false,
            "control_mutations": []
        }],
        "verifier_reports": if passed { serde_json::json!([{
            "schema_version": "ao.next.verifier-report.v1",
            "run_id": "run-cli-scripted-01",
            "verifier_profile_digest": ONE_DIGEST,
            "started_at": "2026-08-05T11:59:00Z",
            "completed_at": "2026-08-05T12:00:00Z",
            "passed": true,
            "results": []
        }]) } else { serde_json::json!([]) },
        "artifacts": [],
        "completed_at": "2026-08-05T12:00:00Z"
    })
}

#[test]
fn inspect_emits_json_on_stdout_and_human_summary_on_stderr() {
    let temporary = TempDir::new().expect("temporary");
    let path = temporary.path().join("readback.json");
    write_json(&path, &readback());
    let output = run(&["inspect", "--readback", path.to_str().expect("path")]);
    assert_eq!(output.status.code(), Some(0));
    let parsed: TerminalReadback =
        serde_json::from_slice(&output.stdout).expect("terminal readback JSON");
    assert_eq!(parsed, readback());
    assert!(String::from_utf8_lossy(&output.stderr).contains("inspected"));
}

#[test]
fn malformed_inputs_have_stable_json_exit_statuses_for_each_command() {
    let temporary = TempDir::new().expect("temporary");
    let malformed = temporary.path().join("malformed.json");
    std::fs::write(&malformed, b"{not-json").expect("malformed fixture");
    let missing = temporary.path().join("missing");
    let cases = [
        vec!["inspect", "--readback", malformed.to_str().expect("path")],
        vec![
            "verify-evidence",
            "--root",
            temporary.path().to_str().expect("root"),
            "--request",
            malformed.to_str().expect("path"),
        ],
        vec![
            "replay",
            "--checkpoint-root",
            temporary.path().to_str().expect("root"),
            "--request",
            malformed.to_str().expect("path"),
        ],
        vec![
            "run",
            "--request",
            malformed.to_str().expect("path"),
            "--script",
            malformed.to_str().expect("path"),
            "--evidence",
            missing.to_str().expect("path"),
            "--now",
            "2026-08-05T12:00:00Z",
        ],
    ];
    for args in cases {
        assert_json_error(&run(&args), 3, "invalid_input");
    }

    let evaluate = run(&[
        "evaluate",
        "--comparison",
        malformed.to_str().expect("path"),
    ]);
    assert_json_error(&evaluate, 3, "invalid_input");
}

#[test]
fn clap_usage_errors_are_also_machine_readable() {
    let output = run(&["inspect"]);
    assert_json_error(&output, 2, "usage");
}

#[test]
fn evaluate_emits_a_non_promotional_offline_decision() {
    let temporary = TempDir::new().expect("temporary");
    let comparison = temporary.path().join("comparison.json");
    write_json(&comparison, &ready_comparison_json());
    let output = run(&[
        "evaluate",
        "--comparison",
        comparison.to_str().expect("path"),
    ]);
    assert_eq!(output.status.code(), Some(0));
    let report: serde_json::Value =
        serde_json::from_slice(&output.stdout).expect("comparison report");
    assert_eq!(report["decision"], "AO_NEXT_READY_FOR_LIVE_EVALUATION");
    assert_eq!(report["promotion_authorized"], false);
    assert_eq!(report["dynamic_fanout_authorized"], false);
    assert!(String::from_utf8_lossy(&output.stderr).contains("evaluated"));
}

#[test]
fn run_maps_success_denial_failure_interruption_and_evidence_failure() {
    let temporary = TempDir::new().expect("temporary");
    let request_path = temporary.path().join("request.json");
    write_json(&request_path, &run_request_json(temporary.path()));

    let cases = [
        (serde_json::json!({"kind": "verify"}), true, 0),
        (
            serde_json::json!({
                "kind": "effect",
                "value": {
                    "effect_id": "read-denied",
                    "run_id": "run-cli-scripted-01",
                    "kind": "read_file",
                    "program": null,
                    "args": [],
                    "paths": [temporary.path().join("result.txt")],
                    "timeout_ms": 100,
                    "input_digest": ZERO_DIGEST
                }
            }),
            false,
            4,
        ),
        (
            serde_json::json!({"kind": "blocked", "value": "fixture blocker"}),
            false,
            5,
        ),
        (serde_json::json!({"kind": "interrupt"}), false, 6),
    ];

    for (index, (action, passed, expected_status)) in cases.into_iter().enumerate() {
        let script_path = temporary.path().join(format!("script-{index}.json"));
        let evidence_path = temporary.path().join(format!("evidence-{index}"));
        write_json(&script_path, &scripted_plan(&action, passed));
        let output = run(&[
            "run",
            "--request",
            request_path.to_str().expect("path"),
            "--script",
            script_path.to_str().expect("path"),
            "--evidence",
            evidence_path.to_str().expect("path"),
            "--now",
            "2026-08-05T12:00:00Z",
        ]);
        assert_eq!(output.status.code(), Some(expected_status));
        let _: serde_json::Value =
            serde_json::from_slice(&output.stdout).expect("run machine JSON");
        assert!(!output.stderr.is_empty());
    }

    let missing_artifact = temporary.path().join("does-not-exist");
    let mut evidence_failure = scripted_plan(&serde_json::json!({"kind": "verify"}), true);
    evidence_failure["artifacts"] = serde_json::json!([{
        "artifact_id": "missing",
        "path": missing_artifact,
        "original_ref": "fixture://missing",
        "media_type": "text/plain",
        "producer": "fixture",
        "input_digests": []
    }]);
    let script_path = temporary.path().join("script-evidence-failure.json");
    write_json(&script_path, &evidence_failure);
    let output = run(&[
        "run",
        "--request",
        request_path.to_str().expect("path"),
        "--script",
        script_path.to_str().expect("path"),
        "--evidence",
        temporary
            .path()
            .join("evidence-failure")
            .to_str()
            .expect("path"),
        "--now",
        "2026-08-05T12:00:00Z",
    ]);
    assert_json_error(&output, 7, "evidence_failure");
}

#[cfg(unix)]
#[test]
fn live_commands_deny_before_input_or_process_resolution() {
    use std::os::unix::fs::PermissionsExt;

    let temporary = TempDir::new().expect("temporary");
    let marker = temporary.path().join("fake-provider-started");
    let executable = temporary.path().join("codex");
    std::fs::write(
        &executable,
        format!("#!/bin/sh\n/usr/bin/touch '{}'\n", marker.display()),
    )
    .expect("fake executable");
    let mut permissions = std::fs::metadata(&executable)
        .expect("fake executable metadata")
        .permissions();
    permissions.set_mode(0o700);
    std::fs::set_permissions(&executable, permissions).expect("fake executable permissions");
    let missing_input = temporary.path().join("missing-live-input.json");

    for command in ["run-current-ao-baseline", "run-live", "run-direct-baseline"] {
        for gate in [None, Some("wrong")] {
            let output = run_with_live_environment(
                &[command, "--input", missing_input.to_str().expect("path")],
                temporary.path(),
                gate,
            );
            assert_json_error(&output, 8, "authorization_denied");
            assert!(
                !marker.exists(),
                "unauthorized live command started a child"
            );
        }
    }

    for gate in [None, Some("wrong")] {
        let output = run_with_live_environment(
            &[
                "evaluate-live",
                "--comparison",
                missing_input.to_str().expect("path"),
            ],
            temporary.path(),
            gate,
        );
        assert_json_error(&output, 8, "authorization_denied");
        assert!(
            !marker.exists(),
            "unauthorized live evaluator started a child"
        );
    }

    let output = run_with_live_environment(
        &[
            "preflight-live-input",
            "--input",
            missing_input.to_str().expect("path"),
            "--variant",
            "n0",
        ],
        temporary.path(),
        Some("operator-authorized"),
    );
    assert_json_error(&output, 8, "authorization_denied");
    let output = run(&[
        "preflight-live-input",
        "--input",
        missing_input.to_str().expect("path"),
        "--variant",
        "n0",
    ]);
    assert_json_error(&output, 2, "usage");
}

#[test]
fn live_commands_require_operator_owned_corpus_and_verifier_anchors() {
    let temporary = TempDir::new().expect("temporary");
    let missing_input = temporary.path().join("missing-live-input.json");
    for command in ["run-current-ao-baseline", "run-live", "run-direct-baseline"] {
        let output = run_with_live_environment(
            &[command, "--input", missing_input.to_str().expect("path")],
            temporary.path(),
            Some("operator-authorized"),
        );
        assert_json_error(&output, 2, "usage");
    }
    let output = run(&[
        "preflight-live-input",
        "--input",
        missing_input.to_str().expect("path"),
        "--variant",
        "n7",
    ]);
    assert_json_error(&output, 2, "usage");
}

fn sealed_corpus() -> CorpusManifest {
    let variants = [
        (ExecutionVariant::N0, "current-ao", "current-ao-native-v1"),
        (ExecutionVariant::N4, "codex", "native-codex-direct-v1"),
        (ExecutionVariant::N7, "ao-next-codex", "ao-next-process-v1"),
    ];
    let tasks = [
        "greenfield-engineering-app",
        "bounded-defect-repair",
        "artifact-reconciliation",
    ]
    .into_iter()
    .map(|task_id| EvaluationTask {
        task_id: task_id.into(),
        task_kind: "sealed-local-task".into(),
        source_digest: digest_bytes(format!("{task_id}:source").as_bytes()),
        objective_digest: digest_bytes(format!("{task_id}:objective").as_bytes()),
        workspace_seed_digest: digest_bytes(format!("{task_id}:seed").as_bytes()),
        visible_fixtures_digest: digest_bytes(format!("{task_id}:visible").as_bytes()),
        hidden_tests_digest: digest_bytes(format!("{task_id}:hidden").as_bytes()),
        verifier_profile_digest: digest_bytes(format!("{task_id}:verifier").as_bytes()),
        variant_profiles: variants
            .iter()
            .map(|(variant, runtime, adapter)| VariantProfile {
                variant: *variant,
                runtime: (*runtime).into(),
                runtime_digest: digest_bytes(format!("{task_id}:{runtime}:runtime").as_bytes()),
                model_identifier: "operator-selected-live-model".into(),
                model_digest: digest_bytes(format!("{task_id}:{runtime}:model").as_bytes()),
                prompt_digest: digest_bytes(format!("{task_id}:{runtime}:prompt").as_bytes()),
                policy_digest: digest_bytes(format!("{task_id}:{runtime}:policy").as_bytes()),
                adapter_version: (*adapter).into(),
                adapter_digest: digest_bytes(format!("{task_id}:{runtime}:adapter").as_bytes()),
            })
            .collect(),
    })
    .collect();
    let mut corpus = CorpusManifest {
        schema_version: "ao.next.evaluation-corpus.v2".into(),
        corpus_kind: CorpusKind::SealedLive,
        corpus_digest: digest_bytes(b"unsealed-live-corpus"),
        required_trial_count: 3,
        schedule: counterbalanced_schedule(),
        tasks,
    };
    corpus.corpus_digest = corpus.calculated_digest().expect("corpus digest");
    corpus
}

struct PrepareLiveFixture {
    _root: TempDir,
    input_path: PathBuf,
    input: serde_json::Value,
    source_snapshot: PathBuf,
    workspace: PathBuf,
    corpus_digest: Digest,
    verifier_digest: Digest,
    provider_marker: PathBuf,
}

fn prepare_live_profile(
    variant: ExecutionVariant,
    runtime: &str,
    adapter_version: &str,
) -> VariantProfile {
    VariantProfile {
        variant,
        runtime: runtime.into(),
        runtime_digest: digest_bytes(format!("{runtime}:runtime").as_bytes()),
        model_identifier: "operator-selected-live-model".into(),
        model_digest: canonical_digest(&("operator-selected-live-model", "high"))
            .expect("model digest"),
        prompt_digest: digest_bytes(format!("{runtime}:prompt").as_bytes()),
        policy_digest: digest_bytes(format!("{runtime}:policy").as_bytes()),
        adapter_version: adapter_version.into(),
        adapter_digest: digest_bytes(format!("{runtime}:{adapter_version}").as_bytes()),
    }
}

fn prepare_live_placeholder_task(task_id: &str, profiles: &[VariantProfile]) -> EvaluationTask {
    EvaluationTask {
        task_id: task_id.into(),
        task_kind: "sealed_local_task".into(),
        source_digest: digest_bytes(format!("{task_id}:source").as_bytes()),
        objective_digest: digest_bytes(format!("{task_id}:objective").as_bytes()),
        workspace_seed_digest: digest_bytes(format!("{task_id}:workspace").as_bytes()),
        visible_fixtures_digest: digest_bytes(format!("{task_id}:visible").as_bytes()),
        hidden_tests_digest: digest_bytes(format!("{task_id}:hidden").as_bytes()),
        verifier_profile_digest: digest_bytes(format!("{task_id}:verifier").as_bytes()),
        variant_profiles: profiles.to_vec(),
    }
}

#[allow(
    clippy::too_many_lines,
    reason = "the CLI fixture keeps every sealed identity explicit and internally consistent"
)]
fn prepare_live_fixture() -> PrepareLiveFixture {
    let root = TempDir::new().expect("temporary");
    let workspace = root.path().join("workspace");
    let protected = root.path().join("protected");
    let visible = protected.join("visible");
    let hidden = protected.join("hidden");
    let raw_capture_root = protected.join("raw-captures");
    std::fs::create_dir(&workspace).expect("workspace");
    std::fs::create_dir_all(&visible).expect("visible fixtures");
    std::fs::create_dir(&hidden).expect("hidden tests");
    std::fs::create_dir(&raw_capture_root).expect("capture root");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt as _;
        std::fs::set_permissions(&raw_capture_root, std::fs::Permissions::from_mode(0o700))
            .expect("private capture root");
    }

    let objective_text = "Prepare the sealed live workspace.";
    let objective = protected.join("objective.md");
    std::fs::write(&objective, objective_text).expect("objective");
    let output_schema = protected.join("adapter-turn.schema.json");
    std::fs::write(
        &output_schema,
        include_bytes!("../../../docs/contracts/adapter-turn-v1.schema.json"),
    )
    .expect("output schema");

    let empty_tree = Vec::<serde_json::Value>::new();
    let workspace_digest = canonical_digest(&empty_tree).expect("empty workspace digest");
    let source = serde_json::json!({
        "schema_version": "ao.next.source-snapshot.v1",
        "task_id": "greenfield-engineering-app",
        "tree_digest": workspace_digest,
        "files": []
    });
    let source_digest = canonical_digest(&source).expect("source digest");
    let source_snapshot = protected.join("source-snapshot.json");
    write_json(&source_snapshot, &source);
    let visible_digest = canonical_digest(&empty_tree).expect("visible digest");
    let hidden_digest = canonical_digest(&empty_tree).expect("hidden digest");

    let mut verifier_entry = CommandVerifierEntry {
        verifier_id: "sealed-prepare-check".into(),
        verifier_digest: digest_bytes(b"unsealed verifier entry"),
        program: "git".into(),
        args: vec!["status".into(), "--short".into()],
        working_directory: PathBuf::new(),
        timeout_ms: 5_000,
        max_output_bytes: 16 * 1024,
        expected_exit_status: 0,
        required_artifacts: Vec::new(),
    };
    verifier_entry.verifier_digest = verifier_entry
        .calculated_digest()
        .expect("verifier entry digest");
    let mut command_verifier = CommandVerifierProfile {
        schema_version: "ao.next.command-verifier-profile.v1".into(),
        profile_id: "sealed-prepare-verifier-v1".into(),
        profile_digest: digest_bytes(b"unsealed verifier profile"),
        entries: vec![verifier_entry.clone()],
    };
    command_verifier.profile_digest = command_verifier
        .calculated_digest()
        .expect("verifier profile digest");

    let profiles = vec![
        prepare_live_profile(ExecutionVariant::N0, "current-ao", "current-ao-native-v1"),
        prepare_live_profile(ExecutionVariant::N4, "codex", "native-codex-direct-v1"),
        prepare_live_profile(ExecutionVariant::N7, "ao-next-codex", "ao-next-process-v1"),
    ];
    let selected_profile = profiles
        .iter()
        .find(|profile| profile.variant == ExecutionVariant::N7)
        .expect("N7 profile")
        .clone();
    let selected_task = EvaluationTask {
        task_id: "greenfield-engineering-app".into(),
        task_kind: "greenfield_engineering_application".into(),
        source_digest: source_digest.clone(),
        objective_digest: digest_bytes(objective_text.as_bytes()),
        workspace_seed_digest: workspace_digest.clone(),
        visible_fixtures_digest: visible_digest,
        hidden_tests_digest: hidden_digest,
        verifier_profile_digest: command_verifier.profile_digest.clone(),
        variant_profiles: profiles.clone(),
    };
    let mut corpus = CorpusManifest {
        schema_version: "ao.next.evaluation-corpus.v2".into(),
        corpus_kind: CorpusKind::SealedLive,
        corpus_digest: digest_bytes(b"unsealed prepare corpus"),
        required_trial_count: 3,
        schedule: counterbalanced_schedule(),
        tasks: vec![
            selected_task,
            prepare_live_placeholder_task("bounded-defect-repair", &profiles),
            prepare_live_placeholder_task("artifact-reconciliation", &profiles),
        ],
    };
    corpus.corpus_digest = corpus.calculated_digest().expect("corpus digest");

    let now = Utc::now();
    let request = RunRequest {
        schema_version: "ao.next.run-request.v1".into(),
        run_id: "prepare-live-run-01".into(),
        objective: objective_text.into(),
        source: SourceIdentity {
            repository: "sealed-local/greenfield-engineering-app".into(),
            head: source_digest,
        },
        workspace: WorkspaceIdentity {
            workspace_id: "prepare-live-workspace-01".into(),
            root: workspace.clone(),
            seed_digest: workspace_digest,
        },
        model_profile: ModelProfile {
            runtime: "codex".into(),
            model_identifier: selected_profile.model_identifier.clone(),
            reasoning_effort: "high".into(),
            system_prompt_digest: selected_profile.prompt_digest.clone(),
            tool_contract_digest: selected_profile.adapter_digest.clone(),
            context_limit: 262_144,
            output_limit: 20_000,
            adapter_version: selected_profile.adapter_version.clone(),
        },
        authority: AuthorityEnvelope {
            schema_version: "ao.next.authority-envelope.v1".into(),
            issued_by: "operator".into(),
            issued_at: now - Duration::minutes(1),
            expires_at: now + Duration::hours(1),
            capabilities: BTreeSet::from([Capability::ReadWorkspace, Capability::WriteWorkspace]),
            allowed_roots: vec![workspace.clone()],
            allowed_programs: BTreeSet::new(),
            network: NetworkPolicy::Denied,
            allowed_network_hosts: BTreeSet::new(),
            external_effects: ExternalEffectPolicy::Denied,
        },
        verifier_profile: VerifierProfile {
            profile_id: command_verifier.profile_id.clone(),
            profile_digest: command_verifier.profile_digest.clone(),
            commands: vec![StructuredCommand {
                program: verifier_entry.program,
                args: verifier_entry.args,
                timeout_ms: verifier_entry.timeout_ms,
            }],
            required_artifacts: Vec::new(),
        },
        policy_digest: selected_profile.policy_digest,
        limits: RunLimits {
            max_input_bytes: 64 * 1024,
            max_turns: 1,
            max_repair_attempts: 0,
            max_run_ms: 10_000,
            max_effect_timeout_ms: 5_000,
            max_output_bytes: 64 * 1024,
            max_tokens: 564_288,
        },
    };
    let input = serde_json::json!({
        "schema_version": "ao.next.live-run-input.v1",
        "corpus": corpus,
        "task_id": "greenfield-engineering-app",
        "trial_id": "prepare-live-trial-01",
        "trial_index": 0,
        "schedule_position": 2,
        "workspace_instance_id": request.workspace.workspace_id,
        "source_snapshot": source_snapshot,
        "objective": objective,
        "visible_fixtures": visible,
        "hidden_tests": hidden,
        "output_schema": output_schema,
        "raw_capture_root": raw_capture_root,
        "request": request,
        "command_verifier": command_verifier
    });
    let input_path = protected.join("live-input.json");
    write_json(&input_path, &input);
    let input_path = std::fs::canonicalize(input_path).expect("canonical live input path");
    let corpus_digest = Digest::new(
        input["corpus"]["corpus_digest"]
            .as_str()
            .expect("corpus digest"),
    )
    .expect("corpus digest");
    let verifier_digest = Digest::new(
        input["command_verifier"]["profile_digest"]
            .as_str()
            .expect("verifier digest"),
    )
    .expect("verifier digest");
    PrepareLiveFixture {
        provider_marker: protected.join("provider-started"),
        _root: root,
        input_path,
        input,
        source_snapshot,
        workspace,
        corpus_digest,
        verifier_digest,
    }
}

fn run_prepare_live(
    fixture: &PrepareLiveFixture,
    receipt_path: &Path,
    corpus_digest: &str,
    verifier_digest: &str,
) -> Output {
    run_without_live_authority(&[
        "prepare-live",
        "--input",
        fixture.input_path.to_str().expect("input path"),
        "--trusted-corpus-digest",
        corpus_digest,
        "--trusted-verifier-profile-digest",
        verifier_digest,
        "--out",
        receipt_path.to_str().expect("receipt path"),
    ])
}

fn git_head(workspace: &Path) -> String {
    let output = Command::new("git")
        .args(["rev-parse", "HEAD"])
        .current_dir(workspace)
        .output()
        .expect("read Git HEAD");
    assert!(output.status.success());
    String::from_utf8(output.stdout)
        .expect("UTF-8 Git HEAD")
        .trim()
        .to_owned()
}

#[test]
fn prepare_live_emits_actual_git_and_journal_identity_without_a_provider() {
    let fixture = prepare_live_fixture();
    let receipt_path = fixture.input_path.with_file_name("prepared-run.json");
    let output = run_prepare_live(
        &fixture,
        &receipt_path,
        fixture.corpus_digest.as_str(),
        fixture.verifier_digest.as_str(),
    );
    assert_eq!(
        output.status.code(),
        Some(0),
        "stdout={} stderr={}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(fixture.workspace.join(".git").is_dir());
    let receipt_bytes = std::fs::read(&receipt_path).expect("receipt bytes");
    let receipt: PreparedRunReceipt = serde_json::from_slice(&receipt_bytes).expect("receipt");
    let stdout_receipt: PreparedRunReceipt =
        serde_json::from_slice(&output.stdout).expect("stdout receipt");
    assert_eq!(
        receipt_bytes,
        canonical_json_bytes(&receipt).expect("canonical receipt")
    );
    assert_eq!(stdout_receipt, receipt);
    assert_eq!(
        receipt.repository_root,
        std::fs::canonicalize(&fixture.workspace).expect("canonical workspace")
    );
    assert_eq!(
        std::fs::canonicalize(&receipt.common_directory).expect("canonical receipt Git directory"),
        std::fs::canonicalize(fixture.workspace.join(".git")).expect("canonical Git directory")
    );
    assert_eq!(receipt.branch, "ao-next-sealed-seed");
    assert_eq!(receipt.base_commit, git_head(&fixture.workspace));
    assert_eq!(
        receipt.input_digest,
        digest_bytes(&std::fs::read(&fixture.input_path).expect("input bytes"))
    );
    assert_eq!(
        receipt.request_digest,
        canonical_digest(&fixture.input["request"]).expect("request digest")
    );
    assert_eq!(
        receipt.workspace_digest.as_str(),
        fixture.input["request"]["workspace"]["seed_digest"]
            .as_str()
            .expect("workspace digest")
    );
    assert_eq!(receipt.provider_calls, 0);
    assert!(!receipt.safe_to_execute);
    assert!(!fixture.provider_marker.exists());
    let journal_identity = PathBuf::from(format!(
        "{}.journal/execution-identity.json",
        fixture.input["raw_capture_root"]
            .as_str()
            .expect("capture root")
    ));
    assert!(journal_identity.is_file());
    let journal_identity_value: serde_json::Value =
        serde_json::from_slice(&std::fs::read(&journal_identity).expect("journal identity bytes"))
            .expect("journal identity");
    assert_eq!(
        receipt.journal_identity_digest,
        canonical_digest(&journal_identity_value).expect("journal identity digest")
    );
    assert!(!journal_identity.with_file_name("execution-events").exists());
}

#[test]
fn prepare_live_rejects_existing_output_git_input_source_and_anchor_drift() {
    let fixture = prepare_live_fixture();
    let receipt_path = fixture.input_path.with_file_name("existing.json");
    std::fs::write(&receipt_path, b"existing").expect("existing receipt");
    assert_json_error(
        &run_prepare_live(
            &fixture,
            &receipt_path,
            fixture.corpus_digest.as_str(),
            fixture.verifier_digest.as_str(),
        ),
        3,
        "invalid_input",
    );
    assert!(!fixture.workspace.join(".git").exists());

    let fixture = prepare_live_fixture();
    std::fs::create_dir(fixture.workspace.join(".git")).expect("preexisting Git metadata");
    assert_json_error(
        &run_prepare_live(
            &fixture,
            &fixture.input_path.with_file_name("git-drift.json"),
            fixture.corpus_digest.as_str(),
            fixture.verifier_digest.as_str(),
        ),
        3,
        "invalid_input",
    );

    let mut fixture = prepare_live_fixture();
    fixture.input["workspace_instance_id"] = serde_json::json!("drifted-workspace");
    write_json(&fixture.input_path, &fixture.input);
    assert_json_error(
        &run_prepare_live(
            &fixture,
            &fixture.input_path.with_file_name("input-drift.json"),
            fixture.corpus_digest.as_str(),
            fixture.verifier_digest.as_str(),
        ),
        3,
        "invalid_input",
    );

    let fixture = prepare_live_fixture();
    std::fs::write(
        &fixture.source_snapshot,
        br#"{"schema_version":"ao.next.source-snapshot.v1","task_id":"drifted","tree_digest":"sha256:4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e1bb663c2b8973f4842d2bc","files":[]}"#,
    )
    .expect("drifted source");
    assert_json_error(
        &run_prepare_live(
            &fixture,
            &fixture.input_path.with_file_name("source-drift.json"),
            fixture.corpus_digest.as_str(),
            fixture.verifier_digest.as_str(),
        ),
        3,
        "invalid_input",
    );

    let fixture = prepare_live_fixture();
    assert_json_error(
        &run_prepare_live(
            &fixture,
            &fixture.input_path.with_file_name("corpus-drift.json"),
            ZERO_DIGEST,
            fixture.verifier_digest.as_str(),
        ),
        3,
        "invalid_input",
    );
    assert_json_error(
        &run_prepare_live(
            &fixture,
            &fixture.input_path.with_file_name("verifier-drift.json"),
            fixture.corpus_digest.as_str(),
            ONE_DIGEST,
        ),
        3,
        "invalid_input",
    );
    assert!(!fixture.workspace.join(".git").exists());
}

#[test]
fn prepare_live_rejects_provider_authorization_before_preparation() {
    let fixture = prepare_live_fixture();
    let receipt_path = fixture.input_path.with_file_name("authorized.json");
    let output = Command::new(env!("CARGO_BIN_EXE_ao-next"))
        .args([
            "prepare-live",
            "--input",
            fixture.input_path.to_str().expect("input path"),
            "--trusted-corpus-digest",
            fixture.corpus_digest.as_str(),
            "--trusted-verifier-profile-digest",
            fixture.verifier_digest.as_str(),
            "--out",
            receipt_path.to_str().expect("receipt path"),
        ])
        .env("AO_NEXT_LIVE_PROVIDER_CALLS", "operator-authorized")
        .output()
        .expect("run authorized prepare-live");
    assert_json_error(&output, 8, "authorization_denied");
    assert!(!receipt_path.exists());
    assert!(!fixture.workspace.join(".git").exists());
    assert!(!fixture.provider_marker.exists());
}

#[test]
fn prepare_live_rejects_any_preexisting_journal_event() {
    let fixture = prepare_live_fixture();
    let request: RunRequest =
        serde_json::from_value(fixture.input["request"].clone()).expect("request");
    let journal_root = PathBuf::from(format!(
        "{}.journal",
        fixture.input["raw_capture_root"]
            .as_str()
            .expect("capture root")
    ));
    let maximum_bytes = request
        .limits
        .max_input_bytes
        .saturating_add(request.limits.max_output_bytes)
        .max(64 * 1024);
    let journal = CheckpointJournal::new(journal_root, maximum_bytes).expect("journal");
    journal
        .record_provider_request_intent(&request, &digest_bytes(b"prepared"))
        .expect("provider event");
    let receipt_path = fixture.input_path.with_file_name("eventful-journal.json");

    assert_json_error(
        &run_prepare_live(
            &fixture,
            &receipt_path,
            fixture.corpus_digest.as_str(),
            fixture.verifier_digest.as_str(),
        ),
        7,
        "evidence_failure",
    );
    assert!(!receipt_path.exists());
    assert!(!fixture.provider_marker.exists());
}

#[test]
fn prepare_live_rejects_a_symlinked_output() {
    let fixture = prepare_live_fixture();
    let target = fixture.input_path.with_file_name("receipt-target.json");
    let receipt_path = fixture.input_path.with_file_name("receipt-link.json");
    std::fs::write(&target, b"target").expect("symlink target");
    #[cfg(unix)]
    std::os::unix::fs::symlink(&target, &receipt_path).expect("symlink output");
    #[cfg(windows)]
    match std::os::windows::fs::symlink_file(&target, &receipt_path) {
        Ok(()) => {}
        Err(error)
            if error.kind() == std::io::ErrorKind::PermissionDenied
                || error.raw_os_error() == Some(1314) =>
        {
            return;
        }
        Err(error) => panic!("symlink output: {error}"),
    }
    assert_json_error(
        &run_prepare_live(
            &fixture,
            &receipt_path,
            fixture.corpus_digest.as_str(),
            fixture.verifier_digest.as_str(),
        ),
        3,
        "invalid_input",
    );
    assert_eq!(std::fs::read(&target).expect("unchanged target"), b"target");
    assert!(!fixture.workspace.join(".git").exists());
}

#[test]
fn prepare_live_rejects_a_symlinked_output_ancestor() {
    let fixture = prepare_live_fixture();
    let protected = fixture.input_path.parent().expect("protected root");
    let real_ancestor = protected.join("real-output-root");
    let real_parent = real_ancestor.join("nested");
    let linked_ancestor = protected.join("linked-output-root");
    std::fs::create_dir_all(&real_parent).expect("real output parent");
    #[cfg(unix)]
    std::os::unix::fs::symlink(&real_ancestor, &linked_ancestor).expect("symlink ancestor");
    #[cfg(windows)]
    match std::os::windows::fs::symlink_dir(&real_ancestor, &linked_ancestor) {
        Ok(()) => {}
        Err(error)
            if error.kind() == std::io::ErrorKind::PermissionDenied
                || error.raw_os_error() == Some(1314) =>
        {
            return;
        }
        Err(error) => panic!("symlink ancestor: {error}"),
    }
    let receipt_path = linked_ancestor.join("nested/prepared-run.json");

    assert_json_error(
        &run_prepare_live(
            &fixture,
            &receipt_path,
            fixture.corpus_digest.as_str(),
            fixture.verifier_digest.as_str(),
        ),
        3,
        "invalid_input",
    );
    assert!(!real_parent.join("prepared-run.json").exists());
    assert!(!fixture.workspace.join(".git").exists());
}

fn qualify_campaign(path: &Path) -> Output {
    #[cfg(unix)]
    let fake_program = Path::new("/usr/bin/true");
    #[cfg(windows)]
    let fake_program = Path::new(env!("CARGO_BIN_EXE_ao-next"));
    let fake_digest = digest_bytes(&std::fs::read(fake_program).expect("fake program bytes"));
    run(&[
        "qualify-live-campaign",
        "--qualification",
        path.to_str().expect("path"),
        "--trusted-corpus-digest",
        ZERO_DIGEST,
        "--trusted-verifier-profile",
        &format!("greenfield-engineering-app={ONE_DIGEST}"),
        "--trusted-verifier-profile",
        &format!("bounded-defect-repair={ONE_DIGEST}"),
        "--trusted-verifier-profile",
        &format!("artifact-reconciliation={ONE_DIGEST}"),
        "--fake-provider-program",
        fake_program.to_str().expect("fake program path"),
        "--fake-provider-program-digest",
        fake_digest.as_str(),
    ])
}

#[test]
fn campaign_rejects_caller_authored_attestations_without_executing_a_fake_process() {
    let temporary = TempDir::new().expect("temporary");
    let path = temporary.path().join("qualification.json");
    write_json(
        &path,
        &serde_json::json!({
            "schema_version":"ao.next.provider-free-campaign-qualification.v1",
            "rows":27,
            "negative_mutations":[{"name":"duplicate-top-level-key","rejected":true}],
            "provider_processes":27,
            "provider_calls":0
        }),
    );
    let output = qualify_campaign(&path);
    assert_json_error(&output, 3, "invalid_input");
}

#[test]
fn campaign_parser_rejects_malformed_and_over_one_mib_inputs_separately() {
    let temporary = TempDir::new().expect("temporary");
    let malformed = temporary.path().join("malformed.json");
    std::fs::write(&malformed, b"{").expect("malformed input");
    assert_json_error(&qualify_campaign(&malformed), 3, "invalid_input");

    let oversized = temporary.path().join("oversized.json");
    std::fs::write(&oversized, vec![b' '; 1024 * 1024 + 1]).expect("oversized input");
    assert_json_error(&qualify_campaign(&oversized), 3, "invalid_input");
}

#[test]
fn verify_corpus_accepts_a_digest_bound_live_manifest() {
    let temporary = TempDir::new().expect("temporary");
    let corpus = sealed_corpus();
    let path = temporary.path().join("corpus.json");
    write_json(&path, &corpus);

    let output = run(&["verify-corpus", "--corpus", path.to_str().expect("path")]);
    assert_eq!(output.status.code(), Some(0));
    let value: serde_json::Value =
        serde_json::from_slice(&output.stdout).expect("machine JSON output");
    assert_eq!(value["schema_version"], "ao.next.corpus-verification.v1");
    assert_eq!(value["corpus_digest"], corpus.corpus_digest.as_str());
    assert_eq!(value["task_count"], 3);
    assert_eq!(value["required_trial_count"], 3);
    assert_eq!(value["live_eligible"], true);
}

#[test]
fn instantiate_corpus_binds_exact_model_effort_and_adapter_identities() {
    let temporary = TempDir::new().expect("temporary");
    let corpus = sealed_corpus();
    let corpus_path = temporary.path().join("corpus.json");
    let bindings_path = temporary.path().join("bindings.json");
    write_json(&corpus_path, &corpus);
    write_json(
        &bindings_path,
        &serde_json::json!({
            "schema_version": "ao.next.live-corpus-bindings.v1",
            "model_identifier": "gpt-5.6-sol",
            "reasoning_effort": "xhigh",
            "variants": [
                {
                    "variant": "N0",
                    "runtime": "current-ao",
                    "runtime_digest": digest_bytes(b"current-ao@3309137"),
                    "adapter_version": "current-ao-native-v1+3309137",
                    "adapter_digest": digest_bytes(b"current-ao-binding")
                },
                {
                    "variant": "N4",
                    "runtime": "codex",
                    "runtime_digest": digest_bytes(b"codex-cli@0.146.0"),
                    "adapter_version": "native-codex-direct-v1+0.146.0",
                    "adapter_digest": digest_bytes(b"native-codex-binding")
                },
                {
                    "variant": "N7",
                    "runtime": "ao-next-codex",
                    "runtime_digest": digest_bytes(b"ao-next@repair+codex-cli@0.146.0"),
                    "adapter_version": "ao-next-process-v1+0.146.0",
                    "adapter_digest": digest_bytes(b"ao-next-process-binding")
                }
            ]
        }),
    );
    let output = run(&[
        "instantiate-corpus",
        "--corpus",
        corpus_path.to_str().expect("path"),
        "--bindings",
        bindings_path.to_str().expect("path"),
    ]);
    assert_eq!(output.status.code(), Some(0));
    let value: serde_json::Value =
        serde_json::from_slice(&output.stdout).expect("instantiation JSON");
    assert_eq!(value["parent_corpus_digest"], corpus.corpus_digest.as_str());
    assert_eq!(value["model_identifier"], "gpt-5.6-sol");
    assert_eq!(value["reasoning_effort"], "xhigh");
    assert!(
        value["corpus"]["tasks"]
            .as_array()
            .expect("tasks")
            .iter()
            .flat_map(|task| task["variant_profiles"].as_array().expect("profiles"))
            .all(|profile| profile["model_identifier"] == "gpt-5.6-sol")
    );
}

#[test]
fn evaluate_live_cli_requires_authority_and_reaches_only_live_decision_path() {
    let temporary = TempDir::new().expect("temporary");
    let corpus = sealed_corpus();
    let mut runs: Vec<RunMeasurement> = corpus
        .tasks
        .iter()
        .flat_map(|task| {
            corpus.schedule.iter().map(|entry| {
                let mut measurement = comparison_measurement(&corpus, task, entry);
                measurement.measurement_origin = MeasurementOrigin::LiveProvider;
                measurement
            })
        })
        .collect();
    for run in &mut runs {
        run.recovery_attempted = false;
        run.recovery_no_duplicate_effect = false;
    }
    let request = ComparisonRequest {
        schema_version: "ao.next.comparison-request.v2".into(),
        corpus,
        runs,
    };
    let path = temporary.path().join("live-comparison.json");
    write_json(&path, &request);
    let output = run_with_live_environment(
        &[
            "evaluate-live",
            "--comparison",
            path.to_str().expect("path"),
        ],
        temporary.path(),
        Some("operator-authorized"),
    );
    assert_eq!(output.status.code(), Some(0));
    let live: serde_json::Value = serde_json::from_slice(&output.stdout).expect("live comparison");
    assert_eq!(live["decision"], "AO_NEXT_NOT_YET_SUPERIOR");

    let output = run_with_live_environment(
        &[
            "evaluate-live",
            "--comparison",
            path.to_str().expect("path"),
            "--recovery-evidence-root",
            temporary
                .path()
                .join("live-recovery")
                .to_str()
                .expect("recovery root"),
        ],
        temporary.path(),
        Some("operator-authorized"),
    );
    assert_eq!(
        output.status.code(),
        Some(0),
        "stdout={} stderr={}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    let live: serde_json::Value = serde_json::from_slice(&output.stdout).expect("live comparison");
    assert_eq!(live["decision"], "AO_NEXT_LIVE_EVALUATION_PASSED");

    let output = run(&["evaluate", "--comparison", path.to_str().expect("path")]);
    assert_eq!(output.status.code(), Some(0));
    let offline: serde_json::Value =
        serde_json::from_slice(&output.stdout).expect("offline comparison");
    assert_eq!(offline["decision"], "AO_NEXT_NOT_YET_SUPERIOR");

    let output = run(&[
        "evaluate",
        "--comparison",
        path.to_str().expect("path"),
        "--recovery-evidence-root",
        temporary
            .path()
            .join("offline-recovery")
            .to_str()
            .expect("recovery root"),
    ]);
    assert_eq!(output.status.code(), Some(0));
    let offline: serde_json::Value =
        serde_json::from_slice(&output.stdout).expect("offline comparison");
    assert_eq!(offline["decision"], "AO_NEXT_READY_FOR_LIVE_EVALUATION");
    assert!(offline["recovery_qualification_digest"].is_string());
}
