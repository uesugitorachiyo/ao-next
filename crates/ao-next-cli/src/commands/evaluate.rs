use ao_next_core::contracts::Digest;
use ao_next_eval::comparison::{
    ComparisonRequest, evaluate_live_authorized_with_recovery_digest,
    evaluate_offline_with_recovery_digest,
};

use super::{CommandFailure, CommandOutput, EvaluateArgs, decode_file};

pub fn execute(args: &EvaluateArgs) -> Result<CommandOutput, CommandFailure> {
    let request: ComparisonRequest = decode_file(&args.comparison)?;
    let recovery_digest = recovery_digest(args)?;
    let report = evaluate_offline_with_recovery_digest(&request, recovery_digest.as_ref())
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let summary = format!("evaluated sealed corpus: {:?}", report.decision);
    let value = serde_json::to_value(report)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    Ok(CommandOutput::new(value, summary, 0))
}

pub fn execute_live(args: &EvaluateArgs) -> Result<CommandOutput, CommandFailure> {
    if std::env::var("AO_NEXT_LIVE_PROVIDER_CALLS").as_deref() != Ok("operator-authorized") {
        return Err(CommandFailure::authorization(
            "live evaluation requires exact operator authorization",
        ));
    }
    let request: ComparisonRequest = decode_file(&args.comparison)?;
    let recovery_digest = recovery_digest(args)?;
    let report = evaluate_live_authorized_with_recovery_digest(&request, recovery_digest.as_ref())
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let summary = format!("evaluated sealed live corpus: {:?}", report.decision);
    let value = serde_json::to_value(report)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    Ok(CommandOutput::new(value, summary, 0))
}

fn recovery_digest(args: &EvaluateArgs) -> Result<Option<Digest>, CommandFailure> {
    args.recovery_qualification_digest
        .as_deref()
        .map(Digest::new)
        .transpose()
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))
}
