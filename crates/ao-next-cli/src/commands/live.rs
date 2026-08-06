use std::collections::{BTreeMap, BTreeSet};
use std::io::Write as _;
use std::path::{Component, Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::Instant;

use ao_next_core::adapter::codex;
use ao_next_core::adapter::process::{
    BoundedProcessRunner, ProcessAdapterConfig, ProcessRunner, ProcessRuntimeAdapter,
    RuntimeCapture, capture_runtime_output,
};
use ao_next_core::adapter::{
    CancellationToken, InvocationError, InvocationLimits, InvocationOutput, PreparedInvocation,
};
use ao_next_core::contracts::{Digest, RunRequest, RunState};
use ao_next_core::effects::LocalEffectBroker;
use ao_next_core::engine::{DirectEngine, EngineEventKind, EngineVerifier, RunOutcome};
use ao_next_core::evidence::digest_bytes;
use ao_next_core::strict_json::{canonical_digest, decode_strict_json};
use ao_next_core::verifier::{CommandEngineVerifier, CommandVerifierProfile};
use ao_next_eval::corpus::{CorpusManifest, EvaluationTask, VariantProfile};
use ao_next_eval::metrics::{ExecutionVariant, MeasurementOrigin, RunMeasurement, TokenRow};
use chrono::Utc;
use serde::{Deserialize, Serialize};
use sha2::{Digest as ShaDigest, Sha256};

use super::{
    CommandFailure, CommandOutput, LiveRunArgs, LiveVariantArg, PreflightLiveInputArgs,
    decode_file, read_bounded_regular,
};

const WORKER_ID: &str = "ao-next-live-worker-01";
const GIT_PROGRAM: &str = "/usr/bin/git";
const ENV_PROGRAM: &str = "/usr/bin/env";
const GIT_BRANCH: &str = "ao-next-sealed-seed";
const GIT_TIMESTAMP: &str = "2000-01-01T00:00:00Z";
const GIT_OUTPUT_LIMIT: usize = 256 * 1024;
const GIT_TIMEOUT_MS: u64 = 10_000;

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "UPPERCASE")]
pub enum LiveVariant {
    N0,
    N4,
    N7,
}

impl LiveVariant {
    const fn execution_variant(self) -> ExecutionVariant {
        match self {
            Self::N0 => ExecutionVariant::N0,
            Self::N4 => ExecutionVariant::N4,
            Self::N7 => ExecutionVariant::N7,
        }
    }
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct LiveRunInput {
    schema_version: String,
    corpus: CorpusManifest,
    task_id: String,
    trial_id: String,
    trial_index: u32,
    schedule_position: u32,
    workspace_instance_id: String,
    source_snapshot: PathBuf,
    objective: PathBuf,
    visible_fixtures: PathBuf,
    hidden_tests: PathBuf,
    output_schema: PathBuf,
    raw_capture_root: PathBuf,
    request: RunRequest,
    command_verifier: CommandVerifierProfile,
    #[serde(default)]
    current_ao: Option<CurrentAoBinding>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
struct CurrentAoBinding {
    schema_version: String,
    ao2_program: PathBuf,
    ao2_program_digest: Digest,
    provider_program: PathBuf,
    provider_program_digest: Digest,
    adapter_version: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
struct SnapshotEntry {
    path: PathBuf,
    sha256: Digest,
    size_bytes: u64,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct SourceSnapshot {
    schema_version: String,
    task_id: String,
    tree_digest: Digest,
    files: Vec<SnapshotEntry>,
}

#[derive(Debug, Serialize)]
#[serde(deny_unknown_fields)]
struct LiveRunRecord {
    schema_version: &'static str,
    variant: LiveVariant,
    terminal_state: RunState,
    measurement: RunMeasurement,
    capture_digests: Vec<Digest>,
    raw_capture_index_digest: Digest,
    verifier_report_digest: Option<Digest>,
    git_workspace: GitWorkspaceIdentity,
    ao2_control_diagnostics: Vec<serde_json::Value>,
    record_digest: Digest,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
struct GitWorkspaceIdentity {
    repository_root: PathBuf,
    common_dir: PathBuf,
    head_commit: String,
    branch: &'static str,
}

struct ValidatedInput<'a> {
    task: &'a EvaluationTask,
    profile: &'a VariantProfile,
    initial_files: Vec<SnapshotEntry>,
    hidden_file_digests: BTreeSet<Digest>,
}

type LiveExecution = (
    RunState,
    Option<RunOutcome>,
    Vec<RuntimeCapture>,
    Vec<InvocationOutput>,
    Option<Digest>,
    Vec<serde_json::Value>,
);

struct SingleProviderProcess<R> {
    runner: R,
    started: bool,
}

struct CaptureFirstRunner<R> {
    runner: R,
    raw_capture_root: PathBuf,
    capture_context: CaptureContext,
    retained_index: Arc<Mutex<Option<Digest>>>,
}

impl<R: ProcessRunner> ProcessRunner for CaptureFirstRunner<R> {
    fn run(
        &mut self,
        invocation: &PreparedInvocation,
        cancellation: &CancellationToken,
    ) -> Result<InvocationOutput, InvocationError> {
        if self
            .retained_index
            .lock()
            .map_err(|error| InvocationError::Io(error.to_string()))?
            .is_some()
        {
            return Err(InvocationError::Io(
                "duplicate provider capture identity".into(),
            ));
        }
        let output = self.runner.run(invocation, cancellation)?;
        let digest = persist_raw_captures(
            &self.raw_capture_root,
            &self.capture_context,
            &[],
            std::slice::from_ref(&output),
        )
        .map_err(|error| InvocationError::Io(format!("capture failure: {}", error.message)))?;
        *self
            .retained_index
            .lock()
            .map_err(|error| InvocationError::Io(error.to_string()))? = Some(digest);
        Ok(output)
    }
}

impl<R> SingleProviderProcess<R> {
    const fn new(runner: R) -> Self {
        Self {
            runner,
            started: false,
        }
    }
}

impl<R: ProcessRunner> ProcessRunner for SingleProviderProcess<R> {
    fn run(
        &mut self,
        invocation: &PreparedInvocation,
        cancellation: &CancellationToken,
    ) -> Result<InvocationOutput, InvocationError> {
        if self.started {
            return Err(InvocationError::Io(
                "single provider process budget exhausted".into(),
            ));
        }
        self.started = true;
        self.runner.run(invocation, cancellation)
    }
}

pub fn execute(args: &LiveRunArgs, variant: LiveVariant) -> Result<CommandOutput, CommandFailure> {
    if std::env::var("AO_NEXT_LIVE_PROVIDER_CALLS").as_deref() != Ok("operator-authorized") {
        return Err(CommandFailure::authorization(
            "live provider calls require separate operator authorization",
        ));
    }
    let input: LiveRunInput = decode_file(&args.input)?;
    execute_with_runners(
        &input,
        variant,
        MeasurementOrigin::LiveProvider,
        BoundedProcessRunner,
        BoundedProcessRunner,
    )
}

pub fn preflight(args: &PreflightLiveInputArgs) -> Result<CommandOutput, CommandFailure> {
    if std::env::var("AO_NEXT_LIVE_PROVIDER_CALLS").is_ok() {
        return Err(CommandFailure::authorization(
            "provider authorization must be absent during offline input preflight",
        ));
    }
    let input: LiveRunInput = decode_file(&args.input)?;
    let variant = match args.variant {
        LiveVariantArg::N0 => LiveVariant::N0,
        LiveVariantArg::N4 => LiveVariant::N4,
        LiveVariantArg::N7 => LiveVariant::N7,
    };
    let validated = validate_input(&input, variant, Utc::now())?;
    let git_workspace = prepare_git_workspace(
        &input.request.workspace.root,
        &input.request.authority.allowed_roots,
        &validated.task.workspace_seed_digest,
    )?;
    Ok(CommandOutput::new(
        serde_json::json!({
            "schema_version": "ao.next.live-input-preflight.v1",
            "corpus_digest": input.corpus.corpus_digest,
            "task_id": input.task_id,
            "trial_id": input.trial_id,
            "trial_index": input.trial_index,
            "schedule_position": input.schedule_position,
            "workspace_instance_id": input.workspace_instance_id,
            "variant": variant,
            "model_identifier": validated.profile.model_identifier,
            "reasoning_effort": input.request.model_profile.reasoning_effort,
            "runtime": validated.profile.runtime,
            "adapter_version": validated.profile.adapter_version,
            "git_workspace": git_workspace,
            "provider_calls": 0
        }),
        "validated one provider-free live input",
        0,
    ))
}

#[allow(
    clippy::too_many_lines,
    reason = "the run record is assembled once so every measured field remains visibly source-bound"
)]
fn execute_with_runners<P: ProcessRunner, V: ProcessRunner>(
    input: &LiveRunInput,
    variant: LiveVariant,
    measurement_origin: MeasurementOrigin,
    provider_runner: P,
    verifier_runner: V,
) -> Result<CommandOutput, CommandFailure> {
    let started_at = Utc::now();
    let started = Instant::now();
    let validated = validate_input(input, variant, started_at)?;
    let git_workspace = prepare_git_workspace(
        &input.request.workspace.root,
        &input.request.authority.allowed_roots,
        &validated.task.workspace_seed_digest,
    )?;
    let cancellation = CancellationToken::new();
    let invocation_limits = invocation_limits(&input.request)?;
    let mut verifier = CommandEngineVerifier::new(
        &input.request,
        input.command_verifier.clone(),
        verifier_runner,
        cancellation.clone(),
        started_at,
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let retained_index = Arc::new(Mutex::new(None));
    let capture_context = capture_context(input, variant);

    let execution = match variant {
        LiveVariant::N0 => execute_n0(
            input,
            provider_runner,
            &capture_context,
            &mut verifier,
            &cancellation,
            invocation_limits,
        ),
        LiveVariant::N7 => execute_n7(
            input,
            CaptureFirstRunner {
                runner: provider_runner,
                raw_capture_root: input.raw_capture_root.clone(),
                capture_context: capture_context.clone(),
                retained_index: retained_index.clone(),
            },
            &mut verifier,
            cancellation.clone(),
            invocation_limits,
        ),
        LiveVariant::N4 => execute_n4(
            input,
            CaptureFirstRunner {
                runner: provider_runner,
                raw_capture_root: input.raw_capture_root.clone(),
                capture_context: capture_context.clone(),
                retained_index: retained_index.clone(),
            },
            &mut verifier,
            &cancellation,
            invocation_limits,
        ),
    };
    let (
        terminal_state,
        outcome,
        captures,
        raw_outputs,
        retained_capture_index,
        ao2_control_diagnostics,
    ) = match execution {
        Ok(execution) => execution,
        Err(error) => {
            if input.raw_capture_root.join("capture-index.json").is_file() {
                record_capture_terminal(&input.raw_capture_root, &capture_context, &error, None)?;
            }
            return Err(error);
        }
    };
    let raw_capture_index_digest = retained_capture_index
        .or_else(|| retained_index.lock().ok().and_then(|value| value.clone()))
        .ok_or_else(|| CommandFailure::evidence("raw provider capture coverage is incomplete"))?;
    if let Err(error) = verify_raw_capture_index(
        &input.raw_capture_root,
        &capture_context,
        &raw_capture_index_digest,
        input.request.limits.max_output_bytes,
    ) {
        record_capture_terminal(
            &input.raw_capture_root,
            &capture_context,
            &error,
            Some("evidence"),
        )?;
        return Err(error);
    }
    if captures.len() != raw_outputs.len() {
        let error =
            CommandFailure::runtime("provider output was retained but could not be normalized");
        record_capture_terminal(
            &input.raw_capture_root,
            &capture_context,
            &error,
            Some("normalization"),
        )?;
        return Err(error);
    }
    let wall_clock_ms = u64::try_from(started.elapsed().as_millis())
        .unwrap_or(u64::MAX)
        .max(1);
    let final_files = snapshot_product_tree(
        &input.request.workspace.root,
        input.request.limits.max_input_bytes,
        Some(&git_workspace),
    )?;
    let hidden_test_exposure = final_files
        .iter()
        .any(|entry| validated.hidden_file_digests.contains(&entry.sha256));
    let changed_files = count_changed_files(&validated.initial_files, &final_files);
    let report = verifier.reports().last();
    let hidden_tests_total =
        u32::try_from(input.command_verifier.entries.len()).unwrap_or(u32::MAX);
    let hidden_tests_passed = report.map_or(0, |report| {
        u32::try_from(
            report
                .results
                .iter()
                .filter(|result| result.verifier_id.starts_with("command:") && result.passed)
                .count(),
        )
        .unwrap_or(u32::MAX)
    });
    let usage = outcome.as_ref().map_or_else(
        || sum_capture_usage(&captures),
        |value| value.metrics.usage.clone(),
    );
    if usage.total_tokens() > input.request.limits.max_tokens {
        return Err(CommandFailure::runtime(
            "trusted provider usage exceeded the sealed token limit",
        ));
    }
    let model_wait_ms = captures
        .iter()
        .fold(0_u64, |total, capture| {
            total.saturating_add(capture.model_wait_ms)
        })
        .min(wall_clock_ms);
    let capture_digests = captures
        .iter()
        .map(|capture| capture.raw_capture_digest.clone())
        .collect::<Vec<_>>();
    let raw_capture_digest = canonical_digest(&capture_digests)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let task_success = terminal_state == RunState::Passed && !hidden_test_exposure;
    if !task_success {
        let stage = if raw_outputs.iter().any(|output| output.status != 0) {
            "provider"
        } else {
            "verifier"
        };
        record_capture_terminal(
            &input.raw_capture_root,
            &capture_context,
            &CommandFailure::runtime(format!("{stage} stage did not pass")),
            Some(stage),
        )?;
    }
    let evidence_complete = task_success && !captures.is_empty() && report.is_some();
    let unauthorized_effects = outcome.as_ref().map_or(0, |value| {
        u32::try_from(
            value
                .events
                .iter()
                .filter(|event| matches!(event.kind, EngineEventKind::EffectDenied(_)))
                .count(),
        )
        .unwrap_or(u32::MAX)
    });
    let repair_attempts = outcome
        .as_ref()
        .map_or(0, |value| value.metrics.repair_attempts);
    let completed_effects = outcome.as_ref().map_or_else(Vec::new, |value| {
        value
            .events
            .iter()
            .filter_map(|event| match &event.kind {
                EngineEventKind::EffectCompleted(effect_id) => Some(effect_id.as_str()),
                _ => None,
            })
            .collect::<Vec<_>>()
    });
    let unique_completed_effects = completed_effects.iter().copied().collect::<BTreeSet<_>>();
    let recovery_attempted = repair_attempts > 0;
    let recovery_no_duplicate_effect =
        recovery_attempted && unique_completed_effects.len() == completed_effects.len();
    let measurement = RunMeasurement {
        schema_version: "ao.next.run-measurement.v2".into(),
        corpus_digest: input.corpus.corpus_digest.clone(),
        run_id: input.request.run_id.clone(),
        trial_id: input.trial_id.clone(),
        trial_index: input.trial_index,
        schedule_position: input.schedule_position,
        raw_capture_digest,
        raw_capture_digests: capture_digests.clone(),
        workspace_instance_id: input.workspace_instance_id.clone(),
        task_id: input.task_id.clone(),
        variant: variant.execution_variant(),
        source_digest: validated.task.source_digest.clone(),
        objective_digest: validated.task.objective_digest.clone(),
        workspace_seed_digest: validated.task.workspace_seed_digest.clone(),
        visible_fixtures_digest: validated.task.visible_fixtures_digest.clone(),
        hidden_tests_digest: validated.task.hidden_tests_digest.clone(),
        verifier_profile_digest: validated.task.verifier_profile_digest.clone(),
        runtime: validated.profile.runtime.clone(),
        runtime_digest: validated.profile.runtime_digest.clone(),
        model_identifier: validated.profile.model_identifier.clone(),
        model_digest: validated.profile.model_digest.clone(),
        prompt_digest: validated.profile.prompt_digest.clone(),
        policy_digest: validated.profile.policy_digest.clone(),
        adapter_version: validated.profile.adapter_version.clone(),
        adapter_digest: validated.profile.adapter_digest.clone(),
        measurement_origin,
        provider_usage_trusted: !captures.is_empty(),
        tokens: TokenRow {
            input_tokens: Some(usage.input_tokens),
            cached_input_tokens: Some(usage.cached_input_tokens),
            reasoning_tokens: Some(usage.reasoning_tokens),
            output_tokens: Some(usage.output_tokens),
            reported_total_tokens: usage.total_tokens(),
        },
        wall_clock_ms,
        model_wait_ms,
        worker_turns: u32::try_from(captures.len()).unwrap_or(u32::MAX),
        repair_attempts,
        operator_interventions: 0,
        changed_files,
        accepted_changed_files: if task_success { changed_files } else { 0 },
        task_success,
        hidden_tests_passed,
        hidden_tests_total,
        regressions: hidden_tests_total.saturating_sub(hidden_tests_passed),
        unauthorized_effects,
        evidence_complete,
        evidence_digest_valid: evidence_complete,
        recovery_attempted,
        recovery_no_duplicate_effect,
        cross_runtime_agreement: variant == LiveVariant::N7,
        worker_count: 1,
        dynamic_fanout: false,
        hidden_test_exposure,
    };
    let verifier_report_digest = report.and_then(|report| canonical_digest(report).ok());
    let record_digest = canonical_digest(&(
        variant,
        &terminal_state,
        &measurement,
        &capture_digests,
        &raw_capture_index_digest,
        &verifier_report_digest,
        &git_workspace,
        &ao2_control_diagnostics,
    ))
    .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let record = LiveRunRecord {
        schema_version: "ao.next.live-run-record.v1",
        variant,
        terminal_state: terminal_state.clone(),
        measurement,
        capture_digests,
        raw_capture_index_digest,
        verifier_report_digest,
        git_workspace,
        ao2_control_diagnostics,
        record_digest,
    };
    let status = match terminal_state {
        _ if hidden_test_exposure => 7,
        RunState::Passed => 0,
        RunState::Interrupted => 6,
        RunState::Failed if report.is_some() => 5,
        _ => 4,
    };
    let value = serde_json::to_value(record)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    Ok(CommandOutput::new(
        value,
        format!(
            "{variant:?} run {} ended {terminal_state:?}",
            input.request.run_id
        ),
        status,
    ))
}

#[allow(
    clippy::too_many_lines,
    reason = "the N0 runner keeps one provider call and both digest-bound AO2 patch controls in execution order"
)]
fn execute_n0<P: ProcessRunner, V: ProcessRunner>(
    input: &LiveRunInput,
    mut process_runner: P,
    capture_context: &CaptureContext,
    verifier: &mut CommandEngineVerifier<V>,
    cancellation: &CancellationToken,
    limits: InvocationLimits,
) -> Result<LiveExecution, CommandFailure> {
    let binding = input
        .current_ao
        .as_ref()
        .ok_or_else(|| CommandFailure::invalid_input("N0 current-AO binding is missing"))?;
    let prompt = current_ao_prompt(input)?;
    let provider_args = [
        "exec".to_string(),
        "--json".to_string(),
        "--ephemeral".to_string(),
        "--ignore-user-config".to_string(),
        "--ignore-rules".to_string(),
        "--color".to_string(),
        "never".to_string(),
        "--sandbox".to_string(),
        "workspace-write".to_string(),
        "-c".to_string(),
        "approval_policy=\"never\"".to_string(),
        "--model".to_string(),
        input.request.model_profile.model_identifier.clone(),
        "-c".to_string(),
        format!(
            "model_reasoning_effort=\"{}\"",
            input.request.model_profile.reasoning_effort
        ),
        "--skip-git-repo-check".to_string(),
        prompt,
    ];
    let invocation = PreparedInvocation {
        program: binding.ao2_program.display().to_string(),
        args: vec![
            "adapter".into(),
            "run".into(),
            "--provider".into(),
            "codex".into(),
            "--target".into(),
            input.request.workspace.root.display().to_string(),
            "--command".into(),
            binding.provider_program.display().to_string(),
            "--args".into(),
            provider_args.join("\t"),
            "--role-id".into(),
            "ao-next-n0-worker-01".into(),
            "--keep-sandbox".into(),
            "--timeout-seconds".into(),
            input.request.limits.max_run_ms.div_ceil(1_000).to_string(),
        ],
        stdin: Vec::new(),
        cwd: input.request.workspace.root.clone(),
        limits,
    };
    let started = Instant::now();
    let ao2_output = process_runner
        .run(&invocation, cancellation)
        .map_err(|error| CommandFailure::runtime(error.to_string()))?;
    let model_wait_ms = u64::try_from(started.elapsed().as_millis()).unwrap_or(u64::MAX);
    let Ok(run) = decode_current_ao_output(&ao2_output, &input.request.workspace.root, limits)
    else {
        return Ok((
            RunState::Failed,
            None,
            Vec::new(),
            vec![ao2_output],
            None,
            Vec::new(),
        ));
    };
    let provider_output = InvocationOutput {
        status: run.exit_code,
        stdout: run.stdout,
        stderr: run.stderr,
    };
    let retained_capture_index = persist_raw_captures(
        &input.raw_capture_root,
        capture_context,
        &[],
        std::slice::from_ref(&provider_output),
    )?;
    let Ok(capture) = capture_runtime_output("codex", &provider_output, limits.max_output_bytes)
    else {
        return Ok((
            RunState::Failed,
            None,
            Vec::new(),
            vec![provider_output],
            Some(retained_capture_index),
            Vec::new(),
        ));
    };

    let preview = run_current_ao_control(
        &mut process_runner,
        binding,
        &input.request.workspace.root,
        "preview",
        &[
            "adapter",
            "patch",
            "preview",
            "--target",
            &input.request.workspace.root.display().to_string(),
            "--sandbox",
            &run.sandbox_path.display().to_string(),
        ],
        cancellation,
        limits,
    )?;
    let digest = preview
        .value
        .get("action_digest")
        .and_then(serde_json::Value::as_str)
        .filter(|value| !value.trim().is_empty())
        .ok_or_else(|| CommandFailure::runtime("current-AO patch digest is missing"))?
        .to_string();
    let mut ao2_control_diagnostics = vec![preview.diagnostic];
    let applied = run_current_ao_control(
        &mut process_runner,
        binding,
        &input.request.workspace.root,
        "apply",
        &[
            "adapter",
            "patch",
            "apply",
            "--target",
            &input.request.workspace.root.display().to_string(),
            "--sandbox",
            &run.sandbox_path.display().to_string(),
            "--digest",
            &digest,
            "--approver",
            "ao-next:bounded-live-evaluation",
        ],
        cancellation,
        limits,
    )?;
    if applied
        .value
        .get("action_digest")
        .and_then(serde_json::Value::as_str)
        != Some(digest.as_str())
    {
        return Err(CommandFailure::runtime(
            "current-AO patch application digest drifted",
        ));
    }
    ao2_control_diagnostics.push(applied.diagnostic);
    let verification = verifier.verify(&input.request);
    let terminal_state = if verification.passed {
        RunState::Passed
    } else {
        RunState::Failed
    };
    Ok((
        terminal_state,
        None,
        vec![RuntimeCapture {
            turn_index: 0,
            raw_capture_digest: capture.raw_capture_digest,
            usage: capture.usage,
            model_wait_ms,
        }],
        vec![provider_output],
        Some(retained_capture_index),
        ao2_control_diagnostics,
    ))
}

struct CurrentAoOutput {
    exit_code: i32,
    stdout: Vec<u8>,
    stderr: Vec<u8>,
    sandbox_path: PathBuf,
}

fn decode_current_ao_output(
    output: &InvocationOutput,
    workspace: &Path,
    limits: InvocationLimits,
) -> Result<CurrentAoOutput, CommandFailure> {
    if output.status != 0 {
        return Err(CommandFailure::runtime(format!(
            "current-AO adapter exited {}",
            output.status
        )));
    }
    let value: serde_json::Value = decode_strict_json(&output.stdout, limits.max_output_bytes)
        .map_err(|error| CommandFailure::runtime(error.to_string()))?;
    let target = value
        .get("target_repo")
        .and_then(serde_json::Value::as_str)
        .ok_or_else(|| CommandFailure::runtime("current-AO target identity is missing"))?;
    if std::fs::canonicalize(target).ok() != std::fs::canonicalize(workspace).ok() {
        return Err(CommandFailure::runtime(
            "current-AO target identity drifted",
        ));
    }
    let sandbox_path = PathBuf::from(
        value
            .get("sandbox_path")
            .and_then(serde_json::Value::as_str)
            .ok_or_else(|| CommandFailure::runtime("current-AO sandbox identity is missing"))?,
    );
    let sandbox_metadata = std::fs::symlink_metadata(&sandbox_path)
        .map_err(|error| CommandFailure::runtime(error.to_string()))?;
    if sandbox_metadata.file_type().is_symlink() || !sandbox_metadata.is_dir() {
        return Err(CommandFailure::runtime(
            "current-AO sandbox is not a regular directory",
        ));
    }
    let adapter = value
        .get("adapter")
        .and_then(serde_json::Value::as_object)
        .ok_or_else(|| CommandFailure::runtime("current-AO adapter result is missing"))?;
    if adapter.get("provider").and_then(serde_json::Value::as_str) != Some("codex")
        || adapter.get("role_id").and_then(serde_json::Value::as_str)
            != Some("ao-next-n0-worker-01")
        || !adapter
            .get("blocker")
            .is_some_and(serde_json::Value::is_null)
    {
        return Err(CommandFailure::runtime(
            "current-AO provider identity or status drifted",
        ));
    }
    let exit_code = adapter
        .get("exit_code")
        .and_then(serde_json::Value::as_i64)
        .and_then(|value| i32::try_from(value).ok())
        .ok_or_else(|| CommandFailure::runtime("current-AO provider exit is missing"))?;
    let stdout = adapter
        .get("stdout")
        .and_then(serde_json::Value::as_str)
        .ok_or_else(|| CommandFailure::runtime("current-AO provider stdout is missing"))?
        .as_bytes()
        .to_vec();
    let stderr = adapter
        .get("stderr")
        .and_then(serde_json::Value::as_str)
        .ok_or_else(|| CommandFailure::runtime("current-AO provider stderr is missing"))?
        .as_bytes()
        .to_vec();
    Ok(CurrentAoOutput {
        exit_code,
        stdout,
        stderr,
        sandbox_path,
    })
}

struct Ao2ControlResult {
    value: serde_json::Value,
    diagnostic: serde_json::Value,
}

fn run_current_ao_control<P: ProcessRunner>(
    runner: &mut P,
    binding: &CurrentAoBinding,
    workspace: &Path,
    stage: &'static str,
    args: &[&str],
    cancellation: &CancellationToken,
    limits: InvocationLimits,
) -> Result<Ao2ControlResult, CommandFailure> {
    let started = Instant::now();
    let output = match runner.run(
        &PreparedInvocation {
            program: binding.ao2_program.display().to_string(),
            args: args.iter().map(|value| (*value).to_string()).collect(),
            stdin: Vec::new(),
            cwd: workspace.to_path_buf(),
            limits,
        },
        cancellation,
    ) {
        Ok(output) => output,
        Err(error) => {
            let diagnostic = ao2_control_diagnostic(
                binding,
                workspace,
                stage,
                args,
                u64::try_from(started.elapsed().as_millis()).unwrap_or(u64::MAX),
                None,
                Some(&error.to_string()),
            );
            return Err(CommandFailure::runtime_with_diagnostic(
                format!("AO2 {stage} invocation failed"),
                diagnostic,
            ));
        }
    };
    let diagnostic = ao2_control_diagnostic(
        binding,
        workspace,
        stage,
        args,
        u64::try_from(started.elapsed().as_millis()).unwrap_or(u64::MAX),
        Some(&output),
        None,
    );
    if output.status != 0 {
        return Err(CommandFailure::runtime_with_diagnostic(
            format!("AO2 {stage} exited {}", output.status),
            diagnostic,
        ));
    }
    let value = decode_strict_json(&output.stdout, limits.max_output_bytes).map_err(|error| {
        CommandFailure::runtime_with_diagnostic(
            format!("AO2 {stage} returned malformed control output: {error}"),
            diagnostic.clone(),
        )
    })?;
    Ok(Ao2ControlResult { value, diagnostic })
}

fn ao2_control_diagnostic(
    binding: &CurrentAoBinding,
    workspace: &Path,
    stage: &str,
    args: &[&str],
    elapsed_ms: u64,
    output: Option<&InvocationOutput>,
    invocation_error: Option<&str>,
) -> serde_json::Value {
    let sandbox = args
        .windows(2)
        .find(|pair| pair[0] == "--sandbox")
        .map(|pair| PathBuf::from(pair[1]));
    let workspace_text = workspace.display().to_string();
    let sandbox_text = sandbox.as_ref().map(|path| path.display().to_string());
    let sanitize = |bytes: &[u8]| {
        let mut text = String::from_utf8_lossy(&bytes[..bytes.len().min(2048)]).to_string();
        text = text.replace(&workspace_text, "<workspace>");
        if let Some(sandbox) = &sandbox_text {
            text = text.replace(sandbox, "<sandbox>");
        }
        text
    };
    let sanitized_command = args
        .iter()
        .enumerate()
        .map(|(index, value)| {
            if *value == workspace_text {
                "<workspace>".to_string()
            } else if index > 0 && args[index - 1] == "--sandbox" {
                "<sandbox>".to_string()
            } else {
                (*value).to_string()
            }
        })
        .collect::<Vec<_>>();
    let stdout = output.map_or(&[][..], |value| value.stdout.as_slice());
    let stderr = output.map_or_else(
        || invocation_error.unwrap_or_default().as_bytes(),
        |value| value.stderr.as_slice(),
    );
    serde_json::json!({
        "schema_version": "ao.next.ao2-control-diagnostic.v1",
        "stage": stage,
        "program_path": binding.ao2_program,
        "program_digest": binding.ao2_program_digest,
        "command": sanitized_command,
        "target_identity": digest_bytes(workspace_text.as_bytes()),
        "sandbox_identity": sandbox_text.as_deref().map(|value| digest_bytes(value.as_bytes())),
        "exit_status": output.map(|value| value.status),
        "elapsed_ms": elapsed_ms,
        "stdout": {
            "digest": digest_bytes(stdout),
            "byte_count": stdout.len(),
            "bounded_text": sanitize(stdout),
        },
        "stderr": {
            "digest": digest_bytes(stderr),
            "byte_count": stderr.len(),
            "bounded_text": sanitize(stderr),
        }
    })
}

fn execute_n7<P: ProcessRunner, V: ProcessRunner>(
    input: &LiveRunInput,
    provider_runner: P,
    verifier: &mut CommandEngineVerifier<V>,
    cancellation: CancellationToken,
    limits: InvocationLimits,
) -> Result<LiveExecution, CommandFailure> {
    let config = ProcessAdapterConfig::from_request(
        &input.request,
        WORKER_ID,
        &input.output_schema,
        limits,
        cancellation,
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let mut adapter =
        ProcessRuntimeAdapter::new(config, SingleProviderProcess::new(provider_runner));
    let broker = LocalEffectBroker::new(
        input.request.limits.max_effect_timeout_ms,
        usize::try_from(input.request.limits.max_output_bytes).unwrap_or(usize::MAX),
    );
    let outcome = DirectEngine::new(&broker).run(&input.request, &mut adapter, verifier);
    let terminal_state = outcome.terminal_state.clone();
    Ok((
        terminal_state,
        Some(outcome),
        adapter.captures().to_vec(),
        adapter.raw_outputs().to_vec(),
        None,
        Vec::new(),
    ))
}

fn execute_n4<P: ProcessRunner, V: ProcessRunner>(
    input: &LiveRunInput,
    mut provider_runner: P,
    verifier: &mut CommandEngineVerifier<V>,
    cancellation: &CancellationToken,
    limits: InvocationLimits,
) -> Result<LiveExecution, CommandFailure> {
    if input.request.model_profile.runtime != "codex" {
        return Err(CommandFailure::invalid_input(
            "N4 native baseline requires the Codex runtime",
        ));
    }
    let prompt = direct_prompt(input)?;
    let invocation = codex::prepare_direct_invocation(
        &input.request.model_profile.model_identifier,
        &input.request.model_profile.reasoning_effort,
        &input.request.workspace.root,
        &prompt,
        limits,
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let started = Instant::now();
    let output = provider_runner
        .run(&invocation, cancellation)
        .map_err(|error| CommandFailure::runtime(error.to_string()))?;
    let model_wait_ms = u64::try_from(started.elapsed().as_millis()).unwrap_or(u64::MAX);
    let Ok(capture) = capture_runtime_output("codex", &output, limits.max_output_bytes) else {
        return Ok((
            RunState::Failed,
            None,
            Vec::new(),
            vec![output],
            None,
            Vec::new(),
        ));
    };
    let verification = verifier.verify(&input.request);
    let terminal_state = if verification.passed {
        RunState::Passed
    } else {
        RunState::Failed
    };
    Ok((
        terminal_state,
        None,
        vec![RuntimeCapture {
            turn_index: 0,
            raw_capture_digest: capture.raw_capture_digest,
            usage: capture.usage,
            model_wait_ms,
        }],
        vec![output],
        None,
        Vec::new(),
    ))
}

#[allow(
    clippy::too_many_lines,
    reason = "the fail-closed intake audit stays linear so all identity comparisons remain visible"
)]
fn validate_input(
    input: &LiveRunInput,
    variant: LiveVariant,
    now: chrono::DateTime<Utc>,
) -> Result<ValidatedInput<'_>, CommandFailure> {
    if input.schema_version != "ao.next.live-run-input.v1"
        || input.task_id.trim().is_empty()
        || input.trial_id.trim().is_empty()
        || input.workspace_instance_id != input.request.workspace.workspace_id
    {
        return Err(CommandFailure::invalid_input(
            "live run input identity is invalid",
        ));
    }
    input
        .corpus
        .validate_live()
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let task = input
        .corpus
        .tasks
        .iter()
        .find(|task| task.task_id == input.task_id)
        .ok_or_else(|| CommandFailure::invalid_input("task is not in the sealed corpus"))?;
    let profile = task
        .variant_profiles
        .iter()
        .find(|profile| profile.variant == variant.execution_variant())
        .ok_or_else(|| CommandFailure::invalid_input("variant profile is missing"))?;
    let scheduled = input.corpus.schedule.iter().any(|entry| {
        entry.trial_index == input.trial_index
            && entry.schedule_position == input.schedule_position
            && entry.variant == variant.execution_variant()
    });
    if input.trial_index >= input.corpus.required_trial_count || !scheduled {
        return Err(CommandFailure::invalid_input(
            "trial identity does not match the sealed schedule",
        ));
    }
    ao_next_core::contracts::validate_intake(
        &input.request,
        &ao_next_core::contracts::IntakeExpectation {
            run_id: input.request.run_id.clone(),
            source: input.request.source.clone(),
            workspace: input.request.workspace.clone(),
            now,
        },
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if input.request.source.head != task.source_digest
        || input.request.workspace.seed_digest != task.workspace_seed_digest
        || input.request.verifier_profile.profile_digest != task.verifier_profile_digest
        || input.command_verifier.profile_digest != task.verifier_profile_digest
        || input.request.model_profile.model_identifier != profile.model_identifier
        || input.request.model_profile.system_prompt_digest != profile.prompt_digest
        || input.request.model_profile.tool_contract_digest != profile.adapter_digest
        || input.request.model_profile.adapter_version != profile.adapter_version
        || input.request.policy_digest != profile.policy_digest
        || canonical_digest(&(
            profile.model_identifier.as_str(),
            input.request.model_profile.reasoning_effort.as_str(),
        ))
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
            != profile.model_digest
        || !runtime_matches_profile(
            variant,
            &input.request.model_profile.runtime,
            &profile.runtime,
        )
    {
        return Err(CommandFailure::invalid_input(
            "source, workspace, model, prompt, policy, verifier, adapter, or runtime identity drifted",
        ));
    }
    match (variant, input.current_ao.as_ref()) {
        (LiveVariant::N0, Some(binding)) => validate_current_ao_binding(binding, profile)?,
        (LiveVariant::N0, None) => {
            return Err(CommandFailure::invalid_input(
                "N0 current-AO binding is missing",
            ));
        }
        (_, Some(_)) => {
            return Err(CommandFailure::invalid_input(
                "current-AO binding is forbidden for N4 and N7",
            ));
        }
        (_, None) => {}
    }
    let objective = read_bounded_regular(&input.objective)?;
    if objective != input.request.objective.as_bytes()
        || digest_bytes(&objective) != task.objective_digest
    {
        return Err(CommandFailure::invalid_input("objective identity drifted"));
    }
    let initial_files = snapshot_tree(
        &input.request.workspace.root,
        input.request.limits.max_input_bytes,
    )?;
    if canonical_digest(&initial_files)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
        != task.workspace_seed_digest
    {
        return Err(CommandFailure::invalid_input("workspace seed drifted"));
    }
    let source_bytes = read_bounded_regular(&input.source_snapshot)?;
    let source: SourceSnapshot = decode_strict_json(
        &source_bytes,
        usize::try_from(input.request.limits.max_input_bytes).unwrap_or(usize::MAX),
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if source.schema_version != "ao.next.source-snapshot.v1"
        || source.task_id != input.task_id
        || source.tree_digest != task.workspace_seed_digest
        || source.files != initial_files
        || canonical_digest(&source)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
            != task.source_digest
    {
        return Err(CommandFailure::invalid_input("source snapshot drifted"));
    }
    let visible = snapshot_tree(
        &input.visible_fixtures,
        input.request.limits.max_input_bytes,
    )?;
    if canonical_digest(&visible)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
        != task.visible_fixtures_digest
    {
        return Err(CommandFailure::invalid_input("visible fixture drifted"));
    }
    let hidden = snapshot_tree(&input.hidden_tests, input.request.limits.max_input_bytes)?;
    if canonical_digest(&hidden)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
        != task.hidden_tests_digest
    {
        return Err(CommandFailure::invalid_input("hidden-test fixture drifted"));
    }
    ensure_outside_roots(&input.hidden_tests, &input.request.authority.allowed_roots)?;
    ensure_private_capture_root(
        &input.raw_capture_root,
        &input.request.authority.allowed_roots,
    )?;
    ensure_bounded_regular_under_roots(
        &input.output_schema,
        &input.request.authority.allowed_roots,
        input.request.limits.max_input_bytes,
    )?;
    Ok(ValidatedInput {
        task,
        profile,
        initial_files,
        hidden_file_digests: hidden.into_iter().map(|entry| entry.sha256).collect(),
    })
}

fn ensure_private_capture_root(
    path: &Path,
    worker_roots: &[PathBuf],
) -> Result<(), CommandFailure> {
    ensure_outside_roots(path, worker_roots)?;
    let metadata = std::fs::symlink_metadata(path)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if metadata.file_type().is_symlink() || !metadata.is_dir() {
        return Err(CommandFailure::invalid_input(
            "raw capture root is not a regular non-symlink directory",
        ));
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt as _;
        if metadata.permissions().mode() & 0o077 != 0 {
            return Err(CommandFailure::invalid_input(
                "raw capture root permissions are not owner-only",
            ));
        }
    }
    let mut entries = std::fs::read_dir(path)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if entries
        .next()
        .transpose()
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
        .is_some()
    {
        return Err(CommandFailure::invalid_input(
            "raw capture root is not empty",
        ));
    }
    Ok(())
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
struct CaptureContext {
    run_id: String,
    trial_id: String,
    workspace_instance_id: String,
    runtime_identity: CaptureRuntimeIdentity,
    maximum_output_bytes: u64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
struct CaptureRuntimeIdentity {
    runtime: String,
    model_identifier: String,
    adapter_version: String,
    worker_id: String,
}

fn capture_context(input: &LiveRunInput, variant: LiveVariant) -> CaptureContext {
    CaptureContext {
        run_id: input.request.run_id.clone(),
        trial_id: input.trial_id.clone(),
        workspace_instance_id: input.workspace_instance_id.clone(),
        runtime_identity: CaptureRuntimeIdentity {
            runtime: if variant == LiveVariant::N0 {
                "codex".into()
            } else {
                input.request.model_profile.runtime.clone()
            },
            model_identifier: input.request.model_profile.model_identifier.clone(),
            adapter_version: input.request.model_profile.adapter_version.clone(),
            worker_id: match variant {
                LiveVariant::N0 => "ao-next-n0-worker-01",
                LiveVariant::N4 => "ao-next-n4-direct-worker-01",
                LiveVariant::N7 => WORKER_ID,
            }
            .into(),
        },
        maximum_output_bytes: input.request.limits.max_output_bytes,
    }
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct RawCaptureIndex {
    schema_version: String,
    run_id: String,
    trial_id: String,
    workspace_instance_id: String,
    runtime_identity: CaptureRuntimeIdentity,
    entries: Vec<RawCaptureIndexEntry>,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct RawCaptureIndexEntry {
    capture_order: u32,
    turn_index: u32,
    status: i32,
    raw_capture_digest: Digest,
    stdout_path: String,
    stdout_digest: Digest,
    stdout_size_bytes: u64,
    stderr_path: String,
    stderr_digest: Digest,
    stderr_size_bytes: u64,
}

fn persist_raw_captures(
    root: &Path,
    context: &CaptureContext,
    captures: &[RuntimeCapture],
    outputs: &[InvocationOutput],
) -> Result<Digest, CommandFailure> {
    if outputs.is_empty() {
        return Err(CommandFailure::evidence(
            "raw provider capture coverage is incomplete",
        ));
    }
    if outputs.iter().any(|output| {
        u64::try_from(output.stdout.len().saturating_add(output.stderr.len())).unwrap_or(u64::MAX)
            > context.maximum_output_bytes
    }) {
        return Err(CommandFailure::evidence(
            "raw provider capture exceeds its sealed output bound",
        ));
    }
    let mut entries = Vec::with_capacity(outputs.len());
    for (index, output) in outputs.iter().enumerate() {
        let observed = canonical_digest(&(output.status, &output.stdout, &output.stderr))
            .map_err(|error| CommandFailure::evidence(error.to_string()))?;
        if let Some(capture) = captures.get(index)
            && observed != capture.raw_capture_digest
        {
            return Err(CommandFailure::evidence(
                "raw provider capture digest drifted before persistence",
            ));
        }
        let turn_index = captures.get(index).map_or_else(
            || u32::try_from(index).unwrap_or(u32::MAX),
            |capture| capture.turn_index,
        );
        let stem = format!("capture-{turn_index:03}");
        let stdout_path = format!("{stem}.stdout");
        let stderr_path = format!("{stem}.stderr");
        write_private_new(&root.join(&stdout_path), &output.stdout)?;
        write_private_new(&root.join(&stderr_path), &output.stderr)?;
        entries.push(RawCaptureIndexEntry {
            capture_order: u32::try_from(index).unwrap_or(u32::MAX),
            turn_index,
            status: output.status,
            raw_capture_digest: observed,
            stdout_path,
            stdout_digest: digest_bytes(&output.stdout),
            stdout_size_bytes: u64::try_from(output.stdout.len()).unwrap_or(u64::MAX),
            stderr_path,
            stderr_digest: digest_bytes(&output.stderr),
            stderr_size_bytes: u64::try_from(output.stderr.len()).unwrap_or(u64::MAX),
        });
    }
    let index = RawCaptureIndex {
        schema_version: "ao.next.raw-provider-capture-index.v2".into(),
        run_id: context.run_id.clone(),
        trial_id: context.trial_id.clone(),
        workspace_instance_id: context.workspace_instance_id.clone(),
        runtime_identity: context.runtime_identity.clone(),
        entries,
    };
    let digest =
        canonical_digest(&index).map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let bytes =
        serde_json::to_vec(&index).map_err(|error| CommandFailure::evidence(error.to_string()))?;
    publish_private_index(root, &bytes, true)?;
    Ok(digest)
}

fn publish_private_index(root: &Path, bytes: &[u8], publish: bool) -> Result<(), CommandFailure> {
    let incomplete = root.join("capture-index.json.incomplete");
    let completed = root.join("capture-index.json");
    write_private_new(&incomplete, bytes)?;
    if !publish {
        return Err(CommandFailure::evidence(
            "capture index publication was interrupted",
        ));
    }
    std::fs::hard_link(&incomplete, &completed)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    std::fs::File::open(root)
        .and_then(|directory| directory.sync_all())
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    std::fs::remove_file(&incomplete)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    std::fs::File::open(root)
        .and_then(|directory| directory.sync_all())
        .map_err(|error| CommandFailure::evidence(error.to_string()))
}

fn verify_raw_capture_index(
    root: &Path,
    context: &CaptureContext,
    expected_digest: &Digest,
    maximum_output_bytes: u64,
) -> Result<(), CommandFailure> {
    let bytes = read_bounded_path(&root.join("capture-index.json"), 1024 * 1024)
        .map_err(|error| CommandFailure::evidence(error.message))?;
    let index: RawCaptureIndex = decode_strict_json(&bytes, 1024 * 1024)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    if index.schema_version != "ao.next.raw-provider-capture-index.v2"
        || index.run_id != context.run_id
        || index.trial_id != context.trial_id
        || index.workspace_instance_id != context.workspace_instance_id
        || index.runtime_identity != context.runtime_identity
        || canonical_digest(&index).map_err(|error| CommandFailure::evidence(error.to_string()))?
            != *expected_digest
        || index.entries.is_empty()
    {
        return Err(CommandFailure::evidence(
            "raw capture index identity or digest is contradictory",
        ));
    }
    let mut paths = BTreeSet::new();
    for (capture_order, entry) in index.entries.iter().enumerate() {
        if entry.capture_order != u32::try_from(capture_order).unwrap_or(u32::MAX) {
            return Err(CommandFailure::evidence(
                "raw capture order is contradictory",
            ));
        }
        let stdout_path = checked_capture_path(root, &entry.stdout_path)?;
        let stderr_path = checked_capture_path(root, &entry.stderr_path)?;
        if !paths.insert(stdout_path.clone()) || !paths.insert(stderr_path.clone()) {
            return Err(CommandFailure::evidence("raw capture path is duplicated"));
        }
        let stdout = read_bounded_path(&stdout_path, maximum_output_bytes)
            .map_err(|error| CommandFailure::evidence(error.message))?;
        let stderr = read_bounded_path(&stderr_path, maximum_output_bytes)
            .map_err(|error| CommandFailure::evidence(error.message))?;
        if u64::try_from(stdout.len()).unwrap_or(u64::MAX) != entry.stdout_size_bytes
            || u64::try_from(stderr.len()).unwrap_or(u64::MAX) != entry.stderr_size_bytes
            || digest_bytes(&stdout) != entry.stdout_digest
            || digest_bytes(&stderr) != entry.stderr_digest
            || canonical_digest(&(entry.status, &stdout, &stderr))
                .map_err(|error| CommandFailure::evidence(error.to_string()))?
                != entry.raw_capture_digest
        {
            return Err(CommandFailure::evidence(
                "retained provider capture digest or byte count mismatched",
            ));
        }
    }
    Ok(())
}

fn record_capture_terminal(
    root: &Path,
    context: &CaptureContext,
    error: &CommandFailure,
    stage_override: Option<&str>,
) -> Result<(), CommandFailure> {
    let index_bytes = read_bounded_path(&root.join("capture-index.json"), 1024 * 1024)
        .map_err(|failure| CommandFailure::evidence(failure.message))?;
    let index: RawCaptureIndex = decode_strict_json(&index_bytes, 1024 * 1024)
        .map_err(|failure| CommandFailure::evidence(failure.to_string()))?;
    let capture_index_digest = canonical_digest(&index)
        .map_err(|failure| CommandFailure::evidence(failure.to_string()))?;
    let failure_stage = stage_override
        .or_else(|| {
            error
                .diagnostic
                .as_ref()
                .and_then(|diagnostic| diagnostic.get("stage"))
                .and_then(serde_json::Value::as_str)
                .or_else(|| {
                    if error.message.contains("capture failure") {
                        Some("capture")
                    } else if error.message.contains("provider")
                        || error.message.contains("adapter")
                    {
                        Some("provider")
                    } else if error.code == "evidence_failure" {
                        Some("evidence")
                    } else {
                        Some("control")
                    }
                })
        })
        .unwrap_or("control");
    let message = error
        .message
        .replace(&context.run_id, "<run>")
        .chars()
        .take(1024)
        .collect::<String>();
    let terminal = serde_json::json!({
        "schema_version": "ao.next.raw-provider-capture-terminal.v1",
        "run_id": context.run_id,
        "trial_id": context.trial_id,
        "workspace_instance_id": context.workspace_instance_id,
        "failure_stage": failure_stage,
        "error_code": error.code,
        "message": message,
        "diagnostic_digest": error.diagnostic.as_ref().map(canonical_digest).transpose()
            .map_err(|failure| CommandFailure::evidence(failure.to_string()))?,
        "capture_index_digest": capture_index_digest,
    });
    let bytes = serde_json::to_vec(&terminal)
        .map_err(|failure| CommandFailure::evidence(failure.to_string()))?;
    write_private_new(&root.join("capture-terminal.json"), &bytes)
}

fn checked_capture_path(root: &Path, relative: &str) -> Result<PathBuf, CommandFailure> {
    let path = Path::new(relative);
    let mut components = path.components();
    if !matches!(components.next(), Some(Component::Normal(_))) || components.next().is_some() {
        return Err(CommandFailure::evidence("raw capture path is unsafe"));
    }
    let path = root.join(path);
    let metadata = std::fs::symlink_metadata(&path)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    if metadata.file_type().is_symlink() || !metadata.is_file() {
        return Err(CommandFailure::evidence(
            "raw capture is not a regular non-symlink file",
        ));
    }
    Ok(path)
}

fn write_private_new(path: &Path, bytes: &[u8]) -> Result<(), CommandFailure> {
    let mut options = std::fs::OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt as _;
        options.mode(0o600);
    }
    let mut file = options
        .open(path)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    write_all_exact(&mut file, bytes)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    file.sync_all()
        .map_err(|error| CommandFailure::evidence(error.to_string()))
}

fn write_all_exact(writer: &mut impl std::io::Write, bytes: &[u8]) -> std::io::Result<()> {
    writer.write_all(bytes)
}

fn ensure_outside_roots(path: &Path, roots: &[PathBuf]) -> Result<(), CommandFailure> {
    let canonical = std::fs::canonicalize(path)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    for root in roots {
        let canonical_root = std::fs::canonicalize(root)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
        if canonical.starts_with(canonical_root) {
            return Err(CommandFailure::invalid_input(
                "hidden-test root is reachable through worker authority",
            ));
        }
    }
    Ok(())
}

fn runtime_matches_profile(
    variant: LiveVariant,
    provider_runtime: &str,
    profile_runtime: &str,
) -> bool {
    match variant {
        LiveVariant::N0 => provider_runtime == "codex" && profile_runtime == "current-ao",
        LiveVariant::N4 => provider_runtime == "codex" && profile_runtime == "codex",
        LiveVariant::N7 => {
            matches!(provider_runtime, "codex" | "claude")
                && (profile_runtime == provider_runtime
                    || profile_runtime == format!("ao-next-{provider_runtime}"))
        }
    }
}

fn validate_current_ao_binding(
    binding: &CurrentAoBinding,
    profile: &VariantProfile,
) -> Result<(), CommandFailure> {
    if binding.schema_version != "ao.next.current-ao-binding.v1"
        || binding.adapter_version != profile.adapter_version
        || canonical_digest(binding)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
            != profile.adapter_digest
    {
        return Err(CommandFailure::invalid_input(
            "current-AO adapter binding drifted",
        ));
    }
    for (program, expected) in [
        (&binding.ao2_program, &binding.ao2_program_digest),
        (&binding.provider_program, &binding.provider_program_digest),
    ] {
        if !program.is_absolute() {
            return Err(CommandFailure::invalid_input(
                "current-AO program path is not absolute",
            ));
        }
        let metadata = std::fs::symlink_metadata(program)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
        if metadata.file_type().is_symlink()
            || !metadata.is_file()
            || metadata.len() > 512 * 1024 * 1024
        {
            return Err(CommandFailure::invalid_input(
                "current-AO program is not a bounded regular non-symlink file",
            ));
        }
        let observed = digest_regular_file(program)?;
        if &observed != expected {
            return Err(CommandFailure::invalid_input(
                "current-AO program digest drifted",
            ));
        }
    }
    Ok(())
}

fn digest_regular_file(path: &Path) -> Result<Digest, CommandFailure> {
    let mut file = std::fs::File::open(path)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let mut hasher = Sha256::new();
    std::io::copy(&mut file, &mut hasher)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    Digest::new(format!("sha256:{:x}", hasher.finalize()))
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))
}

fn direct_prompt(input: &LiveRunInput) -> Result<String, CommandFailure> {
    serde_json::to_string(&serde_json::json!({
        "schema_version": "ao.next.native-direct-prompt.v1",
        "objective": input.request.objective,
        "run_id": input.request.run_id,
        "source": input.request.source,
        "workspace": input.request.workspace,
        "policy_digest": input.request.policy_digest,
        "verifier_profile_digest": input.request.verifier_profile.profile_digest,
        "constraints": {
            "worker_count": 1,
            "dynamic_fanout": false,
            "network": "denied",
            "credentials": "denied"
        }
    }))
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))
}

fn current_ao_prompt(input: &LiveRunInput) -> Result<String, CommandFailure> {
    serde_json::to_string(&serde_json::json!({
        "schema_version": "ao.next.current-ao-prompt.v1",
        "objective": input.request.objective,
        "run_id": input.request.run_id,
        "source": input.request.source,
        "workspace": input.request.workspace,
        "policy_digest": input.request.policy_digest,
        "verifier_profile_digest": input.request.verifier_profile.profile_digest,
        "constraints": {
            "worker_count": 1,
            "dynamic_fanout": false,
            "network": "denied",
            "credentials": "denied",
            "external_effects": "denied"
        }
    }))
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))
}

fn invocation_limits(request: &RunRequest) -> Result<InvocationLimits, CommandFailure> {
    Ok(InvocationLimits {
        max_input_bytes: usize::try_from(request.limits.max_input_bytes)
            .map_err(|_| CommandFailure::invalid_input("input limit is too large"))?,
        max_output_bytes: usize::try_from(request.limits.max_output_bytes)
            .map_err(|_| CommandFailure::invalid_input("output limit is too large"))?,
        timeout_ms: request.limits.max_run_ms,
    })
}

fn ensure_bounded_regular_under_roots(
    path: &Path,
    roots: &[PathBuf],
    maximum_bytes: u64,
) -> Result<(), CommandFailure> {
    let bytes = read_bounded_path(path, maximum_bytes)?;
    decode_strict_json::<serde_json::Value>(
        &bytes,
        usize::try_from(maximum_bytes).unwrap_or(usize::MAX),
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let canonical = std::fs::canonicalize(path)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let allowed = roots.iter().try_fold(false, |matched, root| {
        let metadata = std::fs::symlink_metadata(root)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
        if metadata.file_type().is_symlink() || !metadata.is_dir() {
            return Err(CommandFailure::invalid_input(
                "authority root is not a regular non-symlink directory",
            ));
        }
        let root = std::fs::canonicalize(root)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
        Ok(matched || canonical.starts_with(root))
    })?;
    if !allowed {
        return Err(CommandFailure::invalid_input(
            "output schema is outside authority roots",
        ));
    }
    Ok(())
}

#[allow(
    clippy::too_many_lines,
    reason = "the deterministic seed repository contract is intentionally visible in one boundary"
)]
fn prepare_git_workspace(
    root: &Path,
    allowed_roots: &[PathBuf],
    seed_digest: &Digest,
) -> Result<GitWorkspaceIdentity, CommandFailure> {
    let metadata = std::fs::symlink_metadata(root)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if metadata.file_type().is_symlink() || !metadata.is_dir() {
        return Err(CommandFailure::invalid_input(
            "workspace is not a regular non-symlink directory",
        ));
    }
    let repository_root = std::fs::canonicalize(root)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let allowed = allowed_roots
        .iter()
        .try_fold(false, |matched, allowed_root| {
            let metadata = std::fs::symlink_metadata(allowed_root)
                .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
            if metadata.file_type().is_symlink() || !metadata.is_dir() {
                return Err(CommandFailure::invalid_input(
                    "authority root is not a regular non-symlink directory",
                ));
            }
            let allowed_root = std::fs::canonicalize(allowed_root)
                .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
            Ok(matched || repository_root.starts_with(allowed_root))
        })?;
    if !allowed {
        return Err(CommandFailure::invalid_input(
            "workspace is outside exact authority roots",
        ));
    }
    reject_git_metadata(&repository_root)?;

    run_git_checked(
        &repository_root,
        vec![
            "init".into(),
            "--quiet".into(),
            format!("--initial-branch={GIT_BRANCH}"),
            "--template=".into(),
            ".".into(),
        ],
        "initialize repository",
    )?;
    run_git_checked(
        &repository_root,
        vec![
            "-c".into(),
            "core.autocrlf=false".into(),
            "-c".into(),
            "core.safecrlf=false".into(),
            "add".into(),
            "--all".into(),
            "--".into(),
            ".".into(),
        ],
        "stage sealed seed",
    )?;
    run_git_checked(
        &repository_root,
        vec![
            "-c".into(),
            "user.name=AO Next".into(),
            "-c".into(),
            "user.email=ao-next@invalid".into(),
            "-c".into(),
            "commit.gpgSign=false".into(),
            "commit".into(),
            "--quiet".into(),
            "--allow-empty".into(),
            "--no-verify".into(),
            "--cleanup=verbatim".into(),
            "--message".into(),
            format!(
                "AO Next sealed workspace seed\n\nSeed-Digest: {}",
                seed_digest.as_str()
            ),
        ],
        "commit sealed seed",
    )?;

    let common_dir = PathBuf::from(git_text(
        &repository_root,
        vec![
            "rev-parse".into(),
            "--path-format=absolute".into(),
            "--git-common-dir".into(),
        ],
        "resolve Git common directory",
    )?);
    let repository_top = PathBuf::from(git_text(
        &repository_root,
        vec![
            "rev-parse".into(),
            "--path-format=absolute".into(),
            "--show-toplevel".into(),
        ],
        "resolve Git repository root",
    )?);
    let head_commit = git_text(
        &repository_root,
        vec!["rev-parse".into(), "HEAD".into()],
        "resolve Git HEAD",
    )?;
    let branch = git_text(
        &repository_root,
        vec!["symbolic-ref".into(), "--short".into(), "HEAD".into()],
        "resolve Git branch",
    )?;
    let identity = GitWorkspaceIdentity {
        repository_root,
        common_dir,
        head_commit,
        branch: GIT_BRANCH,
    };
    if branch != GIT_BRANCH
        || std::fs::canonicalize(&repository_top).ok() != Some(identity.repository_root.clone())
        || identity.head_commit.len() != 40
        || !identity
            .head_commit
            .bytes()
            .all(|byte| byte.is_ascii_hexdigit())
    {
        return Err(CommandFailure::runtime(
            "prepared Git repository identity drifted",
        ));
    }
    verify_git_workspace(&identity, true)?;
    Ok(identity)
}

fn reject_git_metadata(root: &Path) -> Result<(), CommandFailure> {
    let mut pending = vec![root.to_path_buf()];
    while let Some(directory) = pending.pop() {
        for entry in std::fs::read_dir(&directory)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
        {
            let path = entry
                .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
                .path();
            if path.file_name().and_then(|name| name.to_str()) == Some(".git") {
                return Err(CommandFailure::invalid_input(
                    "workspace contains preexisting or nested Git metadata",
                ));
            }
            let metadata = std::fs::symlink_metadata(&path)
                .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
            if metadata.file_type().is_symlink() {
                return Err(CommandFailure::invalid_input(
                    "workspace contains a symlink",
                ));
            }
            if metadata.is_dir() {
                pending.push(path);
            } else if !metadata.is_file() {
                return Err(CommandFailure::invalid_input(
                    "workspace contains a non-regular entry",
                ));
            }
        }
    }
    Ok(())
}

fn git_environment_args() -> Vec<String> {
    vec![
        "-i".into(),
        "PATH=/usr/bin:/bin".into(),
        "HOME=/var/empty".into(),
        "LANG=C".into(),
        "LC_ALL=C".into(),
        "TZ=UTC".into(),
        "GIT_CONFIG_NOSYSTEM=1".into(),
        "GIT_CONFIG_GLOBAL=/dev/null".into(),
        "GIT_CONFIG_SYSTEM=/dev/null".into(),
        "GIT_AUTHOR_NAME=AO Next".into(),
        "GIT_AUTHOR_EMAIL=ao-next@invalid".into(),
        format!("GIT_AUTHOR_DATE={GIT_TIMESTAMP}"),
        "GIT_COMMITTER_NAME=AO Next".into(),
        "GIT_COMMITTER_EMAIL=ao-next@invalid".into(),
        format!("GIT_COMMITTER_DATE={GIT_TIMESTAMP}"),
        GIT_PROGRAM.into(),
    ]
}

fn run_git_checked(
    root: &Path,
    args: Vec<String>,
    stage: &str,
) -> Result<InvocationOutput, CommandFailure> {
    let mut environment_args = git_environment_args();
    environment_args.extend(args);
    let mut runner = BoundedProcessRunner;
    let output = runner
        .run(
            &PreparedInvocation {
                program: ENV_PROGRAM.into(),
                args: environment_args,
                stdin: Vec::new(),
                cwd: root.to_path_buf(),
                limits: InvocationLimits {
                    max_input_bytes: 0,
                    max_output_bytes: GIT_OUTPUT_LIMIT,
                    timeout_ms: GIT_TIMEOUT_MS,
                },
            },
            &CancellationToken::new(),
        )
        .map_err(|error| CommandFailure::runtime(format!("Git {stage} failed: {error}")))?;
    if output.status != 0 {
        let diagnostic = String::from_utf8_lossy(&output.stderr)
            .replace(&root.display().to_string(), "<workspace>");
        return Err(CommandFailure::runtime(format!(
            "Git {stage} exited {}: {}",
            output.status,
            diagnostic.trim()
        )));
    }
    Ok(output)
}

fn git_text(root: &Path, args: Vec<String>, stage: &str) -> Result<String, CommandFailure> {
    let output = run_git_checked(root, args, stage)?;
    let text = String::from_utf8(output.stdout)
        .map_err(|_| CommandFailure::runtime(format!("Git {stage} returned non-UTF-8")))?;
    let text = text.trim_end_matches(['\r', '\n']);
    if text.is_empty() || text.contains('\0') || text.contains('\n') || text.contains('\r') {
        return Err(CommandFailure::runtime(format!(
            "Git {stage} returned malformed output"
        )));
    }
    Ok(text.to_string())
}

fn verify_git_workspace(
    identity: &GitWorkspaceIdentity,
    require_clean: bool,
) -> Result<(), CommandFailure> {
    let git_metadata = std::fs::symlink_metadata(identity.repository_root.join(".git"))
        .map_err(|error| CommandFailure::runtime(error.to_string()))?;
    if git_metadata.file_type().is_symlink() || !git_metadata.is_dir() {
        return Err(CommandFailure::runtime(
            "prepared root Git metadata is not an ordinary directory",
        ));
    }
    let common_dir = PathBuf::from(git_text(
        &identity.repository_root,
        vec![
            "rev-parse".into(),
            "--path-format=absolute".into(),
            "--git-common-dir".into(),
        ],
        "verify common directory",
    )?);
    let head = git_text(
        &identity.repository_root,
        vec!["rev-parse".into(), "HEAD".into()],
        "verify HEAD",
    )?;
    let branch = git_text(
        &identity.repository_root,
        vec!["symbolic-ref".into(), "--short".into(), "HEAD".into()],
        "verify branch",
    )?;
    if std::fs::canonicalize(common_dir).ok() != std::fs::canonicalize(&identity.common_dir).ok()
        || head != identity.head_commit
        || branch != identity.branch
    {
        return Err(CommandFailure::runtime(
            "prepared Git workspace identity changed",
        ));
    }
    if require_clean {
        let status = run_git_checked(
            &identity.repository_root,
            vec![
                "status".into(),
                "--porcelain=v1".into(),
                "--untracked-files=all".into(),
            ],
            "verify clean workspace",
        )?;
        if !status.stdout.is_empty() || !status.stderr.is_empty() {
            return Err(CommandFailure::runtime(
                "prepared Git workspace is dirty before provider spawn",
            ));
        }
    }
    Ok(())
}

fn snapshot_tree(root: &Path, maximum_bytes: u64) -> Result<Vec<SnapshotEntry>, CommandFailure> {
    snapshot_product_tree(root, maximum_bytes, None)
}

fn snapshot_product_tree(
    root: &Path,
    maximum_bytes: u64,
    git_workspace: Option<&GitWorkspaceIdentity>,
) -> Result<Vec<SnapshotEntry>, CommandFailure> {
    if let Some(identity) = git_workspace {
        verify_git_workspace(identity, false)?;
    }
    let metadata = std::fs::symlink_metadata(root)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if metadata.file_type().is_symlink() || !metadata.is_dir() {
        return Err(CommandFailure::invalid_input(
            "snapshot root is not a regular non-symlink directory",
        ));
    }
    let canonical_root = std::fs::canonicalize(root)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let mut pending = vec![canonical_root.clone()];
    let mut paths = Vec::new();
    while let Some(directory) = pending.pop() {
        let entries = std::fs::read_dir(&directory)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
        for entry in entries {
            let path = entry
                .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
                .path();
            if path.file_name().and_then(|name| name.to_str()) == Some(".git") {
                if git_workspace.is_some() && directory == canonical_root {
                    continue;
                }
                return Err(CommandFailure::invalid_input(
                    "product snapshot contains unexpected Git metadata",
                ));
            }
            let metadata = std::fs::symlink_metadata(&path)
                .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
            if metadata.file_type().is_symlink() {
                return Err(CommandFailure::invalid_input(
                    "snapshot tree contains a symlink",
                ));
            }
            if metadata.is_dir() {
                pending.push(path);
            } else if metadata.is_file() {
                paths.push(path);
            } else {
                return Err(CommandFailure::invalid_input(
                    "snapshot tree contains a non-regular entry",
                ));
            }
        }
    }
    paths.sort();
    let mut total = 0_u64;
    let mut snapshot = Vec::with_capacity(paths.len());
    for path in paths {
        let relative = path
            .strip_prefix(&canonical_root)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
            .to_path_buf();
        if relative
            .components()
            .any(|component| !matches!(component, Component::Normal(_)))
        {
            return Err(CommandFailure::invalid_input(
                "snapshot tree contains an unsafe path",
            ));
        }
        let bytes = read_bounded_path(&path, maximum_bytes)?;
        total = total.saturating_add(u64::try_from(bytes.len()).unwrap_or(u64::MAX));
        if total > maximum_bytes {
            return Err(CommandFailure::invalid_input(
                "snapshot tree exceeds its byte bound",
            ));
        }
        snapshot.push(SnapshotEntry {
            path: relative,
            sha256: digest_bytes(&bytes),
            size_bytes: u64::try_from(bytes.len()).unwrap_or(u64::MAX),
        });
    }
    Ok(snapshot)
}

fn read_bounded_path(path: &Path, maximum_bytes: u64) -> Result<Vec<u8>, CommandFailure> {
    let metadata = std::fs::symlink_metadata(path)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if metadata.file_type().is_symlink() || !metadata.is_file() || metadata.len() > maximum_bytes {
        return Err(CommandFailure::invalid_input(
            "file is not a bounded regular non-symlink file",
        ));
    }
    std::fs::read(path).map_err(|error| CommandFailure::invalid_input(error.to_string()))
}

fn count_changed_files(before: &[SnapshotEntry], after: &[SnapshotEntry]) -> u32 {
    let before = before
        .iter()
        .map(|entry| (&entry.path, &entry.sha256))
        .collect::<BTreeMap<_, _>>();
    let after = after
        .iter()
        .map(|entry| (&entry.path, &entry.sha256))
        .collect::<BTreeMap<_, _>>();
    u32::try_from(
        before
            .keys()
            .chain(after.keys())
            .collect::<BTreeSet<_>>()
            .into_iter()
            .filter(|path| before.get(*path) != after.get(*path))
            .count(),
    )
    .unwrap_or(u32::MAX)
}

fn sum_capture_usage(captures: &[RuntimeCapture]) -> ao_next_core::adapter::TokenUsage {
    captures.iter().fold(
        ao_next_core::adapter::TokenUsage::default(),
        |mut total, capture| {
            total.input_tokens = total
                .input_tokens
                .saturating_add(capture.usage.input_tokens);
            total.cached_input_tokens = total
                .cached_input_tokens
                .saturating_add(capture.usage.cached_input_tokens);
            total.reasoning_tokens = total
                .reasoning_tokens
                .saturating_add(capture.usage.reasoning_tokens);
            total.output_tokens = total
                .output_tokens
                .saturating_add(capture.usage.output_tokens);
            total.output_bytes = total
                .output_bytes
                .saturating_add(capture.usage.output_bytes);
            total
        },
    )
}

#[cfg(test)]
mod tests {
    use std::collections::{BTreeSet, VecDeque};
    use std::process::Command;

    use ao_next_core::adapter::{
        AdapterAction, AdapterTurn, InvocationError, InvocationOutput, PreparedInvocation,
        TokenUsage,
    };
    use ao_next_core::contracts::{
        AuthorityEnvelope, Capability, ExternalEffectPolicy, ModelProfile, NetworkPolicy,
        RunLimits, SourceIdentity, StructuredCommand, VerifierProfile, WorkspaceIdentity,
    };
    use ao_next_core::verifier::CommandVerifierEntry;
    use ao_next_eval::corpus::{CorpusKind, counterbalanced_schedule};
    use chrono::Duration;
    use tempfile::TempDir;

    use super::*;

    struct FakeProvider {
        outputs: VecDeque<InvocationOutput>,
        direct_write: Option<PathBuf>,
        additional_write: Option<(PathBuf, Vec<u8>)>,
    }

    impl ProcessRunner for FakeProvider {
        fn run(
            &mut self,
            invocation: &PreparedInvocation,
            _: &CancellationToken,
        ) -> Result<InvocationOutput, InvocationError> {
            if invocation.args.first().is_some_and(|arg| arg == "adapter") {
                assert!(invocation.program.ends_with("true"));
            } else if invocation
                .args
                .windows(2)
                .any(|args| args == ["--sandbox", "workspace-write"])
            {
                if let Some(path) = self.direct_write.take() {
                    std::fs::write(path, b"ready\n").expect("direct fixture write");
                }
            } else {
                assert!(
                    invocation
                        .args
                        .windows(2)
                        .any(|args| args == ["--sandbox", "read-only"])
                );
            }
            if let Some((path, bytes)) = self.additional_write.take() {
                std::fs::write(path, bytes).expect("additional fixture write");
            }
            Ok(self.outputs.pop_front().expect("provider fixture output"))
        }
    }

    struct GitCheckingN0Runner {
        adapter_output: InvocationOutput,
        workspace: PathBuf,
        product: PathBuf,
    }

    struct CaptureCheckingVerifier {
        capture_root: PathBuf,
        inner: BoundedProcessRunner,
    }

    impl ProcessRunner for CaptureCheckingVerifier {
        fn run(
            &mut self,
            invocation: &PreparedInvocation,
            cancellation: &CancellationToken,
        ) -> Result<InvocationOutput, InvocationError> {
            if !self.capture_root.join("capture-index.json").is_file() {
                return Err(InvocationError::Io(
                    "capture missing before verifier".into(),
                ));
            }
            self.inner.run(invocation, cancellation)
        }
    }

    impl ProcessRunner for GitCheckingN0Runner {
        fn run(
            &mut self,
            invocation: &PreparedInvocation,
            _: &CancellationToken,
        ) -> Result<InvocationOutput, InvocationError> {
            match invocation.args.get(1).map(String::as_str) {
                Some("run") => Ok(self.adapter_output.clone()),
                Some("patch") if invocation.args.get(2).map(String::as_str) == Some("preview") => {
                    let output = Command::new("/usr/bin/git")
                        .args(["rev-parse", "--git-common-dir"])
                        .current_dir(&self.workspace)
                        .output()
                        .expect("real Git preview prerequisite");
                    if output.status.success() {
                        Ok(InvocationOutput {
                            status: 0,
                            stdout: serde_json::to_vec(&serde_json::json!({
                                "action_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
                            }))
                            .expect("preview JSON"),
                            stderr: output.stderr,
                        })
                    } else {
                        Ok(InvocationOutput {
                            status: output.status.code().unwrap_or(-1),
                            stdout: output.stdout,
                            stderr: output.stderr,
                        })
                    }
                }
                Some("patch") if invocation.args.get(2).map(String::as_str) == Some("apply") => {
                    std::fs::write(&self.product, b"ready\n").expect("applied product");
                    Ok(InvocationOutput {
                        status: 0,
                        stdout: serde_json::to_vec(&serde_json::json!({
                            "action_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
                        }))
                        .expect("apply JSON"),
                        stderr: Vec::new(),
                    })
                }
                _ => panic!("unexpected N0 command: {:?}", invocation.args),
            }
        }
    }

    struct Fixture {
        root: TempDir,
        input: LiveRunInput,
        product: PathBuf,
    }

    #[allow(
        clippy::too_many_lines,
        reason = "one self-contained fixture keeps all sealed identities internally consistent"
    )]
    fn fixture(variant: LiveVariant) -> Fixture {
        let root = TempDir::new().expect("temporary");
        let workspace = root.path().join("workspace");
        let protected = root.path().join("protected");
        let controls = root.path().join("controls");
        let visible = protected.join("visible");
        let hidden = protected.join("hidden");
        let raw_capture_root = protected.join("raw-captures");
        std::fs::create_dir_all(&workspace).expect("workspace");
        std::fs::create_dir_all(&visible).expect("visible");
        std::fs::create_dir_all(&hidden).expect("hidden");
        std::fs::create_dir_all(&raw_capture_root).expect("raw captures");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt as _;
            std::fs::set_permissions(&raw_capture_root, std::fs::Permissions::from_mode(0o700))
                .expect("private raw captures");
        }
        std::fs::create_dir_all(&controls).expect("controls");
        std::fs::write(visible.join("example.txt"), b"visible\n").expect("visible fixture");
        std::fs::write(
            hidden.join("test_product.py"),
            b"from pathlib import Path\nassert Path.cwd().joinpath('product.txt').read_text() == 'ready\\n'\n",
        )
        .expect("hidden fixture");
        let objective_text = "Create product.txt containing ready followed by a newline.";
        let objective_path = protected.join("objective.md");
        std::fs::write(&objective_path, objective_text).expect("objective");
        let output_schema = controls.join("turn.schema.json");
        std::fs::write(&output_schema, b"{\"type\":\"object\"}").expect("schema");

        let workspace_files = snapshot_tree(&workspace, 64 * 1024).expect("workspace snapshot");
        let workspace_digest = canonical_digest(&workspace_files).expect("workspace digest");
        let source_snapshot = SourceSnapshot {
            schema_version: "ao.next.source-snapshot.v1".into(),
            task_id: "greenfield-engineering-app".into(),
            tree_digest: workspace_digest.clone(),
            files: workspace_files,
        };
        let source_digest = canonical_digest(&source_snapshot).expect("source digest");
        let source_path = protected.join("source-snapshot.json");
        std::fs::write(
            &source_path,
            serde_json::to_vec(&source_snapshot).expect("source JSON"),
        )
        .expect("source snapshot");
        let visible_digest =
            canonical_digest(&snapshot_tree(&visible, 64 * 1024).expect("visible snapshot"))
                .expect("visible digest");
        let hidden_digest =
            canonical_digest(&snapshot_tree(&hidden, 64 * 1024).expect("hidden snapshot"))
                .expect("hidden digest");
        let verifier_program = "/usr/bin/python3".to_owned();
        let mut verifier_entry = CommandVerifierEntry {
            verifier_id: "hidden-product-check".into(),
            verifier_digest: digest_bytes(b"unsealed verifier entry"),
            program: verifier_program.clone(),
            args: vec![hidden.join("test_product.py").display().to_string()],
            working_directory: PathBuf::new(),
            timeout_ms: 5_000,
            max_output_bytes: 16 * 1024,
            expected_exit_status: 0,
            required_artifacts: Vec::new(),
        };
        verifier_entry.verifier_digest = verifier_entry.calculated_digest().expect("entry digest");
        let mut command_verifier = CommandVerifierProfile {
            schema_version: "ao.next.command-verifier-profile.v1".into(),
            profile_id: "hidden-product-verifier-v1".into(),
            profile_digest: digest_bytes(b"unsealed verifier profile"),
            entries: vec![verifier_entry.clone()],
        };
        command_verifier.profile_digest = command_verifier
            .calculated_digest()
            .expect("verifier profile digest");
        let current_program = PathBuf::from("/usr/bin/true");
        let current_program_digest =
            digest_bytes(&std::fs::read(&current_program).expect("current-AO fixture program"));
        let current_ao = CurrentAoBinding {
            schema_version: "ao.next.current-ao-binding.v1".into(),
            ao2_program: current_program.clone(),
            ao2_program_digest: current_program_digest.clone(),
            provider_program: current_program,
            provider_program_digest: current_program_digest,
            adapter_version: "current-ao-native-v1".into(),
        };
        let mut profiles = [
            profile(ExecutionVariant::N0, "current-ao", "current-ao-native-v1"),
            profile(ExecutionVariant::N4, "codex", "native-codex-direct-v1"),
            profile(ExecutionVariant::N7, "ao-next-codex", "ao-next-process-v1"),
        ];
        profiles[0].adapter_digest =
            canonical_digest(&current_ao).expect("current-AO binding digest");
        let selected_profile = profiles
            .iter()
            .find(|profile| profile.variant == variant.execution_variant())
            .expect("selected profile")
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
            variant_profiles: profiles.to_vec(),
        };
        let mut corpus = CorpusManifest {
            schema_version: "ao.next.evaluation-corpus.v2".into(),
            corpus_kind: CorpusKind::SealedLive,
            corpus_digest: digest_bytes(b"unsealed corpus"),
            required_trial_count: 3,
            schedule: counterbalanced_schedule(),
            tasks: vec![
                selected_task,
                placeholder_task("bounded-defect-repair", "defect"),
                placeholder_task("artifact-reconciliation", "reconciliation"),
            ],
        };
        corpus.corpus_digest = corpus.calculated_digest().expect("corpus digest");
        let now = Utc::now();
        let request = RunRequest {
            schema_version: "ao.next.run-request.v1".into(),
            run_id: format!("fixture-run-{variant:?}"),
            objective: objective_text.into(),
            source: SourceIdentity {
                repository: "sealed-local-fixture".into(),
                head: source_digest,
            },
            workspace: WorkspaceIdentity {
                workspace_id: format!("fixture-workspace-{variant:?}"),
                root: workspace.clone(),
                seed_digest: workspace_digest,
            },
            model_profile: ModelProfile {
                runtime: "codex".into(),
                model_identifier: selected_profile.model_identifier.clone(),
                reasoning_effort: "high".into(),
                system_prompt_digest: selected_profile.prompt_digest.clone(),
                tool_contract_digest: selected_profile.adapter_digest.clone(),
                context_limit: 32_000,
                output_limit: 4_000,
                adapter_version: selected_profile.adapter_version.clone(),
            },
            authority: AuthorityEnvelope {
                schema_version: "ao.next.authority-envelope.v1".into(),
                issued_by: "offline-fixture".into(),
                issued_at: now - Duration::minutes(1),
                expires_at: now + Duration::hours(1),
                capabilities: BTreeSet::from([Capability::RunLocalProgram]),
                allowed_roots: vec![workspace.clone(), controls.clone()],
                allowed_programs: BTreeSet::from([verifier_program.clone()]),
                network: NetworkPolicy::Denied,
                allowed_network_hosts: BTreeSet::new(),
                external_effects: ExternalEffectPolicy::Denied,
            },
            verifier_profile: VerifierProfile {
                profile_id: command_verifier.profile_id.clone(),
                profile_digest: command_verifier.profile_digest.clone(),
                commands: vec![StructuredCommand {
                    program: verifier_program,
                    args: verifier_entry.args.clone(),
                    timeout_ms: verifier_entry.timeout_ms,
                }],
                required_artifacts: Vec::new(),
            },
            policy_digest: selected_profile.policy_digest.clone(),
            limits: RunLimits {
                max_input_bytes: 64 * 1024,
                max_turns: 2,
                max_repair_attempts: 0,
                max_run_ms: 10_000,
                max_effect_timeout_ms: 5_000,
                max_output_bytes: 64 * 1024,
                max_tokens: 10_000,
            },
        };
        let schedule_position = match variant {
            LiveVariant::N0 => 0,
            LiveVariant::N4 => 1,
            LiveVariant::N7 => 2,
        };
        Fixture {
            product: workspace.join("product.txt"),
            input: LiveRunInput {
                schema_version: "ao.next.live-run-input.v1".into(),
                corpus,
                task_id: "greenfield-engineering-app".into(),
                trial_id: format!("fixture-trial-{variant:?}"),
                trial_index: 0,
                schedule_position,
                workspace_instance_id: request.workspace.workspace_id.clone(),
                source_snapshot: source_path,
                objective: objective_path,
                visible_fixtures: visible,
                hidden_tests: hidden,
                output_schema,
                raw_capture_root,
                request,
                command_verifier,
                current_ao: (variant == LiveVariant::N0).then_some(current_ao),
            },
            root,
        }
    }

    fn profile(variant: ExecutionVariant, runtime: &str, adapter_version: &str) -> VariantProfile {
        VariantProfile {
            variant,
            runtime: runtime.into(),
            runtime_digest: digest_bytes(format!("{runtime}:runtime").as_bytes()),
            model_identifier: "fixed-live-model".into(),
            model_digest: canonical_digest(&("fixed-live-model", "high"))
                .expect("model binding digest"),
            prompt_digest: digest_bytes(format!("{runtime}:prompt").as_bytes()),
            policy_digest: digest_bytes(format!("{runtime}:policy").as_bytes()),
            adapter_version: adapter_version.into(),
            adapter_digest: digest_bytes(format!("{runtime}:{adapter_version}").as_bytes()),
        }
    }

    fn placeholder_task(task_id: &str, salt: &str) -> EvaluationTask {
        EvaluationTask {
            task_id: task_id.into(),
            task_kind: "sealed_local_task".into(),
            source_digest: digest_bytes(format!("{salt}:source").as_bytes()),
            objective_digest: digest_bytes(format!("{salt}:objective").as_bytes()),
            workspace_seed_digest: digest_bytes(format!("{salt}:seed").as_bytes()),
            visible_fixtures_digest: digest_bytes(format!("{salt}:visible").as_bytes()),
            hidden_tests_digest: digest_bytes(format!("{salt}:hidden").as_bytes()),
            verifier_profile_digest: digest_bytes(format!("{salt}:verifier").as_bytes()),
            variant_profiles: vec![
                profile(ExecutionVariant::N0, "current-ao", "current-ao-native-v1"),
                profile(ExecutionVariant::N4, "codex", "native-codex-direct-v1"),
                profile(ExecutionVariant::N7, "ao-next-codex", "ao-next-process-v1"),
            ],
        }
    }

    fn codex_output(turn: Option<&AdapterTurn>) -> InvocationOutput {
        let mut lines = Vec::new();
        if let Some(turn) = turn {
            lines.push(
                serde_json::to_string(&serde_json::json!({
                    "type": "item.completed",
                    "item": {
                        "type": "agent_message",
                        "text": serde_json::to_string(turn).expect("turn JSON")
                    }
                }))
                .expect("event JSON"),
            );
        }
        lines.push(
            serde_json::to_string(&serde_json::json!({
                "type": "turn.completed",
                "usage": {
                    "input_tokens": 11,
                    "cached_input_tokens": 3,
                    "reasoning_tokens": 5,
                    "output_tokens": 2
                }
            }))
            .expect("usage JSON"),
        );
        InvocationOutput {
            status: 0,
            stdout: format!("{}\n", lines.join("\n")).into_bytes(),
            stderr: Vec::new(),
        }
    }

    fn current_ao_outputs(workspace: &Path, sandbox: &Path) -> VecDeque<InvocationOutput> {
        let provider = codex_output(None);
        let adapter = serde_json::json!({
            "adapter": {
                "provider": "codex",
                "role_id": "ao-next-n0-worker-01",
                "command": "codex exec",
                "exit_code": 0,
                "stdout": String::from_utf8(provider.stdout).expect("provider stdout"),
                "stderr": String::from_utf8(provider.stderr).expect("provider stderr"),
                "transcript": "",
                "blocker": null
            },
            "target_repo": workspace,
            "sandbox_path": sandbox,
            "changed_files": ["product.txt"],
            "diff_summary": "added product.txt",
            "transcript_summary": {
                "changed_files": ["product.txt"],
                "concerns": [],
                "blockers": [],
                "usage": {
                    "input_tokens": 11,
                    "output_tokens": 2,
                    "total_tokens": 21
                },
                "cost_usd": null,
                "raw_summary": null,
                "transcript_ids": []
            }
        });
        let digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
        VecDeque::from([
            InvocationOutput {
                status: 0,
                stdout: serde_json::to_vec(&adapter).expect("current-AO JSON"),
                stderr: Vec::new(),
            },
            InvocationOutput {
                status: 0,
                stdout: serde_json::to_vec(&serde_json::json!({
                    "action_digest": digest
                }))
                .expect("preview JSON"),
                stderr: Vec::new(),
            },
            InvocationOutput {
                status: 0,
                stdout: serde_json::to_vec(&serde_json::json!({
                    "action_digest": digest
                }))
                .expect("apply JSON"),
                stderr: Vec::new(),
            },
        ])
    }

    #[test]
    fn n0_prepares_canonical_git_base_before_real_preview_boundary() {
        let n0 = fixture(LiveVariant::N0);
        let sandbox = n0.root.path().join("ao2-sandbox-git-check");
        std::fs::create_dir_all(&sandbox).expect("current-AO sandbox");
        let adapter_output = current_ao_outputs(&n0.input.request.workspace.root, &sandbox)
            .pop_front()
            .expect("adapter output");

        let output = execute_with_runners(
            &n0.input,
            LiveVariant::N0,
            MeasurementOrigin::OfflineFixture,
            GitCheckingN0Runner {
                adapter_output,
                workspace: n0.input.request.workspace.root.clone(),
                product: n0.product.clone(),
            },
            BoundedProcessRunner,
        )
        .expect("deterministic Git workspace enables real N0 preview boundary");

        assert_eq!(output.status, 0);
        assert!(n0.input.request.workspace.root.join(".git").is_dir());
    }

    #[test]
    fn git_preparation_is_deterministic_seed_bound_and_product_neutral() {
        let mut identities = Vec::new();
        for _ in 0..3 {
            let temporary = TempDir::new().expect("workspace parent");
            let workspace = temporary.path().join("workspace");
            std::fs::create_dir(&workspace).expect("workspace");
            std::fs::write(workspace.join("product.txt"), b"sealed product\n")
                .expect("sealed product");
            let before = snapshot_tree(&workspace, 64 * 1024).expect("snapshot before Git");
            let identity = prepare_git_workspace(
                &workspace,
                &[workspace.clone()],
                &digest_bytes(b"same seed"),
            )
            .expect("deterministic Git workspace");
            let after = snapshot_product_tree(&workspace, 64 * 1024, Some(&identity))
                .expect("snapshot after Git");
            assert_eq!(before, after);
            verify_git_workspace(&identity, true).expect("clean prepared workspace");
            identities.push((temporary, identity));
        }
        assert_eq!(identities[0].1.head_commit, identities[1].1.head_commit);
        assert_eq!(identities[1].1.head_commit, identities[2].1.head_commit);

        let empty_parent = TempDir::new().expect("empty workspace parent");
        let empty = empty_parent.path().join("empty");
        std::fs::create_dir(&empty).expect("empty workspace");
        let empty_identity =
            prepare_git_workspace(&empty, &[empty.clone()], &digest_bytes(b"same seed"))
                .expect("allowed empty seed commit");
        verify_git_workspace(&empty_identity, true).expect("empty seed is clean");

        let changed_parent = TempDir::new().expect("changed seed parent");
        let changed = changed_parent.path().join("workspace");
        std::fs::create_dir(&changed).expect("changed seed workspace");
        std::fs::write(changed.join("product.txt"), b"sealed product\n").expect("sealed product");
        let changed_identity = prepare_git_workspace(
            &changed,
            &[changed.clone()],
            &digest_bytes(b"different seed"),
        )
        .expect("different seed workspace");
        assert_ne!(identities[0].1.head_commit, changed_identity.head_commit);

        std::fs::write(
            identities[0].1.repository_root.join("product.txt"),
            b"dirty\n",
        )
        .expect("dirty seed");
        let error = verify_git_workspace(&identities[0].1, true).expect_err("dirty seed rejected");
        assert!(error.message.contains("dirty before provider spawn"));
    }

    #[test]
    fn git_preparation_rejects_existing_nested_and_unsafe_metadata() {
        for kind in ["directory", "file", "nested", "submodule"] {
            let temporary = TempDir::new().expect("workspace parent");
            let workspace = temporary.path().join("workspace");
            std::fs::create_dir(&workspace).expect("workspace");
            match kind {
                "directory" => std::fs::create_dir(workspace.join(".git")).expect("Git dir"),
                "file" => std::fs::write(workspace.join(".git"), b"gitdir: elsewhere\n")
                    .expect("Git file"),
                "nested" => {
                    std::fs::create_dir(workspace.join("nested")).expect("nested");
                    std::fs::create_dir(workspace.join("nested/.git")).expect("nested Git dir");
                }
                "submodule" => {
                    std::fs::create_dir(workspace.join("nested")).expect("nested");
                    std::fs::write(workspace.join("nested/.git"), b"gitdir: ../../modules/x\n")
                        .expect("submodule marker");
                }
                _ => unreachable!(),
            }
            let error =
                prepare_git_workspace(&workspace, &[workspace.clone()], &digest_bytes(b"seed"))
                    .expect_err("unexpected Git metadata rejected");
            assert!(error.message.contains("Git metadata"));
        }

        #[cfg(unix)]
        {
            use std::os::unix::fs::symlink;

            let temporary = TempDir::new().expect("symlink parent");
            let workspace = temporary.path().join("workspace");
            std::fs::create_dir(&workspace).expect("workspace");
            symlink(temporary.path(), workspace.join("nested-link")).expect("nested symlink");
            assert!(
                prepare_git_workspace(&workspace, &[workspace.clone()], &digest_bytes(b"seed"))
                    .is_err()
            );

            let link = temporary.path().join("workspace-link");
            symlink(&workspace, &link).expect("workspace symlink");
            assert!(prepare_git_workspace(&link, &[workspace], &digest_bytes(b"seed")).is_err());
        }
    }

    #[test]
    fn deterministic_git_environment_clears_host_identity_and_configuration() {
        let args = git_environment_args();
        assert_eq!(args.first().map(String::as_str), Some("-i"));
        for binding in [
            "GIT_CONFIG_NOSYSTEM=1",
            "GIT_CONFIG_GLOBAL=/dev/null",
            "GIT_CONFIG_SYSTEM=/dev/null",
            "GIT_AUTHOR_NAME=AO Next",
            "GIT_AUTHOR_EMAIL=ao-next@invalid",
            "GIT_COMMITTER_NAME=AO Next",
            "GIT_COMMITTER_EMAIL=ao-next@invalid",
        ] {
            assert!(args.iter().any(|argument| argument == binding));
        }
        assert!(
            args.iter()
                .any(|argument| argument == &format!("GIT_AUTHOR_DATE={GIT_TIMESTAMP}"))
        );
        assert!(
            args.iter()
                .any(|argument| argument == &format!("GIT_COMMITTER_DATE={GIT_TIMESTAMP}"))
        );
        assert_eq!(args.last().map(String::as_str), Some(GIT_PROGRAM));
    }

    #[test]
    fn n0_retains_exact_provider_bytes_before_preview_failure() {
        let n0 = fixture(LiveVariant::N0);
        let sandbox = n0.root.path().join("ao2-sandbox-preview-failure");
        std::fs::create_dir_all(&sandbox).expect("current-AO sandbox");
        let mut outputs = current_ao_outputs(&n0.input.request.workspace.root, &sandbox);
        let expected = decode_current_ao_output(
            outputs.front().expect("adapter output"),
            &n0.input.request.workspace.root,
            invocation_limits(&n0.input.request).expect("limits"),
        )
        .expect("provider bytes");
        outputs.get_mut(1).expect("preview output").status = 1;
        outputs.get_mut(1).expect("preview output").stderr =
            b"fatal: resolve Git common directory\n".to_vec();

        execute_with_runners(
            &n0.input,
            LiveVariant::N0,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs,
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("preview failure remains an infrastructure failure");

        assert_eq!(
            std::fs::read(n0.input.raw_capture_root.join("capture-000.stdout"))
                .expect("retained stdout"),
            expected.stdout
        );
        assert_eq!(
            std::fs::read(n0.input.raw_capture_root.join("capture-000.stderr"))
                .expect("retained stderr"),
            expected.stderr
        );
        assert!(
            n0.input
                .raw_capture_root
                .join("capture-index.json")
                .is_file()
        );
        let terminal: serde_json::Value = serde_json::from_slice(
            &std::fs::read(n0.input.raw_capture_root.join("capture-terminal.json"))
                .expect("capture terminal metadata"),
        )
        .expect("capture terminal JSON");
        assert_eq!(terminal["failure_stage"], "preview");
        assert!(terminal["capture_index_digest"].as_str().is_some());
        assert_eq!(
            std::fs::read_dir(&n0.input.raw_capture_root)
                .expect("capture root")
                .count(),
            4
        );
    }

    #[test]
    fn n0_preview_failure_exposes_bounded_structured_ao2_diagnostic() {
        let n0 = fixture(LiveVariant::N0);
        let sandbox = n0.root.path().join("ao2-sandbox-diagnostic");
        std::fs::create_dir_all(&sandbox).expect("current-AO sandbox");
        let mut outputs = current_ao_outputs(&n0.input.request.workspace.root, &sandbox);
        outputs.get_mut(1).expect("preview output").status = 1;
        outputs.get_mut(1).expect("preview output").stderr =
            b"Error: resolve Git common directory\n".to_vec();
        let expected_program_digest = n0
            .input
            .current_ao
            .as_ref()
            .expect("current-AO binding")
            .ao2_program_digest
            .clone();

        let error = execute_with_runners(
            &n0.input,
            LiveVariant::N0,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs,
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("preview failure");
        let diagnostic = error.diagnostic.expect("structured AO2 diagnostic");
        assert_eq!(diagnostic["stage"], "preview");
        assert_eq!(diagnostic["exit_status"], 1);
        assert_eq!(
            diagnostic["program_digest"],
            expected_program_digest.as_str()
        );
        assert!(diagnostic["elapsed_ms"].as_u64().is_some());
        assert!(
            diagnostic["stderr"]["bounded_text"]
                .as_str()
                .is_some_and(|text| text.contains("resolve Git common directory"))
        );
        assert!(diagnostic["stderr"]["digest"].as_str().is_some());
        assert_eq!(diagnostic["command"][0], "adapter");
        assert!(diagnostic["target_identity"].as_str().is_some());
        assert!(diagnostic["sandbox_identity"].as_str().is_some());
    }

    #[test]
    fn n4_retains_provider_bytes_before_verifier_execution() {
        let n4 = fixture(LiveVariant::N4);
        let output = execute_with_runners(
            &n4.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(None)]),
                direct_write: Some(n4.product.clone()),
                additional_write: None,
            },
            CaptureCheckingVerifier {
                capture_root: n4.input.raw_capture_root.clone(),
                inner: BoundedProcessRunner,
            },
        )
        .expect("capture-first N4 run");

        assert_eq!(output.status, 0);
    }

    #[cfg(unix)]
    #[test]
    fn capture_index_is_atomic_private_and_identity_bound() {
        use std::os::unix::fs::PermissionsExt as _;

        let n4 = fixture(LiveVariant::N4);
        execute_with_runners(
            &n4.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(None)]),
                direct_write: Some(n4.product.clone()),
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect("captured N4 run");

        let index_path = n4.input.raw_capture_root.join("capture-index.json");
        let index: serde_json::Value =
            serde_json::from_slice(&std::fs::read(&index_path).expect("capture index bytes"))
                .expect("capture index JSON");
        assert_eq!(index["run_id"], n4.input.request.run_id);
        assert_eq!(index["trial_id"], n4.input.trial_id);
        assert_eq!(
            index["workspace_instance_id"],
            n4.input.workspace_instance_id
        );
        assert_eq!(index["runtime_identity"]["runtime"], "codex");
        assert_eq!(index["entries"][0]["capture_order"], 0);
        assert_eq!(index["entries"][0]["status"], 0);
        assert!(index["entries"][0]["stdout_size_bytes"].as_u64().is_some());
        assert!(index["entries"][0]["stderr_size_bytes"].as_u64().is_some());
        for path in [
            index_path,
            n4.input.raw_capture_root.join("capture-000.stdout"),
            n4.input.raw_capture_root.join("capture-000.stderr"),
        ] {
            assert_eq!(
                std::fs::metadata(path)
                    .expect("capture metadata")
                    .permissions()
                    .mode()
                    & 0o777,
                0o600
            );
        }
        assert!(
            !n4.input
                .raw_capture_root
                .join("capture-index.json.incomplete")
                .exists()
        );
    }

    #[test]
    fn capture_index_verification_rejects_tampered_bytes_and_contradictions() {
        let tampered = fixture(LiveVariant::N4);
        let output = execute_with_runners(
            &tampered.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(None)]),
                direct_write: Some(tampered.product.clone()),
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect("captured N4 run");
        let expected = Digest::new(
            output.value["raw_capture_index_digest"]
                .as_str()
                .expect("capture index digest"),
        )
        .expect("valid digest");
        let context = capture_context(&tampered.input, LiveVariant::N4);
        verify_raw_capture_index(
            &tampered.input.raw_capture_root,
            &context,
            &expected,
            tampered.input.request.limits.max_output_bytes,
        )
        .expect("untouched capture verifies");
        std::fs::write(
            tampered.input.raw_capture_root.join("capture-000.stdout"),
            b"tampered",
        )
        .expect("tamper retained bytes");
        assert!(
            verify_raw_capture_index(
                &tampered.input.raw_capture_root,
                &context,
                &expected,
                tampered.input.request.limits.max_output_bytes,
            )
            .is_err()
        );

        let contradictory = fixture(LiveVariant::N4);
        let output = execute_with_runners(
            &contradictory.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(None)]),
                direct_write: Some(contradictory.product.clone()),
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect("second captured N4 run");
        let expected = Digest::new(
            output.value["raw_capture_index_digest"]
                .as_str()
                .expect("capture index digest"),
        )
        .expect("valid digest");
        let index_path = contradictory
            .input
            .raw_capture_root
            .join("capture-index.json");
        let mut index: serde_json::Value =
            serde_json::from_slice(&std::fs::read(&index_path).expect("index bytes"))
                .expect("index JSON");
        index["entries"][0]["stdout_size_bytes"] = serde_json::json!(999_999_u64);
        std::fs::write(
            &index_path,
            serde_json::to_vec(&index).expect("index bytes"),
        )
        .expect("contradict index");
        assert!(
            verify_raw_capture_index(
                &contradictory.input.raw_capture_root,
                &capture_context(&contradictory.input, LiveVariant::N4),
                &expected,
                contradictory.input.request.limits.max_output_bytes,
            )
            .is_err()
        );
    }

    #[test]
    fn capture_retention_rejects_interruption_duplicates_bounds_paths_and_short_writes() {
        struct ZeroWriter;
        impl std::io::Write for ZeroWriter {
            fn write(&mut self, _: &[u8]) -> std::io::Result<usize> {
                Ok(0)
            }

            fn flush(&mut self) -> std::io::Result<()> {
                Ok(())
            }
        }

        let mut zero = ZeroWriter;
        assert_eq!(
            write_all_exact(&mut zero, b"bytes")
                .expect_err("zero-length short write")
                .kind(),
            std::io::ErrorKind::WriteZero
        );

        let interrupted = TempDir::new().expect("interrupted capture root");
        write_private_new(&interrupted.path().join("capture-000.stdout"), b"stdout")
            .expect("stdout");
        write_private_new(&interrupted.path().join("capture-000.stderr"), b"stderr")
            .expect("stderr");
        assert!(publish_private_index(interrupted.path(), b"{}", false).is_err());
        assert!(
            interrupted
                .path()
                .join("capture-index.json.incomplete")
                .is_file()
        );
        assert!(!interrupted.path().join("capture-index.json").exists());
        assert!(interrupted.path().join("capture-000.stdout").is_file());

        let duplicate = fixture(LiveVariant::N4);
        let context = capture_context(&duplicate.input, LiveVariant::N4);
        let output = codex_output(None);
        let first = persist_raw_captures(
            &duplicate.input.raw_capture_root,
            &context,
            &[],
            std::slice::from_ref(&output),
        )
        .expect("first immutable capture");
        assert!(
            persist_raw_captures(
                &duplicate.input.raw_capture_root,
                &context,
                &[],
                std::slice::from_ref(&output),
            )
            .is_err()
        );
        verify_raw_capture_index(
            &duplicate.input.raw_capture_root,
            &context,
            &first,
            duplicate.input.request.limits.max_output_bytes,
        )
        .expect("first capture unchanged");

        let oversized = fixture(LiveVariant::N4);
        let mut context = capture_context(&oversized.input, LiveVariant::N4);
        context.maximum_output_bytes = 1;
        assert!(
            persist_raw_captures(
                &oversized.input.raw_capture_root,
                &context,
                &[],
                &[InvocationOutput {
                    status: 0,
                    stdout: b"too large".to_vec(),
                    stderr: Vec::new(),
                }],
            )
            .is_err()
        );
        assert_eq!(
            std::fs::read_dir(&oversized.input.raw_capture_root)
                .expect("empty oversized root")
                .count(),
            0
        );

        assert!(checked_capture_path(interrupted.path(), "../escape").is_err());
        #[cfg(unix)]
        {
            use std::os::unix::fs::symlink;

            let symlink_root = TempDir::new().expect("symlink capture root");
            let target = symlink_root.path().join("target");
            std::fs::write(&target, b"target").expect("target");
            symlink(&target, symlink_root.path().join("capture-000.stdout"))
                .expect("capture symlink");
            assert!(checked_capture_path(symlink_root.path(), "capture-000.stdout").is_err());

            use std::os::unix::fs::PermissionsExt as _;
            let unsafe_root = fixture(LiveVariant::N4);
            std::fs::set_permissions(
                &unsafe_root.input.raw_capture_root,
                std::fs::Permissions::from_mode(0o755),
            )
            .expect("unsafe permissions");
            assert!(validate_input(&unsafe_root.input, LiveVariant::N4, Utc::now()).is_err());
        }
    }

    #[test]
    fn offline_fake_n0_n4_and_n7_execute_without_the_live_environment_gate() {
        assert_ne!(
            std::env::var("AO_NEXT_LIVE_PROVIDER_CALLS").as_deref(),
            Ok("operator-authorized")
        );

        let n0 = fixture(LiveVariant::N0);
        let sandbox = n0.root.path().join("ao2-sandbox-fixture");
        std::fs::create_dir_all(&sandbox).expect("current-AO sandbox");
        let output = execute_with_runners(
            &n0.input,
            LiveVariant::N0,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: current_ao_outputs(&n0.input.request.workspace.root, &sandbox),
                direct_write: None,
                additional_write: Some((n0.product.clone(), b"ready\n".to_vec())),
            },
            BoundedProcessRunner,
        )
        .expect("offline N0");
        assert_eq!(output.status, 0);
        assert_eq!(output.value["measurement"]["variant"], "N0");
        assert_eq!(output.value["measurement"]["tokens"]["input_tokens"], 11);

        let n4 = fixture(LiveVariant::N4);
        let output = execute_with_runners(
            &n4.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(None)]),
                direct_write: Some(n4.product.clone()),
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect("offline N4");
        assert_eq!(output.status, 0);
        assert_eq!(output.value["terminal_state"], "passed");
        assert_eq!(output.value["measurement"]["variant"], "N4");
        assert_eq!(output.value["measurement"]["provider_usage_trusted"], true);
        assert_eq!(
            output.value["measurement"]["measurement_origin"],
            "offline_fixture"
        );
        assert_eq!(output.value["measurement"]["hidden_tests_passed"], 1);
        assert_eq!(output.value["measurement"]["worker_count"], 1);
        assert_eq!(output.value["measurement"]["dynamic_fanout"], false);

        let n7 = fixture(LiveVariant::N7);
        let script = format!(
            "from pathlib import Path; Path({:?}).write_text('ready\\n')",
            n7.product.display().to_string()
        );
        let turn = AdapterTurn {
            actions: vec![
                AdapterAction::Effect(ao_next_core::contracts::EffectRequest {
                    effect_id: "write-product".into(),
                    run_id: n7.input.request.run_id.clone(),
                    kind: ao_next_core::contracts::EffectKind::RunProgram,
                    program: Some("/usr/bin/python3".into()),
                    args: vec!["-c".into(), script.clone()],
                    paths: vec![n7.input.request.workspace.root.clone()],
                    timeout_ms: 5_000,
                    input_digest: digest_bytes(script.as_bytes()),
                }),
                AdapterAction::Verify,
            ],
            usage: TokenUsage {
                input_tokens: 999,
                cached_input_tokens: 999,
                reasoning_tokens: 999,
                output_tokens: 999,
                output_bytes: 999,
            },
            model_claimed_success: true,
            control_mutations: Vec::new(),
        };
        let output = execute_with_runners(
            &n7.input,
            LiveVariant::N7,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(Some(&turn))]),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect("offline N7");
        assert_eq!(output.status, 0);
        assert_eq!(output.value["terminal_state"], "passed");
        assert_eq!(output.value["measurement"]["variant"], "N7");
        assert_eq!(output.value["measurement"]["tokens"]["input_tokens"], 11);
        assert_eq!(output.value["measurement"]["hidden_test_exposure"], false);
        assert_eq!(output.value["measurement"]["changed_files"], 1);
        assert_eq!(output.value["measurement"]["worker_count"], 1);
        assert_eq!(output.value["measurement"]["dynamic_fanout"], false);
    }

    #[test]
    fn drift_and_hidden_material_copy_fail_closed() {
        let mut drift = fixture(LiveVariant::N7);
        drift.input.request.policy_digest = digest_bytes(b"drifted policy");
        let error = execute_with_runners(
            &drift.input,
            LiveVariant::N7,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::new(),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("policy drift");
        assert_eq!(error.code, "invalid_input");

        let mut effort_drift = fixture(LiveVariant::N7);
        effort_drift.input.request.model_profile.reasoning_effort = "low".into();
        let error = execute_with_runners(
            &effort_drift.input,
            LiveVariant::N7,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::new(),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("reasoning effort drift");
        assert_eq!(error.code, "invalid_input");

        let mut current_ao_drift = fixture(LiveVariant::N0);
        current_ao_drift
            .input
            .current_ao
            .as_mut()
            .expect("current-AO binding")
            .provider_program_digest = digest_bytes(b"drifted current-AO provider");
        let error = execute_with_runners(
            &current_ao_drift.input,
            LiveVariant::N0,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::new(),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("current-AO binding drift");
        assert_eq!(error.code, "invalid_input");

        let mut exposed_authority = fixture(LiveVariant::N7);
        exposed_authority
            .input
            .request
            .authority
            .allowed_roots
            .push(exposed_authority.input.hidden_tests.clone());
        let error = execute_with_runners(
            &exposed_authority.input,
            LiveVariant::N7,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::new(),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("hidden authority exposure");
        assert_eq!(error.code, "invalid_input");
        assert!(error.message.contains("hidden-test root"));

        let leaked = fixture(LiveVariant::N4);
        let hidden_bytes =
            std::fs::read(leaked.input.hidden_tests.join("test_product.py")).expect("hidden bytes");
        let output = execute_with_runners(
            &leaked.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(None)]),
                direct_write: Some(leaked.product.clone()),
                additional_write: Some((
                    leaked.input.request.workspace.root.join("copied-hidden.py"),
                    hidden_bytes,
                )),
            },
            BoundedProcessRunner,
        )
        .expect("leak record");
        assert_ne!(output.status, 0);
        assert_eq!(output.value["measurement"]["hidden_test_exposure"], true);
        assert_eq!(output.value["measurement"]["task_success"], false);
    }

    #[test]
    fn n7_rejects_a_second_provider_process_before_spawn() {
        let n7 = fixture(LiveVariant::N7);
        let turn = AdapterTurn {
            actions: Vec::new(),
            usage: TokenUsage::default(),
            model_claimed_success: false,
            control_mutations: Vec::new(),
        };
        let output = execute_with_runners(
            &n7.input,
            LiveVariant::N7,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(Some(&turn))]),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect("bounded N7 row");
        assert_eq!(output.status, 4);
        assert_eq!(output.value["measurement"]["worker_turns"], 1);
        assert_eq!(output.value["measurement"]["task_success"], false);
    }

    #[test]
    fn malformed_provider_output_is_retained_before_the_run_stops() {
        let malformed = fixture(LiveVariant::N4);
        let error = execute_with_runners(
            &malformed.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([InvocationOutput {
                    status: 0,
                    stdout: b"{malformed-provider-output\n".to_vec(),
                    stderr: b"provider diagnostic".to_vec(),
                }]),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("malformed provider output");
        assert_eq!(error.code, "runtime_failure");
        assert_eq!(
            std::fs::read(malformed.input.raw_capture_root.join("capture-000.stdout"))
                .expect("retained stdout"),
            b"{malformed-provider-output\n"
        );
        assert!(
            malformed
                .input
                .raw_capture_root
                .join("capture-index.json")
                .is_file()
        );
    }
}
