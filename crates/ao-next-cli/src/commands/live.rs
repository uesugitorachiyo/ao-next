use std::collections::{BTreeMap, BTreeSet};
use std::io::Write as _;
use std::path::{Component, Path, PathBuf};
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
    record_digest: Digest,
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
);

struct SingleProviderProcess<R> {
    runner: R,
    started: bool,
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

    let (terminal_state, outcome, captures, raw_outputs) = match variant {
        LiveVariant::N0 => execute_n0(
            input,
            provider_runner,
            &mut verifier,
            &cancellation,
            invocation_limits,
        )?,
        LiveVariant::N7 => execute_n7(
            input,
            provider_runner,
            &mut verifier,
            cancellation.clone(),
            invocation_limits,
        )?,
        LiveVariant::N4 => execute_n4(
            input,
            provider_runner,
            &mut verifier,
            &cancellation,
            invocation_limits,
        )?,
    };
    let raw_capture_index_digest =
        persist_raw_captures(&input.raw_capture_root, &captures, &raw_outputs)?;
    if captures.len() != raw_outputs.len() {
        return Err(CommandFailure::runtime(
            "provider output was retained but could not be normalized",
        ));
    }
    let wall_clock_ms = u64::try_from(started.elapsed().as_millis())
        .unwrap_or(u64::MAX)
        .max(1);
    let final_files = snapshot_tree(
        &input.request.workspace.root,
        input.request.limits.max_input_bytes,
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
        return Ok((RunState::Failed, None, Vec::new(), vec![ao2_output]));
    };
    let provider_output = InvocationOutput {
        status: run.exit_code,
        stdout: run.stdout,
        stderr: run.stderr,
    };
    let Ok(capture) = capture_runtime_output("codex", &provider_output, limits.max_output_bytes)
    else {
        return Ok((RunState::Failed, None, Vec::new(), vec![provider_output]));
    };

    let preview = run_current_ao_control(
        &mut process_runner,
        binding,
        &input.request.workspace.root,
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
        .get("action_digest")
        .and_then(serde_json::Value::as_str)
        .filter(|value| !value.trim().is_empty())
        .ok_or_else(|| CommandFailure::runtime("current-AO patch digest is missing"))?;
    let applied = run_current_ao_control(
        &mut process_runner,
        binding,
        &input.request.workspace.root,
        &[
            "adapter",
            "patch",
            "apply",
            "--target",
            &input.request.workspace.root.display().to_string(),
            "--sandbox",
            &run.sandbox_path.display().to_string(),
            "--digest",
            digest,
            "--approver",
            "ao-next:bounded-live-evaluation",
        ],
        cancellation,
        limits,
    )?;
    if applied
        .get("action_digest")
        .and_then(serde_json::Value::as_str)
        != Some(digest)
    {
        return Err(CommandFailure::runtime(
            "current-AO patch application digest drifted",
        ));
    }
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

fn run_current_ao_control<P: ProcessRunner>(
    runner: &mut P,
    binding: &CurrentAoBinding,
    workspace: &Path,
    args: &[&str],
    cancellation: &CancellationToken,
    limits: InvocationLimits,
) -> Result<serde_json::Value, CommandFailure> {
    let output = runner
        .run(
            &PreparedInvocation {
                program: binding.ao2_program.display().to_string(),
                args: args.iter().map(|value| (*value).to_string()).collect(),
                stdin: Vec::new(),
                cwd: workspace.to_path_buf(),
                limits,
            },
            cancellation,
        )
        .map_err(|error| CommandFailure::runtime(error.to_string()))?;
    if output.status != 0 {
        return Err(CommandFailure::runtime(format!(
            "current-AO control command exited {}",
            output.status
        )));
    }
    decode_strict_json(&output.stdout, limits.max_output_bytes)
        .map_err(|error| CommandFailure::runtime(error.to_string()))
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
        return Ok((RunState::Failed, None, Vec::new(), vec![output]));
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

#[derive(Serialize)]
struct RawCaptureIndex {
    schema_version: &'static str,
    entries: Vec<RawCaptureIndexEntry>,
}

#[derive(Serialize)]
struct RawCaptureIndexEntry {
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
    captures: &[RuntimeCapture],
    outputs: &[InvocationOutput],
) -> Result<Digest, CommandFailure> {
    if outputs.is_empty() {
        return Err(CommandFailure::evidence(
            "raw provider capture coverage is incomplete",
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
        schema_version: "ao.next.raw-provider-capture-index.v1",
        entries,
    };
    let digest =
        canonical_digest(&index).map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let bytes =
        serde_json::to_vec(&index).map_err(|error| CommandFailure::evidence(error.to_string()))?;
    write_private_new(&root.join("capture-index.json"), &bytes)?;
    Ok(digest)
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
    file.write_all(bytes)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    file.sync_all()
        .map_err(|error| CommandFailure::evidence(error.to_string()))
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

fn snapshot_tree(root: &Path, maximum_bytes: u64) -> Result<Vec<SnapshotEntry>, CommandFailure> {
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
