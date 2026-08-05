use std::collections::{BTreeMap, BTreeSet};

use ao_next_core::contracts::Digest;
use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::corpus::{CorpusError, CorpusManifest, EvaluationTask};
use crate::metrics::{ExecutionVariant, MetricRow, MetricsError, RunMeasurement, derive_metrics};

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct ComparisonRequest {
    pub schema_version: String,
    pub corpus: CorpusManifest,
    pub runs: Vec<RunMeasurement>,
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
    #[error("task {task_id} is missing variant {variant:?}")]
    MissingVariant {
        task_id: String,
        variant: ExecutionVariant,
    },
    #[error("task {task_id} duplicates variant {variant:?}")]
    DuplicateVariant {
        task_id: String,
        variant: ExecutionVariant,
    },
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
    if request.schema_version != "ao.next.comparison-request.v1" {
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
    for run in &request.runs {
        let Some(task) = tasks.get(run.task_id.as_str()) else {
            return Err(EvaluationError::RunIdentityMismatch {
                task_id: run.task_id.clone(),
                variant: run.variant,
            });
        };
        validate_run_identity(run, task, &request.corpus.corpus_digest)?;
        if !observed.insert((run.task_id.clone(), run.variant)) {
            return Err(EvaluationError::DuplicateVariant {
                task_id: run.task_id.clone(),
                variant: run.variant,
            });
        }
        rows.push(derive_metrics(run).map_err(|error| map_metrics_error(run, error))?);
    }
    for task in &request.corpus.tasks {
        for variant in [
            ExecutionVariant::N0,
            ExecutionVariant::N4,
            ExecutionVariant::N7,
        ] {
            if !observed.contains(&(task.task_id.clone(), variant)) {
                return Err(EvaluationError::MissingVariant {
                    task_id: task.task_id.clone(),
                    variant,
                });
            }
        }
    }

    let summary = summarize(&rows);
    let gates = calculate_gates(&request.corpus, &rows, &summary);
    let decision = if gates.iter().all(|gate| gate.passed) {
        EvaluationDecision::AoNextReadyForLiveEvaluation
    } else {
        EvaluationDecision::AoNextNotYetSuperior
    };
    Ok(ComparisonReport {
        schema_version: "ao.next.comparison-report.v1".into(),
        corpus_digest: request.corpus.corpus_digest.clone(),
        rows,
        summary,
        gates,
        decision,
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
                || run.model_identifier != profile.model_identifier
                || run.prompt_digest != profile.prompt_digest
                || run.policy_digest != profile.policy_digest
                || run.adapter_version != profile.adapter_version
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
    ComparisonSummary {
        n0_median_total_tokens: median(values(rows, ExecutionVariant::N0, |row| row.total_tokens)),
        n4_median_total_tokens: median(values(rows, ExecutionVariant::N4, |row| row.total_tokens)),
        n7_median_total_tokens: median(values(rows, ExecutionVariant::N7, |row| row.total_tokens)),
        n0_median_wall_clock_ms: median(values(rows, ExecutionVariant::N0, |row| {
            row.measurement.wall_clock_ms
        })),
        n4_median_wall_clock_ms: median(values(rows, ExecutionVariant::N4, |row| {
            row.measurement.wall_clock_ms
        })),
        n7_median_wall_clock_ms: median(values(rows, ExecutionVariant::N7, |row| {
            row.measurement.wall_clock_ms
        })),
    }
}

fn values(
    rows: &[MetricRow],
    variant: ExecutionVariant,
    value: impl Fn(&MetricRow) -> u64,
) -> Vec<u64> {
    rows.iter()
        .filter(|row| row.measurement.variant == variant)
        .map(value)
        .collect()
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
) -> Vec<GateResult> {
    let n7 = rows
        .iter()
        .filter(|row| row.measurement.variant == ExecutionVariant::N7)
        .collect::<Vec<_>>();
    let hidden_quality = corpus.tasks.iter().all(|task| {
        let rate = |variant| {
            rows.iter()
                .find(|row| {
                    row.measurement.task_id == task.task_id && row.measurement.variant == variant
                })
                .map_or(0, |row| row.hidden_test_rate_basis_points)
        };
        rate(ExecutionVariant::N7) >= rate(ExecutionVariant::N0).max(rate(ExecutionVariant::N4))
    });
    let recovery = n7.iter().any(|row| row.measurement.recovery_attempted)
        && n7.iter().all(|row| {
            !row.measurement.recovery_attempted || row.measurement.recovery_no_duplicate_effect
        });
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

fn gate(id: &str, passed: bool, detail: &str) -> GateResult {
    GateResult {
        gate_id: id.into(),
        passed,
        detail: detail.into(),
    }
}
