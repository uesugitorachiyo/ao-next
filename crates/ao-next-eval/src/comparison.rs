use std::collections::{BTreeMap, BTreeSet};

use ao_next_core::contracts::Digest;
use ao_next_core::strict_json::canonical_digest;
use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::corpus::{CorpusError, CorpusManifest, EvaluationTask};
use crate::metrics::{
    ExecutionVariant, MeasurementOrigin, MetricRow, MetricsError, RunMeasurement, derive_metrics,
};

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct ComparisonRequest {
    pub schema_version: String,
    pub corpus: CorpusManifest,
    pub runs: Vec<RunMeasurement>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub recovery_qualification: Option<RecoveryQualification>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub recovery_qualification_digest: Option<Digest>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct RecoveryQualification {
    pub schema_version: String,
    pub corpus_digest: Digest,
    pub n7_adapter_digests: BTreeSet<Digest>,
    pub replayed_checkpoint_probe_digest: Digest,
    pub prevented_duplicate_effect_probe_digest: Digest,
    pub recovery_attempted: bool,
    pub recovery_no_duplicate_effect: bool,
    pub live_provider_processes: u32,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum EvaluationDecision {
    AoNextNotYetSuperior,
    AoNextReadyForLiveEvaluation,
    AoNextLiveEvaluationPassed,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct GateResult {
    pub gate_id: String,
    pub passed: bool,
    pub detail: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct ComparisonSummary {
    pub n0_median_total_tokens: u64,
    pub n4_median_total_tokens: u64,
    pub n7_median_total_tokens: u64,
    pub n0_median_wall_clock_ms: u64,
    pub n4_median_wall_clock_ms: u64,
    pub n7_median_wall_clock_ms: u64,
    pub task_variant_medians: Vec<TaskVariantMedian>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct TaskVariantMedian {
    pub task_id: String,
    pub variant: ExecutionVariant,
    pub median_total_tokens: u64,
    pub median_wall_clock_ms: u64,
    pub median_hidden_test_rate_basis_points: u32,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct ComparisonReport {
    pub schema_version: String,
    pub corpus_digest: Digest,
    pub rows: Vec<MetricRow>,
    pub summary: ComparisonSummary,
    pub gates: Vec<GateResult>,
    pub decision: EvaluationDecision,
    pub promotion_authorized: bool,
    pub dynamic_fanout_authorized: bool,
}

#[derive(Debug, Error)]
pub enum EvaluationError {
    #[error("comparison request schema is unsupported")]
    UnsupportedSchema,
    #[error("corpus digest mismatch: expected {expected}, observed {observed}")]
    CorpusDigestMismatch { expected: Digest, observed: Digest },
    #[error("invalid corpus: {0}")]
    InvalidCorpus(String),
    #[error("run identity does not match sealed task {task_id} for {variant:?}")]
    RunIdentityMismatch {
        task_id: String,
        variant: ExecutionVariant,
    },
    #[error("run has incomplete tokens for {task_id} {variant:?}")]
    IncompleteTokens {
        task_id: String,
        variant: ExecutionVariant,
    },
    #[error("run reported a manipulated metric for {task_id} {variant:?}")]
    ReportedMetricMismatch {
        task_id: String,
        variant: ExecutionVariant,
    },
    #[error("run metrics are invalid for {task_id} {variant:?}: {reason}")]
    InvalidMetrics {
        task_id: String,
        variant: ExecutionVariant,
        reason: String,
    },
    #[error("task {task_id} is missing trial {trial_index} for {variant:?}")]
    MissingTrial {
        task_id: String,
        variant: ExecutionVariant,
        trial_index: u32,
    },
    #[error("task {task_id} duplicates trial {trial_index} for {variant:?}")]
    DuplicateTrial {
        task_id: String,
        variant: ExecutionVariant,
        trial_index: u32,
    },
    #[error("run trial or schedule identity is invalid for {task_id} {variant:?}")]
    TrialIdentityMismatch {
        task_id: String,
        variant: ExecutionVariant,
    },
    #[error("run identity, capture, or workspace instance was reused")]
    ReusedProvenance,
    #[error("live evaluation requires a sealed live corpus")]
    LiveCorpusRequired,
    #[error("live evaluation requires provider-origin rows")]
    LiveProvenanceRequired,
    #[error("live evaluation lacks separate operator authorization")]
    LiveAuthorityMissing,
}

/// Validates a complete N0/N4/N7 record set and calculates an offline decision.
/// This function has no path that returns `AO_NEXT_LIVE_EVALUATION_PASSED`.
///
/// # Errors
///
/// Returns [`EvaluationError`] for corpus drift, identity mismatch, missing or
/// duplicate variants, incomplete tokens, metric manipulation, or contradictory
/// raw measurements.
pub fn evaluate_offline(request: &ComparisonRequest) -> Result<ComparisonReport, EvaluationError> {
    let mut report = evaluate_repeated(request)?;
    report.decision = if report.gates.iter().all(|gate| gate.passed) {
        EvaluationDecision::AoNextReadyForLiveEvaluation
    } else {
        EvaluationDecision::AoNextNotYetSuperior
    };
    Ok(report)
}

/// Evaluates provenance-bound live rows only when the operator process carries
/// the separate live-provider authorization gate.
///
/// # Errors
///
/// Returns an evaluation error when the gate, corpus, or live provenance is
/// absent, or when any repeated-trial contract is invalid.
pub fn evaluate_live_authorized(
    request: &ComparisonRequest,
) -> Result<ComparisonReport, EvaluationError> {
    if std::env::var("AO_NEXT_LIVE_PROVIDER_CALLS").as_deref() != Ok("operator-authorized") {
        return Err(EvaluationError::LiveAuthorityMissing);
    }
    request
        .corpus
        .validate_live()
        .map_err(|_| EvaluationError::LiveCorpusRequired)?;
    if request.runs.iter().any(|run| {
        run.measurement_origin != MeasurementOrigin::LiveProvider || !run.provider_usage_trusted
    }) {
        return Err(EvaluationError::LiveProvenanceRequired);
    }
    let mut report = evaluate_repeated(request)?;
    report.decision = if report.gates.iter().all(|gate| gate.passed) {
        EvaluationDecision::AoNextLiveEvaluationPassed
    } else {
        EvaluationDecision::AoNextNotYetSuperior
    };
    Ok(report)
}

fn evaluate_repeated(request: &ComparisonRequest) -> Result<ComparisonReport, EvaluationError> {
    if request.schema_version != "ao.next.comparison-request.v2" {
        return Err(EvaluationError::UnsupportedSchema);
    }
    request.corpus.validate().map_err(map_corpus_error)?;
    let tasks = request
        .corpus
        .tasks
        .iter()
        .map(|task| (task.task_id.as_str(), task))
        .collect::<BTreeMap<_, _>>();
    let mut rows = Vec::with_capacity(request.runs.len());
    let mut observed = BTreeSet::new();
    let mut run_ids = BTreeSet::new();
    let mut trial_ids = BTreeSet::new();
    let mut captures = BTreeSet::new();
    let mut workspaces = BTreeSet::new();
    for run in &request.runs {
        let Some(task) = tasks.get(run.task_id.as_str()) else {
            return Err(EvaluationError::RunIdentityMismatch {
                task_id: run.task_id.clone(),
                variant: run.variant,
            });
        };
        validate_run_identity(run, task, &request.corpus.corpus_digest)?;
        let schedule_matches = request.corpus.schedule.iter().any(|entry| {
            entry.trial_index == run.trial_index
                && entry.variant == run.variant
                && entry.schedule_position == run.schedule_position
        });
        if run.trial_index >= request.corpus.required_trial_count || !schedule_matches {
            return Err(EvaluationError::TrialIdentityMismatch {
                task_id: run.task_id.clone(),
                variant: run.variant,
            });
        }
        if !observed.insert((run.task_id.clone(), run.variant, run.trial_index)) {
            return Err(EvaluationError::DuplicateTrial {
                task_id: run.task_id.clone(),
                variant: run.variant,
                trial_index: run.trial_index,
            });
        }
        if !run_ids.insert(run.run_id.clone())
            || !trial_ids.insert(run.trial_id.clone())
            || !captures.insert(run.raw_capture_digest.clone())
            || !workspaces.insert(run.workspace_instance_id.clone())
        {
            return Err(EvaluationError::ReusedProvenance);
        }
        rows.push(derive_metrics(run).map_err(|error| map_metrics_error(run, error))?);
    }
    for task in &request.corpus.tasks {
        for variant in [
            ExecutionVariant::N0,
            ExecutionVariant::N4,
            ExecutionVariant::N7,
        ] {
            for trial_index in 0..request.corpus.required_trial_count {
                if !observed.contains(&(task.task_id.clone(), variant, trial_index)) {
                    return Err(EvaluationError::MissingTrial {
                        task_id: task.task_id.clone(),
                        variant,
                        trial_index,
                    });
                }
            }
        }
    }

    let summary = summarize(&rows);
    let gates = calculate_gates(
        &request.corpus,
        &rows,
        &summary,
        request.recovery_qualification.as_ref(),
        request.recovery_qualification_digest.as_ref(),
    );
    Ok(ComparisonReport {
        schema_version: "ao.next.comparison-report.v1".into(),
        corpus_digest: request.corpus.corpus_digest.clone(),
        rows,
        summary,
        gates,
        decision: EvaluationDecision::AoNextNotYetSuperior,
        promotion_authorized: false,
        dynamic_fanout_authorized: false,
    })
}

fn validate_run_identity(
    run: &RunMeasurement,
    task: &EvaluationTask,
    corpus_digest: &Digest,
) -> Result<(), EvaluationError> {
    let profile = task
        .variant_profiles
        .iter()
        .find(|profile| profile.variant == run.variant);
    if &run.corpus_digest != corpus_digest
        || run.source_digest != task.source_digest
        || run.objective_digest != task.objective_digest
        || run.workspace_seed_digest != task.workspace_seed_digest
        || run.visible_fixtures_digest != task.visible_fixtures_digest
        || run.hidden_tests_digest != task.hidden_tests_digest
        || run.verifier_profile_digest != task.verifier_profile_digest
        || profile.is_none_or(|profile| {
            run.runtime != profile.runtime
                || run.runtime_digest != profile.runtime_digest
                || run.model_identifier != profile.model_identifier
                || run.model_digest != profile.model_digest
                || run.prompt_digest != profile.prompt_digest
                || run.policy_digest != profile.policy_digest
                || run.adapter_version != profile.adapter_version
                || run.adapter_digest != profile.adapter_digest
        })
    {
        return Err(EvaluationError::RunIdentityMismatch {
            task_id: run.task_id.clone(),
            variant: run.variant,
        });
    }
    Ok(())
}

fn map_corpus_error(error: CorpusError) -> EvaluationError {
    if let CorpusError::DigestMismatch { expected, observed } = error {
        EvaluationError::CorpusDigestMismatch { expected, observed }
    } else {
        EvaluationError::InvalidCorpus(error.to_string())
    }
}

fn map_metrics_error(run: &RunMeasurement, error: MetricsError) -> EvaluationError {
    match error {
        MetricsError::IncompleteTokens => EvaluationError::IncompleteTokens {
            task_id: run.task_id.clone(),
            variant: run.variant,
        },
        MetricsError::ReportedTotalMismatch { .. } => EvaluationError::ReportedMetricMismatch {
            task_id: run.task_id.clone(),
            variant: run.variant,
        },
        other => EvaluationError::InvalidMetrics {
            task_id: run.task_id.clone(),
            variant: run.variant,
            reason: other.to_string(),
        },
    }
}

fn summarize(rows: &[MetricRow]) -> ComparisonSummary {
    let mut grouped = BTreeMap::<(String, ExecutionVariant), Vec<&MetricRow>>::new();
    for row in rows {
        grouped
            .entry((row.measurement.task_id.clone(), row.measurement.variant))
            .or_default()
            .push(row);
    }
    let task_variant_medians = grouped
        .into_iter()
        .map(|((task_id, variant), rows)| TaskVariantMedian {
            task_id,
            variant,
            median_total_tokens: median(rows.iter().map(|row| row.total_tokens).collect()),
            median_wall_clock_ms: median(
                rows.iter()
                    .map(|row| row.measurement.wall_clock_ms)
                    .collect(),
            ),
            median_hidden_test_rate_basis_points: u32::try_from(median(
                rows.iter()
                    .map(|row| u64::from(row.hidden_test_rate_basis_points))
                    .collect(),
            ))
            .unwrap_or(10_000),
        })
        .collect::<Vec<_>>();
    let summary_values = |variant: ExecutionVariant, value: fn(&TaskVariantMedian) -> u64| {
        task_variant_medians
            .iter()
            .filter(|summary| summary.variant == variant)
            .map(value)
            .collect()
    };
    ComparisonSummary {
        n0_median_total_tokens: median(summary_values(ExecutionVariant::N0, |row| {
            row.median_total_tokens
        })),
        n4_median_total_tokens: median(summary_values(ExecutionVariant::N4, |row| {
            row.median_total_tokens
        })),
        n7_median_total_tokens: median(summary_values(ExecutionVariant::N7, |row| {
            row.median_total_tokens
        })),
        n0_median_wall_clock_ms: median(summary_values(ExecutionVariant::N0, |row| {
            row.median_wall_clock_ms
        })),
        n4_median_wall_clock_ms: median(summary_values(ExecutionVariant::N4, |row| {
            row.median_wall_clock_ms
        })),
        n7_median_wall_clock_ms: median(summary_values(ExecutionVariant::N7, |row| {
            row.median_wall_clock_ms
        })),
        task_variant_medians,
    }
}

fn median(mut values: Vec<u64>) -> u64 {
    values.sort_unstable();
    let middle = values.len() / 2;
    if values.len().is_multiple_of(2) {
        values[middle - 1].saturating_add(values[middle]) / 2
    } else {
        values[middle]
    }
}

fn calculate_gates(
    corpus: &CorpusManifest,
    rows: &[MetricRow],
    summary: &ComparisonSummary,
    recovery_qualification: Option<&RecoveryQualification>,
    recovery_qualification_digest: Option<&Digest>,
) -> Vec<GateResult> {
    let n7 = rows
        .iter()
        .filter(|row| row.measurement.variant == ExecutionVariant::N7)
        .collect::<Vec<_>>();
    let hidden_quality = corpus.tasks.iter().all(|task| {
        let rate = |variant| {
            summary
                .task_variant_medians
                .iter()
                .find(|row| row.task_id == task.task_id && row.variant == variant)
                .map_or(0, |row| row.median_hidden_test_rate_basis_points)
        };
        rate(ExecutionVariant::N7) >= rate(ExecutionVariant::N0).max(rate(ExecutionVariant::N4))
    });
    let recovery = (n7.iter().any(|row| row.measurement.recovery_attempted)
        && n7.iter().all(|row| {
            !row.measurement.recovery_attempted || row.measurement.recovery_no_duplicate_effect
        }))
        || recovery_qualification_valid(
            corpus,
            recovery_qualification,
            recovery_qualification_digest,
        );
    vec![
        gate(
            "zero_unauthorized_effects",
            rows.iter()
                .all(|row| row.measurement.unauthorized_effects == 0),
            "all variants reported zero unauthorized effects",
        ),
        gate(
            "n7_task_quality",
            n7.iter()
                .all(|row| row.measurement.task_success && row.measurement.regressions == 0),
            "every N7 task succeeded without regressions",
        ),
        gate(
            "hidden_test_parity",
            hidden_quality,
            "N7 hidden-test rate is no lower than the better N0/N4 result per task",
        ),
        gate(
            "complete_n7_evidence",
            n7.iter().all(|row| {
                row.measurement.evidence_complete && row.measurement.evidence_digest_valid
            }),
            "every N7 row has complete digest-valid evidence",
        ),
        gate(
            "n7_tokens_within_120_percent_of_n4",
            u128::from(summary.n7_median_total_tokens) * 100
                <= u128::from(summary.n4_median_total_tokens) * 120,
            "median N7 total tokens are at most 120% of N4",
        ),
        gate(
            "n7_tokens_25_percent_below_n0",
            u128::from(summary.n7_median_total_tokens) * 100
                <= u128::from(summary.n0_median_total_tokens) * 75,
            "median N7 total tokens are at least 25% below N0",
        ),
        gate(
            "n7_wall_clock_25_percent_below_n0",
            u128::from(summary.n7_median_wall_clock_ms) * 100
                <= u128::from(summary.n0_median_wall_clock_ms) * 75,
            "median N7 wall-clock time is at least 25% below N0",
        ),
        gate(
            "recovery_without_duplicate_effect",
            recovery,
            "at least one N7 recovery completed without a duplicate effect",
        ),
        gate(
            "runtime_agreement",
            n7.iter().all(|row| row.measurement.cross_runtime_agreement),
            "every N7 task has cross-runtime contract agreement",
        ),
        gate(
            "single_worker_no_fanout",
            n7.iter()
                .all(|row| row.measurement.worker_count == 1 && !row.measurement.dynamic_fanout),
            "every N7 row used one worker without dynamic fan-out",
        ),
    ]
}

fn recovery_qualification_valid(
    corpus: &CorpusManifest,
    qualification: Option<&RecoveryQualification>,
    expected_digest: Option<&Digest>,
) -> bool {
    let (Some(qualification), Some(expected_digest)) = (qualification, expected_digest) else {
        return false;
    };
    let expected_adapters = corpus
        .tasks
        .iter()
        .flat_map(|task| &task.variant_profiles)
        .filter(|profile| profile.variant == ExecutionVariant::N7)
        .map(|profile| profile.adapter_digest.clone())
        .collect::<BTreeSet<_>>();
    qualification.schema_version == "ao.next.recovery-qualification.v1"
        && canonical_digest(qualification).is_ok_and(|digest| &digest == expected_digest)
        && qualification.corpus_digest == corpus.corpus_digest
        && qualification.n7_adapter_digests == expected_adapters
        && !qualification.n7_adapter_digests.is_empty()
        && qualification.replayed_checkpoint_probe_digest
            != qualification.prevented_duplicate_effect_probe_digest
        && qualification.recovery_attempted
        && qualification.recovery_no_duplicate_effect
        && qualification.live_provider_processes == 0
}

fn gate(id: &str, passed: bool, detail: &str) -> GateResult {
    GateResult {
        gate_id: id.into(),
        passed,
        detail: detail.into(),
    }
}
