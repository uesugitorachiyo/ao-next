use ao_next_core::contracts::Digest;
use serde::{Deserialize, Serialize};
use thiserror::Error;

#[derive(Clone, Copy, Debug, Deserialize, Eq, Ord, PartialEq, PartialOrd, Serialize)]
#[serde(rename_all = "UPPERCASE")]
pub enum ExecutionVariant {
    N0,
    N4,
    N7,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct TokenRow {
    pub input_tokens: Option<u64>,
    pub cached_input_tokens: Option<u64>,
    pub reasoning_tokens: Option<u64>,
    pub output_tokens: Option<u64>,
    pub reported_total_tokens: u64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
#[allow(
    clippy::struct_excessive_bools,
    reason = "independent evidence observations must remain separately contradictory and auditable"
)]
pub struct RunMeasurement {
    pub schema_version: String,
    pub corpus_digest: Digest,
    pub task_id: String,
    pub variant: ExecutionVariant,
    pub source_digest: Digest,
    pub objective_digest: Digest,
    pub workspace_seed_digest: Digest,
    pub visible_fixtures_digest: Digest,
    pub hidden_tests_digest: Digest,
    pub verifier_profile_digest: Digest,
    pub runtime: String,
    pub model_identifier: String,
    pub prompt_digest: Digest,
    pub policy_digest: Digest,
    pub adapter_version: String,
    pub tokens: TokenRow,
    pub wall_clock_ms: u64,
    pub model_wait_ms: u64,
    pub worker_turns: u32,
    pub repair_attempts: u32,
    pub operator_interventions: u32,
    pub changed_files: u32,
    pub accepted_changed_files: u32,
    pub task_success: bool,
    pub hidden_tests_passed: u32,
    pub hidden_tests_total: u32,
    pub regressions: u32,
    pub unauthorized_effects: u32,
    pub evidence_complete: bool,
    pub evidence_digest_valid: bool,
    pub recovery_attempted: bool,
    pub recovery_no_duplicate_effect: bool,
    pub cross_runtime_agreement: bool,
    pub worker_count: u32,
    pub dynamic_fanout: bool,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct MetricRow {
    pub measurement: RunMeasurement,
    pub total_tokens: u64,
    pub hidden_test_rate_basis_points: u32,
    pub changed_file_precision_basis_points: u32,
}

#[derive(Debug, Error)]
pub enum MetricsError {
    #[error("run measurement schema is unsupported")]
    UnsupportedSchema,
    #[error("run measurement has incomplete token fields")]
    IncompleteTokens,
    #[error("run measurement token sum overflowed")]
    TokenOverflow,
    #[error("reported token total {reported} differs from calculated {calculated}")]
    ReportedTotalMismatch { reported: u64, calculated: u64 },
    #[error("run measurement timing is contradictory")]
    TimingContradiction,
    #[error("run measurement hidden tests are contradictory")]
    HiddenTestContradiction,
    #[error("run measurement changed-file counts are contradictory")]
    ChangedFileContradiction,
    #[error("run measurement identity fields are empty")]
    EmptyIdentity,
    #[error("run measurement worker count is zero")]
    ZeroWorkers,
}

/// Calculates all derived metrics from raw counters and rejects supplied-total
/// manipulation or contradictory rows.
///
/// # Errors
///
/// Returns [`MetricsError`] for missing tokens, overflow, reported-total drift,
/// impossible timing/quality counters, empty identities, or zero workers.
pub fn derive_metrics(measurement: &RunMeasurement) -> Result<MetricRow, MetricsError> {
    if measurement.schema_version != "ao.next.run-measurement.v1" {
        return Err(MetricsError::UnsupportedSchema);
    }
    if measurement.task_id.trim().is_empty()
        || measurement.runtime.trim().is_empty()
        || measurement.model_identifier.trim().is_empty()
        || measurement.adapter_version.trim().is_empty()
    {
        return Err(MetricsError::EmptyIdentity);
    }
    if measurement.worker_count == 0 {
        return Err(MetricsError::ZeroWorkers);
    }
    let tokens = [
        measurement.tokens.input_tokens,
        measurement.tokens.cached_input_tokens,
        measurement.tokens.reasoning_tokens,
        measurement.tokens.output_tokens,
    ];
    let mut total_tokens = 0_u64;
    for value in tokens {
        total_tokens = total_tokens
            .checked_add(value.ok_or(MetricsError::IncompleteTokens)?)
            .ok_or(MetricsError::TokenOverflow)?;
    }
    if total_tokens != measurement.tokens.reported_total_tokens {
        return Err(MetricsError::ReportedTotalMismatch {
            reported: measurement.tokens.reported_total_tokens,
            calculated: total_tokens,
        });
    }
    if measurement.model_wait_ms > measurement.wall_clock_ms {
        return Err(MetricsError::TimingContradiction);
    }
    if measurement.hidden_tests_total == 0
        || measurement.hidden_tests_passed > measurement.hidden_tests_total
    {
        return Err(MetricsError::HiddenTestContradiction);
    }
    if measurement.accepted_changed_files > measurement.changed_files {
        return Err(MetricsError::ChangedFileContradiction);
    }
    let hidden_test_rate_basis_points = u32::try_from(
        u64::from(measurement.hidden_tests_passed) * 10_000
            / u64::from(measurement.hidden_tests_total),
    )
    .unwrap_or(10_000);
    let changed_file_precision_basis_points = if measurement.changed_files == 0 {
        10_000
    } else {
        u32::try_from(
            u64::from(measurement.accepted_changed_files) * 10_000
                / u64::from(measurement.changed_files),
        )
        .unwrap_or(10_000)
    };
    Ok(MetricRow {
        measurement: measurement.clone(),
        total_tokens,
        hidden_test_rate_basis_points,
        changed_file_precision_basis_points,
    })
}
