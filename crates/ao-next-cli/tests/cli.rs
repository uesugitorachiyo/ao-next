use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};

use ao_next_core::contracts::{
    AdapterIdentity, Digest, RunState, SourceIdentity, TerminalReadback, WorkspaceIdentity,
};
use ao_next_core::evidence::digest_bytes;
use ao_next_eval::comparison::ComparisonRequest;
use ao_next_eval::corpus::{
    CorpusKind, CorpusManifest, EvaluationTask, ScheduleEntry, VariantProfile,
    counterbalanced_schedule,
};
use ao_next_eval::metrics::{ExecutionVariant, MeasurementOrigin, RunMeasurement, TokenRow};
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
        recovery_qualification: None,
        recovery_qualification_digest: None,
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

fn qualify_campaign(path: &Path) -> Output {
    let fake_program = Path::new("/usr/bin/true");
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
    let runs = corpus
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
    let request = ComparisonRequest {
        schema_version: "ao.next.comparison-request.v2".into(),
        corpus,
        runs,
        recovery_qualification: None,
        recovery_qualification_digest: None,
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
    assert_eq!(live["decision"], "AO_NEXT_LIVE_EVALUATION_PASSED");

    let output = run(&["evaluate", "--comparison", path.to_str().expect("path")]);
    assert_eq!(output.status.code(), Some(0));
    let offline: serde_json::Value =
        serde_json::from_slice(&output.stdout).expect("offline comparison");
    assert_eq!(offline["decision"], "AO_NEXT_READY_FOR_LIVE_EVALUATION");
}
