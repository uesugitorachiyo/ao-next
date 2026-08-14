use ao_next_eval::comparison::{ComparisonRequest, evaluate_live_authorized, evaluate_offline};

use super::{CommandFailure, CommandOutput, EvaluateArgs, decode_file};

pub fn execute(args: &EvaluateArgs) -> Result<CommandOutput, CommandFailure> {
    let request: ComparisonRequest = decode_file(&args.comparison)?;
    let report = evaluate_offline(&request)
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
    let report = evaluate_live_authorized(&request)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let summary = format!("evaluated sealed live corpus: {:?}", report.decision);
    let value = serde_json::to_value(report)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    Ok(CommandOutput::new(value, summary, 0))
}
