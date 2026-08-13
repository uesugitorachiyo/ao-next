use std::collections::BTreeSet;
use std::path::{Path, PathBuf};

use ao_next_core::adapter::process::ProcessRunner;
use ao_next_core::adapter::{
    CancellationToken, InvocationError, InvocationOutput, PreparedInvocation,
};
use ao_next_core::contracts::{
    AuthorityEnvelope, Capability, Digest, ExternalEffectPolicy, ModelProfile, NetworkPolicy,
    RunLimits, RunRequest, SourceIdentity, StructuredCommand, VerifierProfile, WorkspaceIdentity,
};
use ao_next_core::engine::EngineVerifier;
use ao_next_core::evidence::digest_bytes;
use ao_next_core::verifier::{
    CommandEngineVerifier, CommandVerifierEntry, CommandVerifierProfile,
    RequiredArtifactExpectation,
};
use chrono::{TimeZone, Utc};
use tempfile::TempDir;

const ZERO: &str = "sha256:0000000000000000000000000000000000000000000000000000000000000000";

fn digest(value: &str) -> Digest {
    Digest::new(value).expect("fixture digest")
}

fn request(root: &Path, profile: &CommandVerifierProfile) -> RunRequest {
    let commands = profile
        .entries
        .iter()
        .map(|entry| StructuredCommand {
            program: entry.program.clone(),
            args: entry.args.clone(),
            timeout_ms: entry.timeout_ms,
        })
        .collect();
    let required_artifacts = profile
        .entries
        .iter()
        .flat_map(|entry| {
            entry
                .required_artifacts
                .iter()
                .map(|artifact| artifact.path.clone())
        })
        .collect();
    RunRequest {
        schema_version: "ao.next.run-request.v1".into(),
        run_id: "run-command-verifier".into(),
        objective: "Verify a bounded artifact".into(),
        source: SourceIdentity {
            repository: "sealed-fixture".into(),
            head: digest(ZERO),
        },
        workspace: WorkspaceIdentity {
            workspace_id: "workspace-command-verifier".into(),
            root: root.to_path_buf(),
            seed_digest: digest(ZERO),
        },
        model_profile: ModelProfile {
            runtime: "codex".into(),
            model_identifier: "test-model".into(),
            reasoning_effort: "high".into(),
            system_prompt_digest: digest(ZERO),
            tool_contract_digest: digest(ZERO),
            context_limit: 32_000,
            output_limit: 4_000,
            adapter_version: "codex-process-v1".into(),
        },
        authority: AuthorityEnvelope {
            schema_version: "ao.next.authority-envelope.v1".into(),
            issued_by: "operator".into(),
            issued_at: Utc.with_ymd_and_hms(2026, 8, 6, 0, 0, 0).unwrap(),
            expires_at: Utc.with_ymd_and_hms(2026, 8, 7, 0, 0, 0).unwrap(),
            capabilities: BTreeSet::from([Capability::RunLocalProgram]),
            allowed_roots: vec![root.to_path_buf()],
            allowed_programs: BTreeSet::from(["/usr/bin/printf".into()]),
            network: NetworkPolicy::Denied,
            allowed_network_hosts: BTreeSet::new(),
            external_effects: ExternalEffectPolicy::Denied,
        },
        verifier_profile: VerifierProfile {
            profile_id: profile.profile_id.clone(),
            profile_digest: profile.profile_digest.clone(),
            commands,
            required_artifacts,
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

fn sealed_profile(
    required_artifacts: Vec<RequiredArtifactExpectation>,
    max_output_bytes: usize,
) -> CommandVerifierProfile {
    let mut entry = CommandVerifierEntry {
        verifier_id: "product-check".into(),
        verifier_digest: digest(ZERO),
        program: "/usr/bin/printf".into(),
        args: vec!["ok".into()],
        working_directory: PathBuf::from("product"),
        timeout_ms: 500,
        max_output_bytes,
        expected_exit_status: 0,
        required_artifacts,
    };
    entry.verifier_digest = entry.calculated_digest().expect("entry digest");
    let mut profile = CommandVerifierProfile {
        schema_version: "ao.next.command-verifier-profile.v1".into(),
        profile_id: "sealed-product-verifier".into(),
        profile_digest: digest(ZERO),
        entries: vec![entry],
    };
    profile.profile_digest = profile.calculated_digest().expect("profile digest");
    profile
}

struct FakeRunner {
    output: InvocationOutput,
    seen: Vec<PreparedInvocation>,
}

impl ProcessRunner for FakeRunner {
    fn run(
        &mut self,
        invocation: &PreparedInvocation,
        _: &CancellationToken,
    ) -> Result<InvocationOutput, InvocationError> {
        self.seen.push(invocation.clone());
        Ok(self.output.clone())
    }
}

struct TimeoutRunner;

impl ProcessRunner for TimeoutRunner {
    fn run(
        &mut self,
        _: &PreparedInvocation,
        _: &CancellationToken,
    ) -> Result<InvocationOutput, InvocationError> {
        Err(InvocationError::TimedOut)
    }
}

#[test]
fn sealed_command_verifier_runs_exact_contract_and_checks_artifact_digest() {
    let workspace = TempDir::new().expect("workspace");
    let working_directory = workspace.path().join("product");
    std::fs::create_dir(&working_directory).expect("working directory");
    std::fs::write(workspace.path().join("result.txt"), b"verified artifact").expect("artifact");
    let profile = sealed_profile(
        vec![RequiredArtifactExpectation {
            path: PathBuf::from("result.txt"),
            digest: digest_bytes(b"verified artifact"),
        }],
        1024,
    );
    let base_request = request(workspace.path(), &profile);
    let runner = FakeRunner {
        output: InvocationOutput {
            status: 0,
            stdout: b"ok".to_vec(),
            stderr: Vec::new(),
        },
        seen: Vec::new(),
    };
    let mut verifier = CommandEngineVerifier::new(
        &base_request,
        profile,
        runner,
        CancellationToken::new(),
        Utc.with_ymd_and_hms(2026, 8, 6, 12, 0, 0).unwrap(),
    )
    .expect("sealed verifier");

    let outcome = verifier.verify(&base_request);

    assert!(outcome.passed);
    assert_eq!(verifier.reports().len(), 1);
    let report = &verifier.reports()[0];
    assert!(report.passed);
    assert_eq!(report.results.len(), 2);
    assert!(report.results.iter().all(|result| result.passed));
    assert_eq!(verifier.runner().seen.len(), 1);
    assert_eq!(verifier.runner().seen[0].program, "/usr/bin/printf");
    assert_eq!(verifier.runner().seen[0].args, ["ok"]);
    assert_eq!(
        verifier.runner().seen[0].cwd,
        std::fs::canonicalize(working_directory).expect("canonical working directory")
    );
    assert_eq!(verifier.runner().seen[0].limits.timeout_ms, 500);
    assert_eq!(verifier.runner().seen[0].limits.max_output_bytes, 1024);
}

#[test]
fn trusted_verifier_does_not_require_model_program_authority() {
    let workspace = TempDir::new().expect("workspace");
    std::fs::create_dir(workspace.path().join("product")).expect("working directory");
    let profile = sealed_profile(Vec::new(), 1024);
    let mut base_request = request(workspace.path(), &profile);
    base_request.authority.capabilities.clear();
    base_request.authority.allowed_programs.clear();
    let runner = FakeRunner {
        output: InvocationOutput {
            status: 0,
            stdout: b"ok".to_vec(),
            stderr: Vec::new(),
        },
        seen: Vec::new(),
    };

    let mut verifier = CommandEngineVerifier::new(
        &base_request,
        profile,
        runner,
        CancellationToken::new(),
        Utc.with_ymd_and_hms(2026, 8, 6, 12, 0, 0).unwrap(),
    )
    .expect("trusted verifier is independent of model effect authority");

    assert!(verifier.verify(&base_request).passed);
    assert_eq!(verifier.runner().seen.len(), 1);
}

#[test]
fn verifier_mutation_and_reordering_fail_before_execution() {
    let workspace = TempDir::new().expect("workspace");
    std::fs::create_dir(workspace.path().join("product")).expect("working directory");
    let profile = sealed_profile(Vec::new(), 1024);
    let base_request = request(workspace.path(), &profile);

    let mut mutated = profile.clone();
    mutated.entries[0].args.push("mutated".into());
    assert!(
        CommandEngineVerifier::new(
            &base_request,
            mutated,
            FakeRunner {
                output: InvocationOutput {
                    status: 0,
                    stdout: Vec::new(),
                    stderr: Vec::new(),
                },
                seen: Vec::new(),
            },
            CancellationToken::new(),
            Utc.with_ymd_and_hms(2026, 8, 6, 12, 0, 0).unwrap(),
        )
        .is_err()
    );

    let mut two_entries = profile;
    let mut second = two_entries.entries[0].clone();
    second.verifier_id = "second-check".into();
    second.verifier_digest = second.calculated_digest().expect("second entry digest");
    two_entries.entries.push(second);
    two_entries.profile_digest = two_entries.calculated_digest().expect("profile digest");
    let ordered_request = request(workspace.path(), &two_entries);
    two_entries.entries.swap(0, 1);
    assert!(
        CommandEngineVerifier::new(
            &ordered_request,
            two_entries,
            FakeRunner {
                output: InvocationOutput {
                    status: 0,
                    stdout: Vec::new(),
                    stderr: Vec::new(),
                },
                seen: Vec::new(),
            },
            CancellationToken::new(),
            Utc.with_ymd_and_hms(2026, 8, 6, 12, 0, 0).unwrap(),
        )
        .is_err()
    );
}

#[test]
fn verifier_timeout_output_overflow_and_missing_artifact_block_passed() {
    let workspace = TempDir::new().expect("workspace");
    std::fs::create_dir(workspace.path().join("product")).expect("working directory");

    let timeout_profile = sealed_profile(Vec::new(), 1024);
    let timeout_request = request(workspace.path(), &timeout_profile);
    let mut timed_out = CommandEngineVerifier::new(
        &timeout_request,
        timeout_profile,
        TimeoutRunner,
        CancellationToken::new(),
        Utc.with_ymd_and_hms(2026, 8, 6, 12, 0, 0).unwrap(),
    )
    .expect("timeout verifier");
    assert!(!timed_out.verify(&timeout_request).passed);

    let overflow_profile = sealed_profile(Vec::new(), 2);
    let overflow_request = request(workspace.path(), &overflow_profile);
    let mut overflowed = CommandEngineVerifier::new(
        &overflow_request,
        overflow_profile,
        FakeRunner {
            output: InvocationOutput {
                status: 0,
                stdout: b"too large".to_vec(),
                stderr: Vec::new(),
            },
            seen: Vec::new(),
        },
        CancellationToken::new(),
        Utc.with_ymd_and_hms(2026, 8, 6, 12, 0, 0).unwrap(),
    )
    .expect("overflow verifier");
    assert!(!overflowed.verify(&overflow_request).passed);

    let missing_profile = sealed_profile(
        vec![RequiredArtifactExpectation {
            path: PathBuf::from("missing.txt"),
            digest: digest_bytes(b"missing"),
        }],
        1024,
    );
    let missing_request = request(workspace.path(), &missing_profile);
    let mut missing = CommandEngineVerifier::new(
        &missing_request,
        missing_profile,
        FakeRunner {
            output: InvocationOutput {
                status: 0,
                stdout: b"ok".to_vec(),
                stderr: Vec::new(),
            },
            seen: Vec::new(),
        },
        CancellationToken::new(),
        Utc.with_ymd_and_hms(2026, 8, 6, 12, 0, 0).unwrap(),
    )
    .expect("missing-artifact verifier");
    assert!(!missing.verify(&missing_request).passed);
}
