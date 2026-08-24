use ao_next_core::adapter::AdapterAction;
use ao_next_core::contracts::{
    N7ExecutionAuthority, PreparedRunReceipt, validate_n7_execution_authority_current,
};
use ao_next_core::recovery::{CheckpointJournal, JournalEffectState};
use ao_next_core::strict_json::canonical_digest;
use chrono::Utc;

use super::live::{
    LiveVariant, PreparedN7Context, capture_context, execute_recovered_live,
    execution_journal_maximum_bytes, execution_journal_root, gate_retained_capture,
    load_trusted_live_input_for_recovery, load_verified_capture, normalize_retained_turn,
    recovered_terminal_output, revalidate_recovery_before_mutation, validate_execution_authority,
    validate_prepared_run_for_recovery,
};
use super::{CommandFailure, CommandOutput, RecoverLiveArgs, decode_file};

#[allow(
    clippy::too_many_lines,
    reason = "the recovery audit remains linear so every no-provider and authority gate is visible"
)]
pub fn execute(args: &RecoverLiveArgs) -> Result<CommandOutput, CommandFailure> {
    if [
        "AO_NEXT_LIVE_PROVIDER_CALLS",
        "AO_NEXT_PROVIDER_FREE_PROGRAM",
        "AO_NEXT_PROVIDER_FREE_PROGRAM_DIGEST",
    ]
    .into_iter()
    .any(|name| std::env::var_os(name).is_some())
    {
        return Err(CommandFailure::authorization(
            "provider authorization and program overrides are forbidden during recovery",
        ));
    }

    let now = Utc::now();
    let input = load_trusted_live_input_for_recovery(
        &args.input,
        LiveVariant::N7,
        &args.trusted_corpus_digest,
        &args.trusted_verifier_profile_digest,
        now,
    )?;
    let receipt: PreparedRunReceipt = decode_file(&args.prepared_run)?;
    let (git_workspace, prepared_run_digest) =
        validate_prepared_run_for_recovery(&args.input, &input, &receipt, now)?;
    let authority: N7ExecutionAuthority = decode_file(&args.authority)?;
    validate_execution_authority(
        &input,
        &receipt,
        &prepared_run_digest,
        &authority,
        now,
        false,
    )?;
    let journal = CheckpointJournal::new(
        execution_journal_root(&input),
        execution_journal_maximum_bytes(&input.request),
    )
    .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let mut provider_state = journal
        .provider_state(&input.request)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let Some(recorded_prepared_run) = provider_state.prepared_run_digest.as_ref() else {
        return Err(CommandFailure::invalid_input(
            "provider intent is missing; recover-live cannot start a provider",
        ));
    };
    if recorded_prepared_run != &prepared_run_digest {
        return Err(CommandFailure::invalid_input(
            "prepared-run digest contradicts the provider journal",
        ));
    }
    if provider_state.outcome_unknown() {
        return Err(CommandFailure::invalid_input(
            "provider outcome is unknown without retained capture",
        ));
    }

    let context = capture_context(&input, LiveVariant::N7);
    let (output, index_digest, _) = load_verified_capture(
        &input.raw_capture_root,
        &context,
        &provider_state,
        input.request.limits.max_output_bytes,
    )?;
    if provider_state.capture_index_digest.is_none() {
        journal
            .record_provider_capture_published(&input.request, &index_digest)
            .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    }
    provider_state = journal
        .provider_state(&input.request)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    if !provider_state.capture_verified {
        journal
            .record_provider_capture_verified(&input.request, &index_digest)
            .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    }
    gate_retained_capture(&input, &context, &index_digest, &output)?;

    let (turn, capture) = normalize_retained_turn(&input, &output)?;
    journal
        .record_adapter_turn_normalized(
            &input.request,
            &canonical_digest(&turn)
                .map_err(|error| CommandFailure::evidence(error.to_string()))?,
        )
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;

    let mut fresh_effect = false;
    let mut unknown_effect = false;
    let effects = turn
        .actions
        .iter()
        .filter_map(|action| match action {
            AdapterAction::Effect(effect) => Some(effect.clone()),
            _ => None,
        })
        .collect::<Vec<_>>();
    for state in journal
        .effect_states(&input.request, &effects)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?
    {
        match state {
            JournalEffectState::Fresh => fresh_effect = true,
            JournalEffectState::Unknown => unknown_effect = true,
            JournalEffectState::Completed(_) => {}
        }
    }
    if unknown_effect {
        return Err(CommandFailure::invalid_input(
            "effect completion is unknown; automatic retry is forbidden",
        ));
    }
    if fresh_effect {
        validate_n7_execution_authority_current(&authority, Utc::now())
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    }
    if !fresh_effect
        && let Some(bytes) = journal
            .retained_terminal_record(&input.request)
            .map_err(|error| CommandFailure::evidence(error.to_string()))?
    {
        let output =
            recovered_terminal_output(&input, &git_workspace, &index_digest, &capture, &bytes)?;
        journal
            .publish_terminal_record(&input.request, &bytes)
            .map_err(|error| CommandFailure::evidence(error.to_string()))?;
        return Ok(output);
    }
    if fresh_effect {
        revalidate_recovery_before_mutation(&input, &git_workspace)?;
        validate_n7_execution_authority_current(&authority, Utc::now())
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    }

    execute_recovered_live(
        &input,
        turn,
        capture,
        output,
        PreparedN7Context {
            git_workspace,
            prepared_run_digest,
            execution_authority: authority,
        },
        &index_digest,
    )
}
