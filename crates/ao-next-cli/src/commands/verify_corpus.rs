use ao_next_eval::corpus::CorpusManifest;

use super::{CommandFailure, CommandOutput, VerifyCorpusArgs, decode_file};

pub fn execute(args: &VerifyCorpusArgs) -> Result<CommandOutput, CommandFailure> {
    let corpus: CorpusManifest = decode_file(&args.corpus)?;
    corpus
        .validate_live()
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    Ok(CommandOutput::new(
        serde_json::json!({
            "schema_version": "ao.next.corpus-verification.v1",
            "corpus_digest": corpus.corpus_digest,
            "task_count": corpus.tasks.len(),
            "required_trial_count": corpus.required_trial_count,
            "live_eligible": true,
        }),
        "verified sealed live corpus",
        0,
    ))
}
