use ao_next_core::contracts::RunRequest;
use ao_next_core::recovery::{CheckpointIdentity, CheckpointJournal};

use super::{CommandFailure, CommandOutput, ReplayArgs, decode_file};

pub fn execute(args: &ReplayArgs) -> Result<CommandOutput, CommandFailure> {
    let request: RunRequest = decode_file(&args.request)?;
    let identity = CheckpointIdentity::from_request(&request)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let journal = CheckpointJournal::new(&args.checkpoint_root, 16 * 1024 * 1024)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let plan = journal
        .resume(&identity, &args.pending_effects)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    Ok(CommandOutput::new(
        serde_json::json!({
            "schema_version": "ao.next.replay-plan.v1",
            "run_id": request.run_id,
            "skipped_committed_effects": plan.skipped_committed_effects,
            "remaining_effects": plan.remaining_effects
        }),
        "replayed checkpoint without executing effects",
        0,
    ))
}
