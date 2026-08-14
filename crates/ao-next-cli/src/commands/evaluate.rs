use ao_next_eval::comparison::{
    ComparisonRequest, RecoveryQualification, evaluate_live_authorized_with_recovery_qualification,
    evaluate_offline_with_recovery_qualification,
};

use super::{CommandFailure, CommandOutput, EvaluateArgs, campaign, decode_file};

pub fn execute(args: &EvaluateArgs) -> Result<CommandOutput, CommandFailure> {
    let request: ComparisonRequest = decode_file(&args.comparison)?;
    let recovery = recovery_qualification(args, &request)?;
    let report = evaluate_offline_with_recovery_qualification(&request, recovery.as_ref())
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
    let recovery = recovery_qualification(args, &request)?;
    let report = evaluate_live_authorized_with_recovery_qualification(&request, recovery.as_ref())
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let summary = format!("evaluated sealed live corpus: {:?}", report.decision);
    let value = serde_json::to_value(report)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    Ok(CommandOutput::new(value, summary, 0))
}

fn recovery_qualification(
    args: &EvaluateArgs,
    request: &ComparisonRequest,
) -> Result<Option<RecoveryQualification>, CommandFailure> {
    args.recovery_evidence_root
        .as_ref()
        .map(|root| campaign::qualify_recovery(&request.corpus, root))
        .transpose()
}
