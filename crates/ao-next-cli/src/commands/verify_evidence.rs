use ao_next_core::contracts::RunRequest;
use ao_next_core::evidence::verify_sealed_run;

use super::{CommandFailure, CommandOutput, VerifyEvidenceArgs, decode_file};

pub fn execute(args: &VerifyEvidenceArgs) -> Result<CommandOutput, CommandFailure> {
    let request: RunRequest = decode_file(&args.request)?;
    verify_sealed_run(&args.root, &request, 16 * 1024 * 1024)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    Ok(CommandOutput::new(
        serde_json::json!({
            "schema_version": "ao.next.evidence-verification.v1",
            "run_id": request.run_id,
            "verified": true
        }),
        "verified sealed evidence",
        0,
    ))
}
