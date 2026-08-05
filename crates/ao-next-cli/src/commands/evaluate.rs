use super::{CommandFailure, CommandOutput, EvaluateArgs};

pub fn execute(args: &EvaluateArgs) -> Result<CommandOutput, CommandFailure> {
    Err(CommandFailure::not_implemented(format!(
        "evaluation command is reserved for phase 9: {}",
        args.comparison.display()
    )))
}
