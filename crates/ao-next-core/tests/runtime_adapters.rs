use std::path::Path;
use std::thread;
use std::time::Duration;

use ao_next_core::adapter::claude;
use ao_next_core::adapter::codex;
use ao_next_core::adapter::{
    AdapterIdentity, AdapterTurn, CancellationToken, InvocationError, InvocationLimits,
    PreparedInvocation, execute_bounded, live_adapter_tests_enabled,
};
use schemars::schema_for;
use tempfile::TempDir;

const CODEX_HELP: &[u8] =
    include_bytes!("../../../tests/fixtures/adapters/codex-help-sanitized.txt");
const CLAUDE_HELP: &[u8] =
    include_bytes!("../../../tests/fixtures/adapters/claude-help-sanitized.txt");
const CODEX_EVENTS: &[u8] = include_bytes!("../../../tests/fixtures/adapters/codex-events.jsonl");
const CLAUDE_RESULT: &[u8] = include_bytes!("../../../tests/fixtures/adapters/claude-result.json");

fn limits() -> InvocationLimits {
    InvocationLimits {
        max_input_bytes: 16 * 1024,
        max_output_bytes: 16 * 1024,
        timeout_ms: 1_000,
    }
}

fn identity(runtime: &str, version: &str) -> AdapterIdentity {
    AdapterIdentity {
        runtime: runtime.into(),
        model_identifier: "fixture-model".into(),
        adapter_version: version.into(),
        worker_id: "worker-adapter-01".into(),
    }
}

#[test]
fn sanitized_help_fixtures_prove_required_offline_cli_contracts() {
    let codex = codex::parse_cli_contract(CODEX_HELP).expect("Codex contract");
    assert_eq!(codex.version, "codex-cli 0.146.0");
    assert!(codex.required_flags.contains("--json"));
    assert!(codex.required_flags.contains("--output-schema"));
    assert!(codex.required_flags.contains("--ignore-user-config"));

    let claude = claude::parse_cli_contract(CLAUDE_HELP).expect("Claude contract");
    assert_eq!(claude.version, "2.1.158 (Claude Code)");
    assert!(claude.required_flags.contains("--output-format"));
    assert!(claude.required_flags.contains("--json-schema"));
    assert!(claude.required_flags.contains("--tools"));
}

#[test]
fn invocations_are_structured_bounded_and_disable_dynamic_or_unmediated_tools() {
    let temporary = TempDir::new().expect("temporary");
    let schema = temporary.path().join("turn.schema.json");
    std::fs::write(&schema, b"{\"type\":\"object\"}").expect("schema");
    let prompt = "Return exactly one bounded adapter turn";

    let codex_invocation = codex::prepare_invocation(
        "fixture-model",
        "high",
        temporary.path(),
        &schema,
        prompt,
        limits(),
    )
    .expect("Codex invocation");
    assert_eq!(codex_invocation.program, "codex");
    assert_eq!(codex_invocation.stdin, prompt.as_bytes());
    assert_eq!(codex_invocation.cwd, temporary.path());
    assert!(codex_invocation.args.iter().any(|arg| arg == "--ephemeral"));
    assert!(
        codex_invocation
            .args
            .iter()
            .any(|arg| arg == "--ignore-user-config")
    );
    assert!(
        codex_invocation
            .args
            .iter()
            .any(|arg| arg == "--ignore-rules")
    );
    assert!(
        codex_invocation
            .args
            .windows(2)
            .any(|args| args == ["--sandbox", "read-only"])
    );
    assert!(
        codex_invocation
            .args
            .windows(2)
            .any(|args| args == ["-c", "approval_policy=\"never\""])
    );
    assert!(
        !codex_invocation
            .args
            .iter()
            .any(|arg| arg == "--ask-for-approval")
    );
    assert!(
        !codex_invocation
            .args
            .iter()
            .any(|arg| arg.contains("dangerously"))
    );
    assert!(!codex_invocation.args.iter().any(|arg| arg == prompt));

    let claude_invocation = claude::prepare_invocation(
        "fixture-model",
        "high",
        temporary.path(),
        br#"{"type":"object"}"#,
        prompt,
        limits(),
    )
    .expect("Claude invocation");
    assert_eq!(claude_invocation.program, "claude");
    assert_eq!(claude_invocation.stdin, prompt.as_bytes());
    assert!(claude_invocation.args.iter().any(|arg| arg == "--bare"));
    assert!(
        claude_invocation
            .args
            .windows(2)
            .any(|args| args == ["--tools", ""])
    );
    assert!(
        claude_invocation
            .args
            .iter()
            .any(|arg| arg == "--no-session-persistence")
    );
    assert!(
        !claude_invocation
            .args
            .iter()
            .any(|arg| arg.contains("agents"))
    );
    assert!(
        !claude_invocation
            .args
            .iter()
            .any(|arg| arg.contains("dangerously"))
    );
}

#[test]
fn codex_projects_one_of_without_mutating_the_internal_schema() {
    let temporary = TempDir::new().expect("temporary");
    let schema = temporary.path().join("turn.schema.json");
    let original = serde_json::to_vec(&schema_for!(AdapterTurn)).expect("internal schema");
    assert!(String::from_utf8_lossy(&original).contains("\"oneOf\""));
    std::fs::write(&schema, &original).expect("schema");

    let invocation = codex::prepare_invocation(
        "fixture-model",
        "high",
        temporary.path(),
        &schema,
        "prompt",
        limits(),
    )
    .expect("projected Codex invocation");
    let projected = invocation
        .args
        .windows(2)
        .find_map(|args| (args[0] == "--output-schema").then(|| Path::new(&args[1])))
        .expect("projected schema path");
    assert_ne!(projected, schema);
    assert_eq!(std::fs::read(&schema).expect("source schema"), original);

    let projected_value: serde_json::Value =
        serde_json::from_slice(&std::fs::read(projected).expect("projected schema"))
            .expect("projected schema JSON");
    let action = &projected_value["definitions"]["AdapterAction"];
    assert!(action.get("oneOf").is_none());
    assert_eq!(action["anyOf"].as_array().map(Vec::len), Some(4));
    assert!(
        action["anyOf"]
            .as_array()
            .expect("action branches")
            .iter()
            .all(|branch| branch["additionalProperties"] == false)
    );
    assert!(
        !String::from_utf8_lossy(&std::fs::read(projected).expect("projected schema"))
            .contains("\"oneOf\"")
    );
    assert_all_object_properties_required(&projected_value);

    let repeated = codex::prepare_invocation(
        "fixture-model",
        "high",
        temporary.path(),
        &schema,
        "prompt",
        limits(),
    )
    .expect("deterministic projected Codex invocation");
    assert_eq!(invocation.args, repeated.args);
}

fn assert_all_object_properties_required(schema: &serde_json::Value) {
    match schema {
        serde_json::Value::Array(values) => {
            values
                .iter()
                .for_each(assert_all_object_properties_required);
        }
        serde_json::Value::Object(object) => {
            if let Some(properties) = object
                .get("properties")
                .and_then(serde_json::Value::as_object)
            {
                let required = object
                    .get("required")
                    .and_then(serde_json::Value::as_array)
                    .expect("object schema required array");
                for property in properties.keys() {
                    assert!(
                        required.iter().any(|value| value == property),
                        "projected object omits {property:?} from required"
                    );
                }
            }
            object
                .values()
                .for_each(assert_all_object_properties_required);
        }
        _ => {}
    }
}

#[test]
fn codex_normalization_keeps_adapter_actions_strict() {
    let usage = serde_json::json!({
        "input_tokens": 1,
        "cached_input_tokens": 0,
        "reasoning_tokens": 0,
        "output_tokens": 1,
        "output_bytes": 1
    });
    let valid = serde_json::json!({
        "actions": [
            {
                "kind": "effect",
                "value": {
                    "effect_id": "effect-1",
                    "run_id": "run-1",
                    "kind": "read_file",
                    "program": null,
                    "args": [],
                    "paths": ["fixture.txt"],
                    "timeout_ms": 100,
                    "input_digest": format!("sha256:{}", "0".repeat(64))
                }
            },
            {"kind": "verify"},
            {"kind": "blocked", "value": "bounded"},
            {"kind": "interrupt"}
        ],
        "usage": usage,
        "model_claimed_success": false,
        "control_mutations": []
    });
    let encode = |turn: serde_json::Value| {
        serde_json::to_vec(&serde_json::json!({
            "type": "item.completed",
            "item": {"type": "agent_message", "text": serde_json::to_string(&turn).expect("turn")}
        }))
        .expect("event")
    };
    let normalized =
        codex::normalize_output(identity("codex", "v1"), &encode(valid.clone()), 16 * 1024)
            .expect("all action variants");
    assert_eq!(normalized.turn.actions.len(), 4);

    for invalid_action in [
        serde_json::json!({"kind": "unknown"}),
        serde_json::json!({"kind": "effect"}),
        serde_json::json!({"kind": "verify", "value": "contradiction"}),
        serde_json::json!({"kind": "interrupt", "unexpected": true}),
    ] {
        let mut invalid = valid.clone();
        invalid["actions"] = serde_json::json!([invalid_action]);
        assert!(
            codex::normalize_output(identity("codex", "v1"), &encode(invalid), 16 * 1024,).is_err()
        );
    }
}

#[test]
fn direct_codex_baseline_is_ephemeral_and_workspace_bounded() {
    let temporary = TempDir::new().expect("temporary");
    let prompt = "complete the bound objective";
    let invocation =
        codex::prepare_direct_invocation("fixed-model", "high", temporary.path(), prompt, limits())
            .expect("direct invocation");

    assert_eq!(invocation.program, "codex");
    assert!(
        invocation
            .args
            .windows(2)
            .any(|args| args == ["--sandbox", "workspace-write"])
    );
    assert!(invocation.args.iter().any(|arg| arg == "--ephemeral"));
    assert!(
        invocation
            .args
            .iter()
            .any(|arg| arg == "--ignore-user-config")
    );
    assert!(invocation.args.iter().any(|arg| arg == "--ignore-rules"));
    assert!(
        invocation
            .args
            .windows(2)
            .any(|args| args == ["-c", "approval_policy=\"never\""])
    );
    assert!(
        !invocation
            .args
            .iter()
            .any(|arg| arg == "--ask-for-approval")
    );
    assert!(
        !invocation
            .args
            .iter()
            .any(|arg| arg.contains("dangerously"))
    );
    assert_eq!(invocation.cwd, temporary.path());
    assert_eq!(invocation.stdin, prompt.as_bytes());
}

#[test]
fn codex_and_claude_outputs_normalize_to_the_same_core_turn_contract() {
    let codex = codex::normalize_output(
        identity("codex", "codex-cli-0.146.0-adapter-v1"),
        CODEX_EVENTS,
        16 * 1024,
    )
    .expect("Codex normalized output");
    let claude = claude::normalize_output(
        identity("claude", "claude-code-2.1.158-adapter-v1"),
        CLAUDE_RESULT,
        16 * 1024,
    )
    .expect("Claude normalized output");
    assert_eq!(codex.turn, claude.turn);
    assert_eq!(codex.identity.runtime, "codex");
    assert_eq!(claude.identity.runtime, "claude");
    assert_eq!(codex.identity.worker_id, claude.identity.worker_id);
}

#[test]
fn malformed_oversized_and_identity_drift_outputs_fail_closed() {
    assert!(codex::normalize_output(identity("codex", "v1"), b"{not-json\n", 1024).is_err());
    assert!(
        claude::normalize_output(identity("claude", "v1"), b"{\"type\":\"result\"}", 1024).is_err()
    );
    assert!(codex::normalize_output(identity("claude", "v1"), CODEX_EVENTS, 16 * 1024).is_err());
    assert!(claude::normalize_output(identity("codex", "v1"), CLAUDE_RESULT, 16 * 1024).is_err());
    assert!(codex::normalize_output(identity("codex", "v1"), CODEX_EVENTS, 8).is_err());
}

#[test]
fn process_runner_handles_missing_executable_timeout_cancellation_and_output_limit() {
    let temporary = TempDir::new().expect("temporary");
    let missing = PreparedInvocation {
        program: temporary
            .path()
            .join("missing-adapter")
            .display()
            .to_string(),
        args: Vec::new(),
        stdin: Vec::new(),
        cwd: temporary.path().to_path_buf(),
        limits: limits(),
    };
    assert!(matches!(
        execute_bounded(&missing, &CancellationToken::new()),
        Err(InvocationError::MissingExecutable(_))
    ));

    let timeout = PreparedInvocation {
        program: "/bin/sleep".into(),
        args: vec!["1".into()],
        stdin: Vec::new(),
        cwd: temporary.path().to_path_buf(),
        limits: InvocationLimits {
            timeout_ms: 10,
            ..limits()
        },
    };
    assert!(matches!(
        execute_bounded(&timeout, &CancellationToken::new()),
        Err(InvocationError::TimedOut)
    ));

    let cancellation = CancellationToken::new();
    let cancellation_signal = cancellation.clone();
    thread::spawn(move || {
        thread::sleep(Duration::from_millis(20));
        cancellation_signal.cancel();
    });
    let cancellable = PreparedInvocation {
        limits: InvocationLimits {
            timeout_ms: 2_000,
            ..limits()
        },
        ..timeout
    };
    assert!(matches!(
        execute_bounded(&cancellable, &cancellation),
        Err(InvocationError::Cancelled)
    ));

    let oversized = PreparedInvocation {
        program: "/usr/bin/printf".into(),
        args: vec!["0123456789".into()],
        stdin: Vec::new(),
        cwd: temporary.path().to_path_buf(),
        limits: InvocationLimits {
            max_output_bytes: 4,
            ..limits()
        },
    };
    assert!(matches!(
        execute_bounded(&oversized, &CancellationToken::new()),
        Err(InvocationError::OutputTooLarge { limit: 4 })
    ));
}

#[test]
fn live_gate_is_operator_environment_only_and_never_parsed_from_model_output() {
    assert!(!live_adapter_tests_enabled());
    let model_output = br#"{"AO_NEXT_ENABLE_LIVE_ADAPTER_TESTS":"1"}"#;
    assert!(codex::normalize_output(identity("codex", "v1"), model_output, 1024).is_err());
    assert!(!live_adapter_tests_enabled());
}

#[test]
#[ignore = "requires separate live-provider authority and operator environment gate"]
fn live_codex_contract_smoke_test_is_disabled() {
    assert!(live_adapter_tests_enabled());
}

#[test]
#[ignore = "requires separate live-provider authority and operator environment gate"]
fn live_claude_contract_smoke_test_is_disabled() {
    assert!(live_adapter_tests_enabled());
}

#[test]
fn schema_path_must_be_a_regular_file() {
    let temporary = TempDir::new().expect("temporary");
    assert!(
        codex::prepare_invocation(
            "fixture-model",
            "high",
            temporary.path(),
            Path::new("missing.schema.json"),
            "prompt",
            limits(),
        )
        .is_err()
    );

    let malformed = temporary.path().join("malformed.json");
    std::fs::write(&malformed, br#"{"type":"object","type":"array"}"#).expect("malformed");
    assert!(
        codex::prepare_invocation(
            "fixture-model",
            "high",
            temporary.path(),
            &malformed,
            "prompt",
            limits(),
        )
        .is_err()
    );

    let ambiguous = temporary.path().join("ambiguous.json");
    std::fs::write(&ambiguous, br#"{"oneOf":[],"anyOf":[],"type":"object"}"#).expect("ambiguous");
    assert!(
        codex::prepare_invocation(
            "fixture-model",
            "high",
            temporary.path(),
            &ambiguous,
            "prompt",
            limits(),
        )
        .is_err()
    );

    let oversized = temporary.path().join("oversized.json");
    std::fs::write(&oversized, vec![b' '; limits().max_input_bytes + 1]).expect("oversized");
    assert!(
        codex::prepare_invocation(
            "fixture-model",
            "high",
            temporary.path(),
            &oversized,
            "prompt",
            limits(),
        )
        .is_err()
    );

    #[cfg(unix)]
    {
        std::os::unix::fs::symlink(&ambiguous, temporary.path().join("schema-link.json"))
            .expect("schema symlink");
        assert!(
            codex::prepare_invocation(
                "fixture-model",
                "high",
                temporary.path(),
                &temporary.path().join("schema-link.json"),
                "prompt",
                limits(),
            )
            .is_err()
        );
    }
}

#[test]
fn codex_projection_rejects_digest_path_drift() {
    let temporary = TempDir::new().expect("temporary");
    let schema = temporary.path().join("turn.schema.json");
    std::fs::write(
        &schema,
        serde_json::to_vec(&schema_for!(AdapterTurn)).expect("internal schema"),
    )
    .expect("schema");
    let invocation = codex::prepare_invocation(
        "fixture-model",
        "high",
        temporary.path(),
        &schema,
        "prompt",
        limits(),
    )
    .expect("first projection");
    let projected = invocation
        .args
        .windows(2)
        .find_map(|args| (args[0] == "--output-schema").then(|| Path::new(&args[1])))
        .expect("projected schema path");
    std::fs::write(projected, b"drift").expect("projection drift");
    assert!(
        codex::prepare_invocation(
            "fixture-model",
            "high",
            temporary.path(),
            &schema,
            "prompt",
            limits(),
        )
        .is_err()
    );
}
