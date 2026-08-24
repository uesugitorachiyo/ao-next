use std::io::Write as _;
use std::path::Path;

use ao_next_core::contracts::PreparedRunReceipt;
use ao_next_core::evidence::digest_bytes;
use ao_next_core::recovery::{CheckpointIdentity, CheckpointJournal};
use ao_next_core::strict_json::{canonical_digest, canonical_json_bytes};
use chrono::Utc;

use super::live::{
    LiveVariant, execution_journal_maximum_bytes, execution_journal_root, load_trusted_live_input,
    prepare_git_workspace,
};
use super::{CommandFailure, CommandOutput, PrepareLiveArgs, read_bounded_regular};

pub fn execute(args: &PrepareLiveArgs) -> Result<CommandOutput, CommandFailure> {
    if std::env::var_os("AO_NEXT_LIVE_PROVIDER_CALLS").is_some() {
        return Err(CommandFailure::authorization(
            "provider authorization must be absent during live preparation",
        ));
    }
    reject_existing_output(&args.out)?;
    let input_bytes = read_bounded_regular(&args.input)?;
    let prepared_at = Utc::now();
    let input = load_trusted_live_input(
        &args.input,
        LiveVariant::N7,
        &args.trusted_corpus_digest,
        &args.trusted_verifier_profile_digest,
        prepared_at,
    )?;
    let git = prepare_git_workspace(
        &input.request.workspace.root,
        &input.request.authority.allowed_roots,
        &input.request.workspace.seed_digest,
    )?;
    let journal = CheckpointJournal::new(
        execution_journal_root(&input),
        execution_journal_maximum_bytes(&input.request),
    )
    .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    journal
        .bind_request(&input.request)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let journal_identity = CheckpointIdentity::from_request(&input.request)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    if read_bounded_regular(&args.input)? != input_bytes {
        return Err(CommandFailure::invalid_input(
            "live input drifted during workspace preparation",
        ));
    }
    let receipt = PreparedRunReceipt {
        schema_version: "ao.next.prepared-run.v1".into(),
        run_id: input.request.run_id.clone(),
        input_digest: digest_bytes(&input_bytes),
        request_digest: canonical_digest(&input.request)
            .map_err(|error| CommandFailure::evidence(error.to_string()))?,
        repository_root: git.repository_root,
        common_directory: git.common_dir,
        branch: git.branch.into(),
        base_commit: git.head_commit,
        control_digest: git.control_digest,
        index_digest: git.index_digest,
        workspace_digest: input.request.workspace.seed_digest.clone(),
        journal_identity_digest: canonical_digest(&journal_identity)
            .map_err(|error| CommandFailure::evidence(error.to_string()))?,
        prepared_at,
        expires_at: input.request.authority.expires_at,
        provider_calls: 0,
        safe_to_execute: false,
    };
    let bytes = canonical_json_bytes(&receipt)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    write_create_new(&args.out, &bytes)?;
    Ok(CommandOutput::new(
        serde_json::to_value(&receipt)
            .map_err(|error| CommandFailure::evidence(error.to_string()))?,
        "prepared exact live Git identity without a provider call",
        0,
    ))
}

fn reject_existing_output(path: &Path) -> Result<(), CommandFailure> {
    match std::fs::symlink_metadata(path) {
        Ok(_) => {
            return Err(CommandFailure::invalid_input(
                "prepared-run output already exists",
            ));
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
        Err(error) => return Err(CommandFailure::invalid_input(error.to_string())),
    }
    let parent = path
        .parent()
        .filter(|value| !value.as_os_str().is_empty())
        .unwrap_or_else(|| Path::new("."));
    let metadata = std::fs::symlink_metadata(parent)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if metadata.file_type().is_symlink() || !metadata.is_dir() {
        return Err(CommandFailure::invalid_input(
            "prepared-run output parent is not a regular non-symlink directory",
        ));
    }
    Ok(())
}

fn write_create_new(path: &Path, bytes: &[u8]) -> Result<(), CommandFailure> {
    let mut file = std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(path)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    file.write_all(bytes)
        .and_then(|()| file.sync_all())
        .map_err(|error| CommandFailure::evidence(error.to_string()))
}
