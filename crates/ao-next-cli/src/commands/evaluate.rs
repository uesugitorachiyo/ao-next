use ao_next_eval::comparison::{ComparisonRequest, evaluate_offline};

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
