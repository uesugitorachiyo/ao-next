use std::collections::{BTreeSet, VecDeque};
use std::path::Path;
use std::sync::{Arc, Mutex};

use ao_next_core::adapter::process::{
    ProcessAdapterConfig, ProcessRunner, ProcessRuntimeAdapter, ProviderVisibility,
    capture_runtime_output,
};
use ao_next_core::adapter::{
    CancellationToken, InvocationError, InvocationLimits, InvocationOutput, PreparedInvocation,
    TokenUsage,
};
use ao_next_core::contracts::{
    AuthorityEnvelope, Capability, Digest, ExternalEffectPolicy, ModelProfile, NetworkPolicy,
    RunLimits, RunRequest, RunState, SourceIdentity, VerifierProfile, WorkspaceIdentity,
};
use ao_next_core::effects::LocalEffectBroker;
use ao_next_core::engine::{DirectEngine, EngineVerifier, VerificationOutcome};
use ao_next_core::evidence::digest_bytes;
use ao_next_core::strict_json::canonical_digest;
use chrono::{TimeZone, Utc};
use tempfile::TempDir;

const ZERO: &str = "sha256:0000000000000000000000000000000000000000000000000000000000000000";
const ONE: &str = "sha256:1111111111111111111111111111111111111111111111111111111111111111";

fn digest(value: &str) -> Digest {
    Digest::new(value).expect("fixture digest")
}

fn request(root: &Path) -> RunRequest {
    RunRequest {
        schema_version: "ao.next.run-request.v1".into(),
        run_id: "run-process-codex".into(),
        objective: "Return one verifier action".into(),
        source: SourceIdentity {
            repository: "sealed-fixture".into(),
            head: digest(ZERO),
        },
        workspace: WorkspaceIdentity {
            workspace_id: "workspace-process-codex".into(),
            root: root.to_path_buf(),
            seed_digest: digest(ONE),
        },
        model_profile: ModelProfile {
            runtime: "codex".into(),
            model_identifier: "codex-test-model".into(),
            reasoning_effort: "high".into(),
            system_prompt_digest: digest(ZERO),
            tool_contract_digest: digest(ONE),
            context_limit: 32_000,
            output_limit: 4_000,
            adapter_version: "codex-process-v1".into(),
        },
        authority: AuthorityEnvelope {
            schema_version: "ao.next.authority-envelope.v1".into(),
            issued_by: "operator".into(),
            issued_at: Utc.with_ymd_and_hms(2026, 8, 6, 0, 0, 0).unwrap(),
            expires_at: Utc.with_ymd_and_hms(2026, 8, 7, 0, 0, 0).unwrap(),
            capabilities: BTreeSet::new(),
            allowed_roots: vec![root.to_path_buf()],
            allowed_programs: BTreeSet::new(),
            network: NetworkPolicy::Denied,
            allowed_network_hosts: BTreeSet::new(),
            external_effects: ExternalEffectPolicy::Denied,
        },
        verifier_profile: VerifierProfile {
            profile_id: "fixture-verifier".into(),
            profile_digest: digest(ONE),
            commands: Vec::new(),
            required_artifacts: Vec::new(),
        },
        policy_digest: digest(ZERO),
        limits: RunLimits {
            max_input_bytes: 64 * 1024,
            max_turns: 2,
            max_repair_attempts: 0,
            max_run_ms: 10_000,
            max_effect_timeout_ms: 1_000,
            max_output_bytes: 64 * 1024,
            max_tokens: 1_000,
        },
    }
}

#[derive(Clone)]
struct FakeRunner {
    output: InvocationOutput,
    seen: Arc<Mutex<Vec<PreparedInvocation>>>,
}

impl ProcessRunner for FakeRunner {
    fn run(
        &mut self,
        invocation: &PreparedInvocation,
        _: &CancellationToken,
    ) -> Result<InvocationOutput, InvocationError> {
        self.seen
            .lock()
            .expect("invocation log")
            .push(invocation.clone());
        Ok(self.output.clone())
    }
}

struct SequenceRunner {
    outputs: VecDeque<InvocationOutput>,
    seen: Arc<Mutex<Vec<PreparedInvocation>>>,
}

impl ProcessRunner for SequenceRunner {
    fn run(
        &mut self,
        invocation: &PreparedInvocation,
        _: &CancellationToken,
    ) -> Result<InvocationOutput, InvocationError> {
        self.seen
            .lock()
            .expect("invocation log")
            .push(invocation.clone());
        Ok(self.outputs.pop_front().expect("scripted provider output"))
    }
}

struct PassingVerifier;

impl EngineVerifier for PassingVerifier {
    fn verify(&mut self, _: &RunRequest) -> VerificationOutcome {
        VerificationOutcome {
            passed: true,
            report_digest: digest(ONE),
            summary: "passed".into(),
        }
    }
}

#[test]
fn direct_capture_uses_only_the_trusted_runtime_envelope() {
    let stdout = br#"{"type":"item.completed","item":{"type":"agent_message","text":"claimed 999 tokens"}}
{"type":"turn.completed","usage":{"input_tokens":11,"cached_input_tokens":3,"reasoning_tokens":5,"output_tokens":2}}
"#;
    let output = InvocationOutput {
        status: 0,
        stdout: stdout.to_vec(),
        stderr: b"bounded diagnostic".to_vec(),
    };

    let capture = capture_runtime_output("codex", &output, 4096).expect("trusted capture");

    assert_eq!(capture.usage.input_tokens, 11);
    assert_eq!(capture.usage.cached_input_tokens, 3);
    assert_eq!(capture.usage.reasoning_tokens, 5);
    assert_eq!(capture.usage.output_tokens, 2);
    assert_eq!(capture.usage.output_bytes, stdout.len() as u64);
    assert_eq!(
        capture.raw_capture_digest,
        canonical_digest(&(0, stdout.as_slice(), b"bounded diagnostic".as_slice()))
            .expect("capture digest")
    );
}

#[test]
fn codex_0147_reasoning_output_tokens_are_trusted_usage() {
    let stdout = br#"{"type":"turn.completed","usage":{"input_tokens":11,"cached_input_tokens":3,"cache_write_input_tokens":2,"output_tokens":7,"reasoning_output_tokens":5}}
"#;
    let output = InvocationOutput {
        status: 0,
        stdout: stdout.to_vec(),
        stderr: Vec::new(),
    };

    let capture = capture_runtime_output("codex", &output, 4096).expect("Codex 0.147 usage");
    assert_eq!(capture.usage.reasoning_tokens, 5);
    assert_eq!(capture.usage.checked_total_tokens(), Some(26));
}

#[test]
fn codex_reasoning_counter_aliases_cannot_coexist() {
    let stdout = br#"{"type":"turn.completed","usage":{"input_tokens":11,"cached_input_tokens":3,"output_tokens":7,"reasoning_tokens":5,"reasoning_output_tokens":5}}
"#;
    let output = InvocationOutput {
        status: 0,
        stdout: stdout.to_vec(),
        stderr: Vec::new(),
    };

    assert!(capture_runtime_output("codex", &output, 4096).is_err());
}

#[test]
fn four_counter_trusted_usage_total_uses_checked_arithmetic() {
    let observed = TokenUsage {
        input_tokens: 225_206,
        cached_input_tokens: 193_792,
        reasoning_tokens: 0,
        output_tokens: 6_100,
        output_bytes: 0,
    };
    assert_eq!(observed.checked_total_tokens(), Some(425_098));

    let addition_overflow = TokenUsage {
        input_tokens: u64::MAX,
        cached_input_tokens: 1,
        reasoning_tokens: 0,
        output_tokens: 0,
        output_bytes: 0,
    };
    assert_eq!(addition_overflow.checked_total_tokens(), None);
}

#[test]
fn malformed_or_incomplete_trusted_usage_remains_rejected() {
    let invalid = [
        r#"{"type":"turn.completed","usage":{"cached_input_tokens":0,"reasoning_tokens":0,"output_tokens":1}}"#,
        r#"{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"reasoning_tokens":0}}"#,
        r#"{"type":"turn.completed","usage":{"input_tokens":-1,"cached_input_tokens":0,"reasoning_tokens":0,"output_tokens":1}}"#,
        r#"{"type":"turn.completed","usage":{"input_tokens":1.5,"cached_input_tokens":0,"reasoning_tokens":0,"output_tokens":1}}"#,
        r#"{"type":"turn.completed","usage":{"input_tokens":18446744073709551616,"cached_input_tokens":0,"reasoning_tokens":0,"output_tokens":1}}"#,
        r#"{"type":"turn.completed","usage":{"input_tokens":1,"input_tokens":2,"cached_input_tokens":0,"reasoning_tokens":0,"output_tokens":1}}"#,
        r#"{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"reasoning_tokens":0,"output_tokens":1}"#,
        r#"{"type":"item.completed","item":{}}"#,
        "{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"cached_input_tokens\":0,\"reasoning_tokens\":0,\"output_tokens\":1}}\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"cached_input_tokens\":0,\"reasoning_tokens\":0,\"output_tokens\":1}}",
    ];
    for stdout in invalid {
        let output = InvocationOutput {
            status: 0,
            stdout: format!("{stdout}\n").into_bytes(),
            stderr: Vec::new(),
        };
        assert!(
            capture_runtime_output("codex", &output, 4096).is_err(),
            "invalid trusted usage was accepted: {stdout}"
        );
    }
}

#[test]
fn codex_process_adapter_runs_through_engine_with_trusted_envelope_usage() {
    let workspace = TempDir::new().expect("workspace");
    let schema = workspace.path().join("turn.schema.json");
    std::fs::write(&schema, b"{\"type\":\"object\"}").expect("schema");
    let request = request(workspace.path());
    let provider = br#"{"type":"item.completed","item":{"type":"agent_message","text":"{\"actions\":[{\"kind\":\"verify\"}],\"usage\":{\"input_tokens\":900,\"cached_input_tokens\":900,\"reasoning_tokens\":900,\"output_tokens\":900,\"output_bytes\":900},\"model_claimed_success\":false,\"control_mutations\":[]}"}}
{"type":"turn.completed","usage":{"input_tokens":11,"cached_input_tokens":3,"reasoning_tokens":5,"output_tokens":2}}
"#;
    let seen = Arc::new(Mutex::new(Vec::new()));
    let runner = FakeRunner {
        output: InvocationOutput {
            status: 0,
            stdout: provider.to_vec(),
            stderr: Vec::new(),
        },
        seen: seen.clone(),
    };
    let config = ProcessAdapterConfig::from_request(
        &request,
        "worker-live-01",
        &schema,
        InvocationLimits {
            max_input_bytes: 64 * 1024,
            max_output_bytes: 64 * 1024,
            timeout_ms: 1_000,
        },
        CancellationToken::new(),
    )
    .expect("adapter config");
    let mut adapter = ProcessRuntimeAdapter::new(config, runner);
    let broker = LocalEffectBroker::new(1_000, 64 * 1024, 64 * 1024);
    let mut verifier = PassingVerifier;

    let outcome = DirectEngine::new(&broker).run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Passed);
    assert_eq!(outcome.worker_identity.worker_id, "worker-live-01");
    assert_eq!(outcome.metrics.usage.input_tokens, 11);
    assert_eq!(outcome.metrics.usage.cached_input_tokens, 3);
    assert_eq!(outcome.metrics.usage.reasoning_tokens, 5);
    assert_eq!(outcome.metrics.usage.output_tokens, 2);
    assert_eq!(outcome.metrics.usage.output_bytes, provider.len() as u64);
    assert_eq!(adapter.captures().len(), 1);
    assert_eq!(
        adapter.captures()[0].raw_capture_digest,
        canonical_digest(&(0, provider.as_slice(), &[] as &[u8])).expect("capture digest")
    );
    let invocations = seen.lock().expect("invocation log");
    assert_eq!(invocations.len(), 1);
    assert_eq!(invocations[0].program, "codex");
    let prompt: serde_json::Value =
        serde_json::from_slice(&invocations[0].stdin).expect("structured prompt");
    assert_eq!(prompt["objective"], request.objective);
    assert_eq!(prompt["run_id"], request.run_id);
    assert_eq!(prompt["turn_index"], 0);
    assert_eq!(prompt["repair_attempt"], 0);
    assert_eq!(prompt["adapter_identity"]["worker_id"], "worker-live-01");
    assert_eq!(
        prompt["allowed_actions"],
        serde_json::json!(["effect", "verify", "blocked", "interrupt"])
    );
    assert_eq!(prompt["effect_observations"], serde_json::json!([]));
    assert_eq!(
        prompt["authority_digest"],
        canonical_digest(&request.authority)
            .expect("authority digest")
            .as_str()
    );
}

#[test]
fn provider_prompt_exposes_bound_native_authority_and_visible_inventory() {
    let workspace = TempDir::new().expect("workspace");
    std::fs::write(workspace.path().join("source.txt"), b"visible source\n")
        .expect("visible source");
    let schema = workspace.path().join("turn.schema.json");
    std::fs::write(&schema, b"{\"type\":\"object\"}").expect("schema");
    let mut request = request(workspace.path());
    request
        .authority
        .capabilities
        .extend([Capability::ReadWorkspace, Capability::WriteWorkspace]);
    let provider = br#"{"type":"item.completed","item":{"type":"agent_message","text":"{\"actions\":[{\"kind\":\"verify\"}],\"usage\":{\"input_tokens\":1,\"cached_input_tokens\":0,\"reasoning_tokens\":0,\"output_tokens\":1,\"output_bytes\":1},\"model_claimed_success\":false,\"control_mutations\":[]}"}}
{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"reasoning_tokens":0,"output_tokens":1}}
"#;
    let seen = Arc::new(Mutex::new(Vec::new()));
    let runner = FakeRunner {
        output: InvocationOutput {
            status: 0,
            stdout: provider.to_vec(),
            stderr: Vec::new(),
        },
        seen: seen.clone(),
    };
    let config = ProcessAdapterConfig::from_request(
        &request,
        "worker-visible-01",
        &schema,
        InvocationLimits {
            max_input_bytes: 64 * 1024,
            max_output_bytes: 64 * 1024,
            timeout_ms: 1_000,
        },
        CancellationToken::new(),
    )
    .expect("adapter config");
    let mut adapter = ProcessRuntimeAdapter::new(config, runner);
    let broker = LocalEffectBroker::new(1_000, 64 * 1024, 64 * 1024);
    let mut verifier = PassingVerifier;

    let outcome = DirectEngine::new(&broker).run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Passed);
    let invocations = seen.lock().expect("invocation log");
    let prompt: serde_json::Value =
        serde_json::from_slice(&invocations[0].stdin).expect("structured prompt");
    assert_eq!(prompt["schema_version"], "ao.next.provider-turn-prompt.v2");
    assert_eq!(
        prompt["authority"]["native_capabilities"],
        serde_json::json!(["read_utf8_file", "write_utf8_file"])
    );
    assert_eq!(
        prompt["authority"]["allowed_programs"],
        serde_json::json!([])
    );
    assert_eq!(prompt["authority"]["network"], "denied");
    assert_eq!(prompt["authority"]["external_effects"], "denied");
    assert_eq!(prompt["authority"]["limits"]["max_turns"], 2);
    assert_eq!(prompt["authority"]["limits"]["max_input_bytes"], 65_536);
    assert_eq!(prompt["authority"]["limits"]["max_output_bytes"], 65_536);
    assert_eq!(
        prompt["visible_workspace"],
        serde_json::json!([
            {
                "path": "source.txt",
                "content": "visible source\n",
                "digest": digest_bytes(b"visible source\n")
            },
            {
                "path": "turn.schema.json",
                "content": "{\"type\":\"object\"}",
                "digest": digest_bytes(b"{\"type\":\"object\"}")
            }
        ])
    );
    assert_eq!(
        prompt["hidden_tests"],
        "unavailable_and_must_not_be_requested"
    );
    assert_eq!(
        prompt["action_sequence"],
        "when max_turns is 1, include every required effect followed by verify in the same actions array"
    );
}

#[test]
fn live_visibility_binds_visible_fixtures_in_relative_path_order_without_private_paths() {
    let workspace = TempDir::new().expect("workspace");
    let visible = TempDir::new().expect("visible fixtures");
    let controls = TempDir::new().expect("controls");
    std::fs::write(workspace.path().join("source.txt"), b"source\n").expect("source");
    std::fs::write(visible.path().join("zeta.txt"), b"zeta\n").expect("later visible");
    std::fs::create_dir(visible.path().join("nested")).expect("nested fixtures");
    std::fs::write(visible.path().join("nested/alpha.txt"), b"alpha\n").expect("nested visible");
    std::fs::write(visible.path().join("example.txt"), b"example\n").expect("visible");
    let schema = controls.path().join("turn.schema.json");
    std::fs::write(&schema, b"{\"type\":\"object\"}").expect("schema");
    let source_digest = canonical_digest(&[serde_json::json!({
        "path": "source.txt",
        "sha256": digest_bytes(b"source\n"),
        "size_bytes": 7
    })])
    .expect("source digest");
    let visible_digest = canonical_digest(&serde_json::json!([
        {"path": "example.txt", "sha256": digest_bytes(b"example\n"), "size_bytes": 8},
        {"path": "nested/alpha.txt", "sha256": digest_bytes(b"alpha\n"), "size_bytes": 6},
        {"path": "zeta.txt", "sha256": digest_bytes(b"zeta\n"), "size_bytes": 5}
    ]))
    .expect("visible digest");
    let visibility = ProviderVisibility::from_live_roots(
        workspace.path(),
        &source_digest,
        visible.path(),
        &visible_digest,
        64 * 1024,
    )
    .expect("bound visibility");
    let mut request = request(workspace.path());
    request.workspace.seed_digest = source_digest;
    request
        .authority
        .capabilities
        .extend([Capability::ReadWorkspace, Capability::WriteWorkspace]);
    let provider = br#"{"type":"item.completed","item":{"type":"agent_message","text":"{\"actions\":[{\"kind\":\"verify\"}],\"usage\":{\"input_tokens\":1,\"cached_input_tokens\":0,\"reasoning_tokens\":0,\"output_tokens\":1,\"output_bytes\":1},\"model_claimed_success\":false,\"control_mutations\":[]}"}}
{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"reasoning_tokens":0,"output_tokens":1}}
"#;
    let seen = Arc::new(Mutex::new(Vec::new()));
    let runner = FakeRunner {
        output: InvocationOutput {
            status: 0,
            stdout: provider.to_vec(),
            stderr: Vec::new(),
        },
        seen: seen.clone(),
    };
    let config = ProcessAdapterConfig::from_request_with_visibility(
        &request,
        "worker-visible-live-01",
        &schema,
        visibility,
        InvocationLimits {
            max_input_bytes: 64 * 1024,
            max_output_bytes: 64 * 1024,
            timeout_ms: 1_000,
        },
        CancellationToken::new(),
    )
    .expect("adapter config");
    let mut adapter = ProcessRuntimeAdapter::new(config, runner);
    let broker = LocalEffectBroker::new(1_000, 64 * 1024, 64 * 1024);
    let mut verifier = PassingVerifier;

    assert_eq!(
        DirectEngine::new(&broker)
            .run(&request, &mut adapter, &mut verifier)
            .terminal_state,
        RunState::Passed
    );
    let invocations = seen.lock().expect("invocations");
    let prompt_text = String::from_utf8(invocations[0].stdin.clone()).expect("prompt UTF-8");
    let prompt: serde_json::Value = serde_json::from_str(&prompt_text).expect("prompt JSON");
    assert_eq!(
        prompt["visible_fixtures"],
        serde_json::json!([
            {"path": "example.txt", "content": "example\n", "digest": digest_bytes(b"example\n")},
            {"path": "nested/alpha.txt", "content": "alpha\n", "digest": digest_bytes(b"alpha\n")},
            {"path": "zeta.txt", "content": "zeta\n", "digest": digest_bytes(b"zeta\n")}
        ])
    );
    let paths = prompt["visible_fixtures"]
        .as_array()
        .expect("visible fixture array")
        .iter()
        .map(|file| file["path"].as_str().expect("relative path"))
        .collect::<Vec<_>>();
    let mut sorted = paths.clone();
    sorted.sort_unstable();
    assert_eq!(paths, sorted);
    assert!(!prompt_text.contains(workspace.path().to_str().expect("workspace path")));
    assert!(!prompt_text.contains(visible.path().to_str().expect("visible path")));
    assert!(!prompt_text.contains(controls.path().to_str().expect("controls path")));
}

#[test]
fn live_visibility_omits_only_an_ordinary_root_git_directory() {
    let visible = TempDir::new().expect("visible fixtures");
    let empty_digest = canonical_digest(&Vec::<serde_json::Value>::new()).expect("empty digest");
    let source_digest = canonical_digest(&[serde_json::json!({
        "path": "source.txt",
        "sha256": digest_bytes(b"source\n"),
        "size_bytes": 7
    })])
    .expect("source digest");

    let prepared = TempDir::new().expect("prepared workspace");
    std::fs::write(prepared.path().join("source.txt"), b"source\n").expect("source");
    std::fs::create_dir(prepared.path().join(".git")).expect("root Git directory");
    std::fs::write(prepared.path().join(".git/HEAD"), b"ref: refs/heads/main\n")
        .expect("Git control file");
    ProviderVisibility::from_live_roots(
        prepared.path(),
        &source_digest,
        visible.path(),
        &empty_digest,
        64 * 1024,
    )
    .expect("ordinary root .git is omitted");

    let visible_control = TempDir::new().expect("visible control root");
    std::fs::create_dir(visible_control.path().join(".git")).expect("visible root Git directory");
    assert!(
        ProviderVisibility::from_live_roots(
            prepared.path(),
            &source_digest,
            visible_control.path(),
            &empty_digest,
            64 * 1024,
        )
        .is_err(),
        "only the workspace root .git may be omitted"
    );

    for control_path in ["nested/.git", ".GIT"] {
        let workspace = TempDir::new().expect("control workspace");
        std::fs::write(workspace.path().join("source.txt"), b"source\n").expect("source");
        std::fs::create_dir_all(workspace.path().join(control_path)).expect("control directory");
        assert!(
            ProviderVisibility::from_live_roots(
                workspace.path(),
                &source_digest,
                visible.path(),
                &empty_digest,
                64 * 1024,
            )
            .is_err(),
            "{control_path} must remain rejected"
        );
    }

    #[cfg(unix)]
    {
        let workspace = TempDir::new().expect("symlink workspace");
        let target = TempDir::new().expect("Git target");
        std::fs::write(workspace.path().join("source.txt"), b"source\n").expect("source");
        std::os::unix::fs::symlink(target.path(), workspace.path().join(".git"))
            .expect("root Git symlink");
        assert!(
            ProviderVisibility::from_live_roots(
                workspace.path(),
                &source_digest,
                visible.path(),
                &empty_digest,
                64 * 1024,
            )
            .is_err(),
            "root .git symlink must remain rejected"
        );
    }
}

#[cfg(windows)]
fn create_junction(link: &Path, target: &Path) {
    let output = std::process::Command::new("cmd")
        .args(["/C", "mklink", "/J"])
        .arg(link)
        .arg(target)
        .output()
        .expect("create junction");
    assert!(
        output.status.success(),
        "stdout={} stderr={}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

#[cfg(windows)]
#[test]
fn live_visibility_rejects_workspace_and_fixture_junctions_at_roots_and_entries() {
    let empty_digest = canonical_digest(&Vec::<serde_json::Value>::new()).expect("empty digest");
    let source_digest = canonical_digest(&[serde_json::json!({
        "path": "source.txt",
        "sha256": digest_bytes(b"source\n"),
        "size_bytes": 7
    })])
    .expect("source digest");
    let ordinary_workspace = TempDir::new().expect("workspace");
    std::fs::write(ordinary_workspace.path().join("source.txt"), b"source\n").expect("source");
    let ordinary_fixtures = TempDir::new().expect("fixtures");

    for boundary in [
        "workspace-root",
        "workspace-entry",
        "fixture-root",
        "fixture-entry",
    ] {
        let parent = TempDir::new().expect("junction parent");
        let target = TempDir::new().expect("junction target");
        if boundary == "workspace-root" {
            std::fs::write(target.path().join("source.txt"), b"source\n").expect("junction source");
        }
        let link = parent.path().join("junction");
        create_junction(&link, target.path());
        let workspace_root = if boundary == "workspace-root" {
            &link
        } else {
            if boundary == "workspace-entry" {
                create_junction(&ordinary_workspace.path().join("nested"), target.path());
            }
            ordinary_workspace.path()
        };
        let fixture_root = if boundary == "fixture-root" {
            &link
        } else {
            if boundary == "fixture-entry" {
                create_junction(&ordinary_fixtures.path().join("nested"), target.path());
            }
            ordinary_fixtures.path()
        };

        assert!(
            ProviderVisibility::from_live_roots(
                workspace_root,
                &source_digest,
                fixture_root,
                &empty_digest,
                64 * 1024,
            )
            .is_err(),
            "accepted {boundary} junction"
        );

        if boundary == "workspace-entry" {
            std::fs::remove_dir(ordinary_workspace.path().join("nested"))
                .expect("remove workspace junction");
        }
        if boundary == "fixture-entry" {
            std::fs::remove_dir(ordinary_fixtures.path().join("nested"))
                .expect("remove fixture junction");
        }
    }
}

#[test]
fn claude_process_adapter_runs_through_engine_with_trusted_envelope_usage() {
    let workspace = TempDir::new().expect("workspace");
    let schema = workspace.path().join("turn.schema.json");
    std::fs::write(&schema, b"{\"type\":\"object\"}").expect("schema");
    let mut request = request(workspace.path());
    request.run_id = "run-process-claude".into();
    request.model_profile.runtime = "claude".into();
    request.model_profile.model_identifier = "claude-test-model".into();
    request.model_profile.adapter_version = "claude-process-v1".into();
    let provider = br#"{
  "type": "result",
  "subtype": "success",
  "is_error": false,
  "usage": {
    "input_tokens": 13,
    "cache_read_input_tokens": 4,
    "cache_creation_input_tokens": 2,
    "reasoning_tokens": 6,
    "output_tokens": 3
  },
  "structured_output": {
    "actions": [{"kind": "verify"}],
    "usage": {
      "input_tokens": 900,
      "cached_input_tokens": 900,
      "reasoning_tokens": 900,
      "output_tokens": 900,
      "output_bytes": 900
    },
    "model_claimed_success": false,
    "control_mutations": []
  }
}"#;
    let seen = Arc::new(Mutex::new(Vec::new()));
    let runner = FakeRunner {
        output: InvocationOutput {
            status: 0,
            stdout: provider.to_vec(),
            stderr: Vec::new(),
        },
        seen: seen.clone(),
    };
    let config = ProcessAdapterConfig::from_request(
        &request,
        "worker-live-claude-01",
        &schema,
        InvocationLimits {
            max_input_bytes: 64 * 1024,
            max_output_bytes: 64 * 1024,
            timeout_ms: 1_000,
        },
        CancellationToken::new(),
    )
    .expect("adapter config");
    let mut adapter = ProcessRuntimeAdapter::new(config, runner);
    let broker = LocalEffectBroker::new(1_000, 64 * 1024, 64 * 1024);
    let mut verifier = PassingVerifier;

    let outcome = DirectEngine::new(&broker).run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Passed);
    assert_eq!(outcome.metrics.usage.input_tokens, 13);
    assert_eq!(outcome.metrics.usage.cached_input_tokens, 6);
    assert_eq!(outcome.metrics.usage.reasoning_tokens, 6);
    assert_eq!(outcome.metrics.usage.output_tokens, 3);
    assert_eq!(outcome.metrics.usage.output_bytes, provider.len() as u64);
    assert_eq!(
        adapter.captures()[0].raw_capture_digest,
        canonical_digest(&(0, provider.as_slice(), &[] as &[u8])).expect("capture digest")
    );
    let invocations = seen.lock().expect("invocation log");
    assert_eq!(invocations.len(), 1);
    assert_eq!(invocations[0].program, "claude");
    assert!(
        invocations[0]
            .args
            .windows(2)
            .any(|pair| pair == ["--tools", ""])
    );
}

#[test]
fn raw_capture_digest_binds_status_stdout_and_stderr() {
    let workspace = TempDir::new().expect("workspace");
    let schema = workspace.path().join("turn.schema.json");
    std::fs::write(&schema, b"{\"type\":\"object\"}").expect("schema");
    let request = request(workspace.path());
    let stdout = br#"{"type":"item.completed","item":{"type":"agent_message","text":"{\"actions\":[{\"kind\":\"verify\"}],\"usage\":{\"input_tokens\":1,\"cached_input_tokens\":0,\"reasoning_tokens\":0,\"output_tokens\":1,\"output_bytes\":1},\"model_claimed_success\":false,\"control_mutations\":[]}"}}
{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"reasoning_tokens":0,"output_tokens":1}}
"#;
    let stderr = b"bounded operator diagnostic";
    let runner = FakeRunner {
        output: InvocationOutput {
            status: 0,
            stdout: stdout.to_vec(),
            stderr: stderr.to_vec(),
        },
        seen: Arc::new(Mutex::new(Vec::new())),
    };
    let config = ProcessAdapterConfig::from_request(
        &request,
        "worker-live-01",
        &schema,
        InvocationLimits {
            max_input_bytes: 64 * 1024,
            max_output_bytes: 64 * 1024,
            timeout_ms: 1_000,
        },
        CancellationToken::new(),
    )
    .expect("adapter config");
    let mut adapter = ProcessRuntimeAdapter::new(config, runner);
    let broker = LocalEffectBroker::new(1_000, 64 * 1024, 64 * 1024);
    let mut verifier = PassingVerifier;

    let outcome = DirectEngine::new(&broker).run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Passed);
    assert_eq!(
        adapter.captures()[0].raw_capture_digest,
        canonical_digest(&(0, stdout.as_slice(), stderr.as_slice())).expect("capture digest")
    );
}

#[test]
fn effect_observation_returns_to_one_worker_on_the_next_provider_turn() {
    let workspace = TempDir::new().expect("workspace");
    let schema = workspace.path().join("turn.schema.json");
    std::fs::write(&schema, b"{\"type\":\"object\"}").expect("schema");
    let source = workspace.path().join("source.txt");
    std::fs::write(&source, b"effect-output").expect("source");
    let mut request = request(workspace.path());
    request
        .authority
        .capabilities
        .insert(Capability::ReadWorkspace);
    let first = format!(
        "{{\"type\":\"item.completed\",\"item\":{{\"type\":\"agent_message\",\"text\":\"{{\\\"actions\\\":[{{\\\"kind\\\":\\\"effect\\\",\\\"value\\\":{{\\\"effect_id\\\":\\\"effect-1\\\",\\\"run_id\\\":\\\"{}\\\",\\\"kind\\\":\\\"read_file\\\",\\\"program\\\":null,\\\"content\\\":null,\\\"args\\\":[],\\\"paths\\\":[\\\"{}\\\"],\\\"timeout_ms\\\":0,\\\"input_digest\\\":\\\"{}\\\"}}}}],\\\"usage\\\":{{\\\"input_tokens\\\":99,\\\"cached_input_tokens\\\":99,\\\"reasoning_tokens\\\":99,\\\"output_tokens\\\":99,\\\"output_bytes\\\":99}},\\\"model_claimed_success\\\":false,\\\"control_mutations\\\":[]}}\"}}}}\n{{\"type\":\"turn.completed\",\"usage\":{{\"input_tokens\":1,\"cached_input_tokens\":0,\"reasoning_tokens\":0,\"output_tokens\":1}}}}\n",
        request.run_id,
        "source.txt",
        digest_bytes(b"effect-output")
    );
    let second = br#"{"type":"item.completed","item":{"type":"agent_message","text":"{\"actions\":[{\"kind\":\"verify\"}],\"usage\":{\"input_tokens\":99,\"cached_input_tokens\":99,\"reasoning_tokens\":99,\"output_tokens\":99,\"output_bytes\":99},\"model_claimed_success\":false,\"control_mutations\":[]}"}}
{"type":"turn.completed","usage":{"input_tokens":2,"cached_input_tokens":0,"reasoning_tokens":0,"output_tokens":1}}
"#;
    let seen = Arc::new(Mutex::new(Vec::new()));
    let runner = SequenceRunner {
        outputs: VecDeque::from([
            InvocationOutput {
                status: 0,
                stdout: first.into_bytes(),
                stderr: Vec::new(),
            },
            InvocationOutput {
                status: 0,
                stdout: second.to_vec(),
                stderr: Vec::new(),
            },
        ]),
        seen: seen.clone(),
    };
    let config = ProcessAdapterConfig::from_request(
        &request,
        "worker-stable-01",
        &schema,
        InvocationLimits {
            max_input_bytes: 64 * 1024,
            max_output_bytes: 64 * 1024,
            timeout_ms: 1_000,
        },
        CancellationToken::new(),
    )
    .expect("adapter config");
    let mut adapter = ProcessRuntimeAdapter::new(config, runner);
    let broker = LocalEffectBroker::new(1_000, 64 * 1024, 64 * 1024);
    let mut verifier = PassingVerifier;

    let outcome = DirectEngine::new(&broker).run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Passed);
    assert_eq!(outcome.metrics.turns, 2);
    assert_eq!(adapter.captures().len(), 2);
    assert!(outcome.events.iter().all(|event| {
        event
            .worker_id
            .as_deref()
            .is_none_or(|worker| worker == "worker-stable-01")
    }));
    let invocations = seen.lock().expect("invocation log");
    assert_eq!(invocations.len(), 2);
    let second_prompt: serde_json::Value =
        serde_json::from_slice(&invocations[1].stdin).expect("second prompt");
    assert_eq!(second_prompt["turn_index"], 1);
    assert_eq!(
        second_prompt["effect_observations"][0]["effect_id"],
        "effect-1"
    );
    assert_eq!(
        second_prompt["effect_observations"][0]["stdout"],
        serde_json::json!([
            101, 102, 102, 101, 99, 116, 45, 111, 117, 116, 112, 117, 116
        ])
    );
    assert_ne!(
        second_prompt["effect_observations"][0]["output_digest"],
        serde_json::Value::Null
    );
}

#[test]
fn pre_cancelled_adapter_never_calls_the_process_runner() {
    let workspace = TempDir::new().expect("workspace");
    let schema = workspace.path().join("turn.schema.json");
    std::fs::write(&schema, b"{\"type\":\"object\"}").expect("schema");
    let request = request(workspace.path());
    let seen = Arc::new(Mutex::new(Vec::new()));
    let runner = FakeRunner {
        output: InvocationOutput {
            status: 0,
            stdout: Vec::new(),
            stderr: Vec::new(),
        },
        seen: seen.clone(),
    };
    let cancellation = CancellationToken::new();
    cancellation.cancel();
    let config = ProcessAdapterConfig::from_request(
        &request,
        "worker-cancelled-01",
        &schema,
        InvocationLimits {
            max_input_bytes: 64 * 1024,
            max_output_bytes: 64 * 1024,
            timeout_ms: 1_000,
        },
        cancellation,
    )
    .expect("adapter config");
    let mut adapter = ProcessRuntimeAdapter::new(config, runner);
    let broker = LocalEffectBroker::new(1_000, 64 * 1024, 64 * 1024);
    let mut verifier = PassingVerifier;

    let outcome = DirectEngine::new(&broker).run(&request, &mut adapter, &mut verifier);

    assert_eq!(outcome.terminal_state, RunState::Failed);
    assert_eq!(outcome.failure_code.as_deref(), Some("adapter_failure"));
    assert!(
        seen.lock().expect("invocation log").is_empty(),
        "cancellation must fail before process execution"
    );
}
