use std::collections::{BTreeMap, BTreeSet};
use std::path::{Component, Path, PathBuf};
use std::time::Instant;

use ao_next_core::adapter::codex;
use ao_next_core::adapter::process::{
    BoundedProcessRunner, ProcessAdapterConfig, ProcessRunner, ProcessRuntimeAdapter,
    RuntimeCapture, capture_runtime_output,
};
use ao_next_core::adapter::{CancellationToken, InvocationLimits};
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

use super::{CommandFailure, CommandOutput, LiveRunArgs, decode_file, read_bounded_regular};

const WORKER_ID: &str = "ao-next-live-worker-01";

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "UPPERCASE")]
pub enum LiveVariant {
    N4,
    N7,
}

impl LiveVariant {
    const fn execution_variant(self) -> ExecutionVariant {
        match self {
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
    request: RunRequest,
    command_verifier: CommandVerifierProfile,
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
    verifier_report_digest: Option<Digest>,
    record_digest: Digest,
}

struct ValidatedInput<'a> {
    task: &'a EvaluationTask,
    profile: &'a VariantProfile,
    initial_files: Vec<SnapshotEntry>,
    hidden_file_digests: BTreeSet<Digest>,
}

pub fn execute(args: &LiveRunArgs, variant: LiveVariant) -> Result<CommandOutput, CommandFailure> {
    if std::env::var("AO_NEXT_LIVE_PROVIDER_CALLS").as_deref() != Ok("operator-authorized") {
        return Err(CommandFailure::authorization(
            "live provider calls require separate operator authorization",
        ));
    }
    let input: LiveRunInput = decode_file(&args.input)?;
    execute_with_runners(&input, variant, BoundedProcessRunner, BoundedProcessRunner)
}

#[allow(
    clippy::too_many_lines,
    reason = "the run record is assembled once so every measured field remains visibly source-bound"
)]
fn execute_with_runners<P: ProcessRunner, V: ProcessRunner>(
    input: &LiveRunInput,
    variant: LiveVariant,
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

    let (terminal_state, outcome, captures) = match variant {
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
        measurement_origin: MeasurementOrigin::LiveProvider,
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
        repair_attempts: outcome
            .as_ref()
            .map_or(0, |value| value.metrics.repair_attempts),
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
        recovery_attempted: false,
        recovery_no_duplicate_effect: false,
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
        &verifier_report_digest,
    ))
    .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let record = LiveRunRecord {
        schema_version: "ao.next.live-run-record.v1",
        variant,
        terminal_state: terminal_state.clone(),
        measurement,
        capture_digests,
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

fn execute_n7<P: ProcessRunner, V: ProcessRunner>(
    input: &LiveRunInput,
    provider_runner: P,
    verifier: &mut CommandEngineVerifier<V>,
    cancellation: CancellationToken,
    limits: InvocationLimits,
) -> Result<(RunState, Option<RunOutcome>, Vec<RuntimeCapture>), CommandFailure> {
    let config = ProcessAdapterConfig::from_request(
        &input.request,
        WORKER_ID,
        &input.output_schema,
        limits,
        cancellation,
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let mut adapter = ProcessRuntimeAdapter::new(config, provider_runner);
    let broker = LocalEffectBroker::new(
        input.request.limits.max_effect_timeout_ms,
        usize::try_from(input.request.limits.max_output_bytes).unwrap_or(usize::MAX),
    );
    let outcome = DirectEngine::new(&broker).run(&input.request, &mut adapter, verifier);
    let terminal_state = outcome.terminal_state.clone();
    Ok((terminal_state, Some(outcome), adapter.captures().to_vec()))
}

fn execute_n4<P: ProcessRunner, V: ProcessRunner>(
    input: &LiveRunInput,
    mut provider_runner: P,
    verifier: &mut CommandEngineVerifier<V>,
    cancellation: &CancellationToken,
    limits: InvocationLimits,
) -> Result<(RunState, Option<RunOutcome>, Vec<RuntimeCapture>), CommandFailure> {
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
    let capture = capture_runtime_output("codex", &output, limits.max_output_bytes)
        .map_err(|error| CommandFailure::runtime(error.to_string()))?;
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
        LiveVariant::N4 => provider_runtime == "codex" && profile_runtime == "codex",
        LiveVariant::N7 => {
            matches!(provider_runtime, "codex" | "claude")
                && (profile_runtime == provider_runtime
                    || profile_runtime == format!("ao-next-{provider_runtime}"))
        }
    }
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
            if let Some(path) = self.direct_write.take() {
                assert!(
                    invocation
                        .args
                        .windows(2)
                        .any(|args| args == ["--sandbox", "workspace-write"])
                );
                std::fs::write(path, b"ready\n").expect("direct fixture write");
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
        _root: TempDir,
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
        std::fs::create_dir_all(&workspace).expect("workspace");
        std::fs::create_dir_all(&visible).expect("visible");
        std::fs::create_dir_all(&hidden).expect("hidden");
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
        let profiles = [
            profile(ExecutionVariant::N0, "current-ao", "current-ao-native-v1"),
            profile(ExecutionVariant::N4, "codex", "native-codex-direct-v1"),
            profile(ExecutionVariant::N7, "ao-next-codex", "ao-next-process-v1"),
        ];
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
                request,
                command_verifier,
            },
            _root: root,
        }
    }

    fn profile(variant: ExecutionVariant, runtime: &str, adapter_version: &str) -> VariantProfile {
        VariantProfile {
            variant,
            runtime: runtime.into(),
            runtime_digest: digest_bytes(format!("{runtime}:runtime").as_bytes()),
            model_identifier: "fixed-live-model".into(),
            model_digest: digest_bytes(format!("{runtime}:model").as_bytes()),
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

    #[test]
    fn offline_fake_n4_and_n7_execute_without_the_live_environment_gate() {
        assert_ne!(
            std::env::var("AO_NEXT_LIVE_PROVIDER_CALLS").as_deref(),
            Ok("operator-authorized")
        );

        let n4 = fixture(LiveVariant::N4);
        let output = execute_with_runners(
            &n4.input,
            LiveVariant::N4,
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
            FakeProvider {
                outputs: VecDeque::new(),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("policy drift");
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
}
