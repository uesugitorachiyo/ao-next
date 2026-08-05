use std::collections::VecDeque;
use std::path::PathBuf;

use ao_next_core::adapter::scripted::ScriptedAdapter;
use ao_next_core::adapter::{AdapterIdentity, AdapterTurn};
use ao_next_core::contracts::{Digest, RunRequest, RunState, VerifierReport};
use ao_next_core::effects::LocalEffectBroker;
use ao_next_core::engine::{DirectEngine, EngineVerifier, VerificationOutcome};
use ao_next_core::evidence::{
    ArtifactSpec, ArtifactStore, StoreLimits, digest_bytes, seal_verified_run,
};
use ao_next_core::strict_json::canonical_digest;
use chrono::{DateTime, Utc};
use serde::Deserialize;

use super::{CommandFailure, CommandOutput, RunArgs, decode_file};

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct ScriptedRunPlan {
    schema_version: String,
    adapter_identity: AdapterIdentity,
    turns: Vec<AdapterTurn>,
    verifier_reports: Vec<VerifierReport>,
    artifacts: Vec<ArtifactInput>,
    completed_at: DateTime<Utc>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct ArtifactInput {
    artifact_id: String,
    path: PathBuf,
    original_ref: String,
    media_type: String,
    producer: String,
    input_digests: Vec<Digest>,
}

struct ReportSequence {
    pending: VecDeque<VerifierReport>,
    last: Option<VerifierReport>,
}

impl ReportSequence {
    fn new(reports: Vec<VerifierReport>) -> Self {
        Self {
            pending: reports.into(),
            last: None,
        }
    }
}

impl EngineVerifier for ReportSequence {
    fn verify(&mut self, _: &RunRequest) -> VerificationOutcome {
        let Some(report) = self.pending.pop_front() else {
            return VerificationOutcome {
                passed: false,
                report_digest: digest_bytes(b"missing scripted verifier report"),
                summary: "script omitted a verifier report".into(),
            };
        };
        let report_digest = canonical_digest(&report)
            .unwrap_or_else(|_| digest_bytes(b"invalid scripted verifier report"));
        let outcome = VerificationOutcome {
            passed: report.passed,
            report_digest,
            summary: if report.passed {
                "scripted verifier passed".into()
            } else {
                "scripted verifier failed".into()
            },
        };
        self.last = Some(report);
        outcome
    }
}

pub fn execute(args: &RunArgs) -> Result<CommandOutput, CommandFailure> {
    let request: RunRequest = decode_file(&args.request)?;
    let plan: ScriptedRunPlan = decode_file(&args.script)?;
    if plan.schema_version != "ao.next.scripted-run-plan.v1" {
        return Err(CommandFailure::invalid_input(
            "unsupported scripted run plan schema",
        ));
    }
    let now = DateTime::parse_from_rfc3339(&args.now)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
        .with_timezone(&Utc);
    ao_next_core::contracts::validate_intake(
        &request,
        &ao_next_core::contracts::IntakeExpectation {
            run_id: request.run_id.clone(),
            source: request.source.clone(),
            workspace: request.workspace.clone(),
            now,
        },
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;

    let output_limit = usize::try_from(request.limits.max_output_bytes).unwrap_or(usize::MAX);
    let broker = LocalEffectBroker::new(request.limits.max_effect_timeout_ms, output_limit);
    let mut adapter = ScriptedAdapter::new(
        plan.adapter_identity.clone(),
        plan.turns.into_iter().map(Ok),
    );
    let mut verifier = ReportSequence::new(plan.verifier_reports);
    let outcome = DirectEngine::new(&broker).run(&request, &mut adapter, &mut verifier);

    if outcome.terminal_state == RunState::Passed {
        let report = verifier.last.as_ref().ok_or_else(|| {
            CommandFailure::evidence("passed run has no retained verifier report")
        })?;
        let store = ArtifactStore::new(
            &args.evidence,
            request.authority.allowed_roots.clone(),
            StoreLimits {
                max_artifact_bytes: request.limits.max_input_bytes,
                max_total_bytes: request.limits.max_input_bytes.saturating_mul(
                    u64::try_from(plan.artifacts.len())
                        .unwrap_or(u64::MAX)
                        .max(1),
                ),
            },
        )
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
        let artifacts = plan
            .artifacts
            .into_iter()
            .map(|input| ArtifactSpec {
                artifact_id: input.artifact_id,
                path: input.path,
                original_ref: input.original_ref,
                media_type: input.media_type,
                producer: input.producer,
                input_digests: input.input_digests,
            })
            .collect::<Vec<_>>();
        let sealed = seal_verified_run(
            &request,
            &plan.adapter_identity,
            report,
            &store,
            &artifacts,
            plan.completed_at,
        )
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
        let value = serde_json::to_value(&sealed.readback)
            .map_err(|error| CommandFailure::evidence(error.to_string()))?;
        return Ok(CommandOutput::new(
            value,
            format!("sealed passed run {}", request.run_id),
            0,
        ));
    }

    let status = match outcome.terminal_state {
        RunState::Denied => 4,
        RunState::Interrupted => 6,
        _ => 5,
    };
    let terminal = format!("run {} ended {:?}", request.run_id, outcome.terminal_state);
    let value = serde_json::to_value(outcome)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    Ok(CommandOutput::new(value, terminal, status))
}
