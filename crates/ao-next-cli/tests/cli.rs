use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};

use ao_next_core::contracts::{
    AdapterIdentity, Digest, RunState, SourceIdentity, TerminalReadback, WorkspaceIdentity,
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
    let task = serde_json::json!({
        "task_id": "cli-evaluation",
        "task_kind": "bounded_public_defect_repair",
        "source_digest": ZERO_DIGEST,
        "objective_digest": ONE_DIGEST,
        "workspace_seed_digest": ZERO_DIGEST,
        "visible_fixtures_digest": ONE_DIGEST,
        "hidden_tests_digest": ZERO_DIGEST,
        "verifier_profile_digest": ONE_DIGEST,
        "variant_profiles": [
            {
                "variant": "N0",
                "runtime": "N0",
                "model_identifier": "fixture-model",
                "prompt_digest": ONE_DIGEST,
                "policy_digest": ZERO_DIGEST,
                "adapter_version": "fixture-v1"
            },
            {
                "variant": "N4",
                "runtime": "N4",
                "model_identifier": "fixture-model",
                "prompt_digest": ONE_DIGEST,
                "policy_digest": ZERO_DIGEST,
                "adapter_version": "fixture-v1"
            },
            {
                "variant": "N7",
                "runtime": "N7",
                "model_identifier": "fixture-model",
                "prompt_digest": ONE_DIGEST,
                "policy_digest": ZERO_DIGEST,
                "adapter_version": "fixture-v1"
            }
        ]
    });
    let corpus_digest =
        ao_next_core::strict_json::canonical_digest(&vec![task.clone()]).expect("corpus digest");
    let measurement = |variant: &str, tokens: u64, wall_clock_ms: u64| {
        serde_json::json!({
            "schema_version": "ao.next.run-measurement.v1",
            "corpus_digest": corpus_digest,
            "task_id": "cli-evaluation",
            "variant": variant,
            "source_digest": ZERO_DIGEST,
            "objective_digest": ONE_DIGEST,
            "workspace_seed_digest": ZERO_DIGEST,
            "visible_fixtures_digest": ONE_DIGEST,
            "hidden_tests_digest": ZERO_DIGEST,
            "verifier_profile_digest": ONE_DIGEST,
            "runtime": variant,
            "model_identifier": "fixture-model",
            "prompt_digest": ONE_DIGEST,
            "policy_digest": ZERO_DIGEST,
            "adapter_version": "fixture-v1",
            "tokens": {
                "input_tokens": tokens,
                "cached_input_tokens": 0,
                "reasoning_tokens": 0,
                "output_tokens": 0,
                "reported_total_tokens": tokens
            },
            "wall_clock_ms": wall_clock_ms,
            "model_wait_ms": wall_clock_ms / 2,
            "worker_turns": 1,
            "repair_attempts": 0,
            "operator_interventions": 0,
            "changed_files": 1,
            "accepted_changed_files": 1,
            "task_success": true,
            "hidden_tests_passed": 10,
            "hidden_tests_total": 10,
            "regressions": 0,
            "unauthorized_effects": 0,
            "evidence_complete": true,
            "evidence_digest_valid": true,
            "recovery_attempted": variant == "N7",
            "recovery_no_duplicate_effect": true,
            "cross_runtime_agreement": true,
            "worker_count": 1,
            "dynamic_fanout": false
        })
    };
    serde_json::json!({
        "schema_version": "ao.next.comparison-request.v1",
        "corpus": {
            "schema_version": "ao.next.evaluation-corpus.v1",
            "corpus_digest": corpus_digest,
            "tasks": [task]
        },
        "runs": [
            measurement("N0", 400, 400),
            measurement("N4", 100, 200),
            measurement("N7", 110, 250)
        ]
    })
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

    for command in ["run-live", "run-direct-baseline"] {
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
}
