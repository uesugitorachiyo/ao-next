use ao_next_core::adapter::process::ProcessRunner;
use ao_next_core::adapter::{
    AdapterAction, CancellationToken, InvocationError, InvocationOutput, PreparedInvocation,
};
use ao_next_core::contracts::{PreparedRunReceipt, validate_authority_current};
use ao_next_core::recovery::{CheckpointJournal, JournalEffectState};
use ao_next_core::strict_json::canonical_digest;
use chrono::Utc;

use super::live::{
    LiveVariant, capture_context, execute_recovered_live, execution_journal_maximum_bytes,
    execution_journal_root, gate_retained_capture, load_trusted_live_input_for_recovery,
    load_verified_capture, normalize_retained_turn, revalidate_recovery_before_mutation,
    validate_prepared_run_for_recovery,
};
use super::{CommandFailure, CommandOutput, RecoverLiveArgs, decode_file};

struct RetainedCaptureRunner {
    output: Option<InvocationOutput>,
}

impl ProcessRunner for RetainedCaptureRunner {
    fn run(
        &mut self,
        _: &PreparedInvocation,
        _: &CancellationToken,
    ) -> Result<InvocationOutput, InvocationError> {
        self.output
            .take()
            .ok_or_else(|| InvocationError::Io("retained capture already consumed".into()))
    }
}

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

    let turn = normalize_retained_turn(
        &input,
        &git_workspace,
        RetainedCaptureRunner {
            output: Some(output.clone()),
        },
    )?;
    journal
        .record_adapter_turn_normalized(
            &input.request,
            &canonical_digest(&turn)
                .map_err(|error| CommandFailure::evidence(error.to_string()))?,
        )
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;

    let mut fresh_effect = false;
    let mut unknown_effect = false;
    for action in &turn.actions {
        let AdapterAction::Effect(effect) = action else {
            continue;
        };
        match journal
            .effect_state(&input.request, effect)
            .map_err(|error| CommandFailure::evidence(error.to_string()))?
        {
            JournalEffectState::Fresh => fresh_effect = true,
            JournalEffectState::Unknown => unknown_effect = true,
            JournalEffectState::Completed(_) => {}
        }
    }
    if fresh_effect || unknown_effect {
        validate_authority_current(&input.request.authority, Utc::now())
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    }
    if unknown_effect {
        return Err(CommandFailure::invalid_input(
            "effect completion is unknown; automatic retry is forbidden",
        ));
    }
    if fresh_effect {
        revalidate_recovery_before_mutation(&input, &git_workspace)?;
        validate_authority_current(&input.request.authority, Utc::now())
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    }

    execute_recovered_live(
        &input,
        RetainedCaptureRunner {
            output: Some(output),
        },
        (git_workspace, prepared_run_digest),
        &index_digest,
    )
}
