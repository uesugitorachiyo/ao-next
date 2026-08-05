use ao_next_core::contracts::TerminalReadback;
use ao_next_core::mission::assess_compatibility;

use super::{CommandFailure, CommandOutput, InspectArgs, decode_file};

pub fn execute(args: &InspectArgs) -> Result<CommandOutput, CommandFailure> {
    let readback: TerminalReadback = decode_file(&args.readback)?;
    assess_compatibility(&readback)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let value = serde_json::to_value(&readback)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    Ok(CommandOutput::new(
        value,
        format!("inspected terminal readback {}", readback.run_id),
        0,
    ))
}
