use std::collections::BTreeSet;
use std::time::{Duration, Instant};

use schemars::JsonSchema;
use serde::{Deserialize, Serialize};

use crate::adapter::{
    AdapterAction, AdapterIdentity, ControlMutation, EffectObservation, RuntimeAdapter, TokenUsage,
    TurnContext,
};
use crate::contracts::{Digest, RunRequest, RunState};
use crate::effects::EffectBroker;
use crate::strict_json::canonical_digest;
use crate::terminal::RunLifecycle;

pub const MAX_EFFECTS_PER_TURN: usize = 64;

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct VerificationOutcome {
    pub passed: bool,
    pub report_digest: Digest,
    pub summary: String,
}

pub trait EngineVerifier {
    fn verify(&mut self, request: &RunRequest) -> VerificationOutcome;
}

#[derive(Clone, Debug, Default, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct EngineMetrics {
    pub turns: u32,
    pub repair_attempts: u32,
    pub usage: TokenUsage,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(rename_all = "snake_case", tag = "kind", content = "value")]
pub enum EngineEventKind {
    Received,
    Validated,
    Running,
    AdapterTurn,
    EffectAdmitted(String),
    EffectCompleted(EffectObservation),
    EffectDenied(String),
    VerificationPassed(Digest),
    VerificationFailed(Digest),
    ControlMutationRejected(ControlMutation),
    Terminal(RunState),
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct EngineEvent {
    pub sequence: u64,
    pub state: RunState,
    pub kind: EngineEventKind,
    pub worker_id: Option<String>,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct RunOutcome {
    pub terminal_state: RunState,
    pub worker_identity: AdapterIdentity,
    pub metrics: EngineMetrics,
    pub events: Vec<EngineEvent>,
    pub effect_observations: Vec<EffectObservation>,
    pub verifier_report_digest: Option<Digest>,
    pub failure_code: Option<String>,
    pub blocker: Option<String>,
}

pub struct DirectEngine<'a, B> {
    broker: &'a B,
}

impl<'a, B> DirectEngine<'a, B>
where
    B: EffectBroker,
{
    #[must_use]
    pub const fn new(broker: &'a B) -> Self {
        Self { broker }
    }

    #[allow(
        clippy::too_many_lines,
        reason = "the audited state machine stays linear so every terminal path is visible"
    )]
    pub fn run<A, V>(&self, request: &RunRequest, adapter: &mut A, verifier: &mut V) -> RunOutcome
    where
        A: RuntimeAdapter,
        V: EngineVerifier,
    {
        let identity = adapter.identity();
        let mut lifecycle = RunLifecycle::new();
        let mut events = Vec::new();
        let mut metrics = EngineMetrics::default();
        let mut verifier_report_digest = None;
        push_event(&mut events, &lifecycle, EngineEventKind::Received, None);

        if !adapter_matches_request(&identity, request) {
            return fail_outcome(
                identity,
                metrics,
                events,
                verifier_report_digest,
                "adapter_identity_mismatch",
                "adapter identity does not match the model profile",
                RunState::Failed,
            );
        }
        if lifecycle.transition(RunState::Validated).is_err() {
            return internal_transition_failure(identity, metrics, events);
        }
        push_event(&mut events, &lifecycle, EngineEventKind::Validated, None);
        if lifecycle.transition(RunState::Running).is_err() {
            return internal_transition_failure(identity, metrics, events);
        }
        push_event(
            &mut events,
            &lifecycle,
            EngineEventKind::Running,
            Some(&identity.worker_id),
        );

        let authority_digest = match canonical_digest(&request.authority) {
            Ok(digest) => digest,
            Err(error) => {
                return fail_outcome(
                    identity,
                    metrics,
                    events,
                    verifier_report_digest,
                    "contract_digest_failure",
                    &error.to_string(),
                    RunState::Failed,
                );
            }
        };
        let started = Instant::now();
        let maximum_duration = Duration::from_millis(request.limits.max_run_ms);
        let mut effect_observations = Vec::new();
        let mut effect_ids = BTreeSet::new();

        for turn_index in 0..request.limits.max_turns {
            if started.elapsed() >= maximum_duration {
                return transition_and_finish(
                    lifecycle,
                    identity,
                    metrics,
                    events,
                    verifier_report_digest,
                    RunState::Interrupted,
                    "time_limit",
                    "run duration limit reached",
                );
            }
            if adapter.identity() != identity {
                return transition_and_finish(
                    lifecycle,
                    identity,
                    metrics,
                    events,
                    verifier_report_digest,
                    RunState::Failed,
                    "worker_identity_drift",
                    "adapter worker identity changed during the run",
                );
            }
            let context = TurnContext {
                run_id: request.run_id.clone(),
                turn_index,
                repair_attempt: metrics.repair_attempts,
                source: request.source.clone(),
                workspace: request.workspace.clone(),
                authority_digest: authority_digest.clone(),
                policy_digest: request.policy_digest.clone(),
                verifier_profile_digest: request.verifier_profile.profile_digest.clone(),
                effect_observations: effect_observations.clone(),
            };
            let turn = match adapter.execute_turn(&context) {
                Ok(turn) => turn,
                Err(error) => {
                    return transition_and_finish(
                        lifecycle,
                        identity,
                        metrics,
                        events,
                        verifier_report_digest,
                        RunState::Failed,
                        "adapter_failure",
                        &error.to_string(),
                    );
                }
            };
            metrics.turns = metrics.turns.saturating_add(1);
            if metrics.usage.checked_accumulate(&turn.usage).is_none() {
                return transition_and_finish(
                    lifecycle,
                    identity,
                    metrics,
                    events,
                    verifier_report_digest,
                    RunState::Failed,
                    "token_limit",
                    "adapter token usage overflowed",
                );
            }
            push_event(
                &mut events,
                &lifecycle,
                EngineEventKind::AdapterTurn,
                Some(&identity.worker_id),
            );

            let Some(total_tokens) = metrics.usage.checked_total_tokens() else {
                return transition_and_finish(
                    lifecycle,
                    identity,
                    metrics,
                    events,
                    verifier_report_digest,
                    RunState::Failed,
                    "token_limit",
                    "adapter token usage overflowed",
                );
            };
            if total_tokens > request.limits.max_tokens {
                return transition_and_finish(
                    lifecycle,
                    identity,
                    metrics,
                    events,
                    verifier_report_digest,
                    RunState::Failed,
                    "token_limit",
                    "adapter token limit exceeded",
                );
            }
            if metrics.usage.output_bytes > request.limits.max_output_bytes {
                return transition_and_finish(
                    lifecycle,
                    identity,
                    metrics,
                    events,
                    verifier_report_digest,
                    RunState::Failed,
                    "output_limit",
                    "adapter output limit exceeded",
                );
            }
            if let Some(mutation) = turn.control_mutations.first() {
                push_event(
                    &mut events,
                    &lifecycle,
                    EngineEventKind::ControlMutationRejected(mutation.clone()),
                    Some(&identity.worker_id),
                );
                return transition_and_finish(
                    lifecycle,
                    identity,
                    metrics,
                    events,
                    verifier_report_digest,
                    RunState::Failed,
                    "adapter_control_mutation",
                    "adapter attempted to alter deterministic controls",
                );
            }

            if turn
                .actions
                .iter()
                .filter(|action| matches!(action, AdapterAction::Effect(_)))
                .count()
                > MAX_EFFECTS_PER_TURN
            {
                return transition_and_finish(
                    lifecycle,
                    identity,
                    metrics,
                    events,
                    verifier_report_digest,
                    RunState::Denied,
                    "effect_limit",
                    "adapter effect count exceeded the per-turn bound",
                );
            }

            for action in turn.actions {
                match action {
                    AdapterAction::Effect(effect) => {
                        if effect.run_id != request.run_id {
                            push_event(
                                &mut events,
                                &lifecycle,
                                EngineEventKind::EffectDenied(effect.effect_id),
                                Some(&identity.worker_id),
                            );
                            return transition_and_finish(
                                lifecycle,
                                identity,
                                metrics,
                                events,
                                verifier_report_digest,
                                RunState::Denied,
                                "effect_run_identity_mismatch",
                                "effect run identity does not match the request",
                            );
                        }
                        if !effect_ids.insert(effect.effect_id.clone()) {
                            push_event(
                                &mut events,
                                &lifecycle,
                                EngineEventKind::EffectDenied(effect.effect_id),
                                Some(&identity.worker_id),
                            );
                            return transition_and_finish(
                                lifecycle,
                                identity,
                                metrics,
                                events,
                                verifier_report_digest,
                                RunState::Denied,
                                "duplicate_effect",
                                "adapter repeated an effect identity",
                            );
                        }
                        match self.broker.authorize(&effect, &request.authority) {
                            Ok(authorized) => {
                                push_event(
                                    &mut events,
                                    &lifecycle,
                                    EngineEventKind::EffectAdmitted(effect.effect_id.clone()),
                                    Some(&identity.worker_id),
                                );
                                let output = match self.broker.execute_authorized(&authorized) {
                                    Ok(output) => output,
                                    Err(error) => {
                                        return transition_and_finish(
                                            lifecycle,
                                            identity,
                                            metrics,
                                            events,
                                            verifier_report_digest,
                                            RunState::Failed,
                                            "effect_execution_failure",
                                            &error.to_string(),
                                        );
                                    }
                                };
                                if output.stdout.len().saturating_add(output.stderr.len())
                                    > usize::try_from(request.limits.max_output_bytes)
                                        .unwrap_or(usize::MAX)
                                {
                                    return transition_and_finish(
                                        lifecycle,
                                        identity,
                                        metrics,
                                        events,
                                        verifier_report_digest,
                                        RunState::Failed,
                                        "effect_output_limit",
                                        "effect output exceeded the run output bound",
                                    );
                                }
                                let output_digest = match canonical_digest(&(
                                    output.status,
                                    &output.stdout,
                                    &output.stderr,
                                )) {
                                    Ok(digest) => digest,
                                    Err(error) => {
                                        return transition_and_finish(
                                            lifecycle,
                                            identity,
                                            metrics,
                                            events,
                                            verifier_report_digest,
                                            RunState::Failed,
                                            "effect_output_digest_failure",
                                            &error.to_string(),
                                        );
                                    }
                                };
                                let observation = EffectObservation {
                                    effect_id: effect.effect_id.clone(),
                                    status: output.status,
                                    stdout: output.stdout,
                                    stderr: output.stderr,
                                    output_digest,
                                };
                                effect_observations.push(observation.clone());
                                push_event(
                                    &mut events,
                                    &lifecycle,
                                    EngineEventKind::EffectCompleted(observation),
                                    Some(&identity.worker_id),
                                );
                            }
                            Err(denial) => {
                                push_event(
                                    &mut events,
                                    &lifecycle,
                                    EngineEventKind::EffectDenied(effect.effect_id),
                                    Some(&identity.worker_id),
                                );
                                return transition_and_finish(
                                    lifecycle,
                                    identity,
                                    metrics,
                                    events,
                                    verifier_report_digest,
                                    RunState::Denied,
                                    "effect_denied",
                                    &denial.to_string(),
                                );
                            }
                        }
                    }
                    AdapterAction::Verify => {
                        if lifecycle.transition(RunState::Verifying).is_err() {
                            return internal_transition_failure(identity, metrics, events);
                        }
                        let verification = verifier.verify(request);
                        verifier_report_digest = Some(verification.report_digest.clone());
                        if verification.passed {
                            push_event(
                                &mut events,
                                &lifecycle,
                                EngineEventKind::VerificationPassed(verification.report_digest),
                                Some(&identity.worker_id),
                            );
                            return transition_and_finish(
                                lifecycle,
                                identity,
                                metrics,
                                events,
                                verifier_report_digest,
                                RunState::Passed,
                                "verified_pass",
                                &verification.summary,
                            );
                        }
                        push_event(
                            &mut events,
                            &lifecycle,
                            EngineEventKind::VerificationFailed(verification.report_digest),
                            Some(&identity.worker_id),
                        );
                        if metrics.repair_attempts < request.limits.max_repair_attempts {
                            metrics.repair_attempts = metrics.repair_attempts.saturating_add(1);
                            if lifecycle.transition(RunState::Running).is_err() {
                                return internal_transition_failure(identity, metrics, events);
                            }
                            break;
                        }
                        return transition_and_finish(
                            lifecycle,
                            identity,
                            metrics,
                            events,
                            verifier_report_digest,
                            RunState::Failed,
                            "verification_failed",
                            &verification.summary,
                        );
                    }
                    AdapterAction::Blocked(reason) => {
                        return transition_and_finish(
                            lifecycle,
                            identity,
                            metrics,
                            events,
                            verifier_report_digest,
                            RunState::Failed,
                            "adapter_blocked",
                            &reason,
                        );
                    }
                    AdapterAction::Interrupt => {
                        return transition_and_finish(
                            lifecycle,
                            identity,
                            metrics,
                            events,
                            verifier_report_digest,
                            RunState::Interrupted,
                            "adapter_interrupted",
                            "adapter requested interruption",
                        );
                    }
                }
            }
        }

        transition_and_finish(
            lifecycle,
            identity,
            metrics,
            events,
            verifier_report_digest,
            RunState::Failed,
            "turn_limit",
            "adapter turn limit reached without verifier success",
        )
    }
}

fn adapter_matches_request(identity: &AdapterIdentity, request: &RunRequest) -> bool {
    identity.runtime == request.model_profile.runtime
        && identity.model_identifier == request.model_profile.model_identifier
        && identity.adapter_version == request.model_profile.adapter_version
        && !identity.worker_id.is_empty()
}

fn push_event(
    events: &mut Vec<EngineEvent>,
    lifecycle: &RunLifecycle,
    kind: EngineEventKind,
    worker_id: Option<&str>,
) {
    events.push(EngineEvent {
        sequence: events.len() as u64,
        state: lifecycle.state().clone(),
        kind,
        worker_id: worker_id.map(ToOwned::to_owned),
    });
}

#[allow(clippy::too_many_arguments)]
fn transition_and_finish(
    mut lifecycle: RunLifecycle,
    identity: AdapterIdentity,
    metrics: EngineMetrics,
    mut events: Vec<EngineEvent>,
    verifier_report_digest: Option<Digest>,
    terminal_state: RunState,
    code: &str,
    blocker: &str,
) -> RunOutcome {
    if lifecycle.transition(terminal_state.clone()).is_err() {
        return internal_transition_failure(identity, metrics, events);
    }
    push_event(
        &mut events,
        &lifecycle,
        EngineEventKind::Terminal(terminal_state.clone()),
        Some(&identity.worker_id),
    );
    let effect_observations = completed_effect_observations(&events);
    RunOutcome {
        terminal_state,
        worker_identity: identity,
        metrics,
        events,
        effect_observations,
        verifier_report_digest,
        failure_code: (code != "verified_pass").then(|| code.to_owned()),
        blocker: (code != "verified_pass").then(|| blocker.to_owned()),
    }
}

#[allow(clippy::too_many_arguments)]
fn fail_outcome(
    identity: AdapterIdentity,
    metrics: EngineMetrics,
    mut events: Vec<EngineEvent>,
    verifier_report_digest: Option<Digest>,
    code: &str,
    blocker: &str,
    terminal_state: RunState,
) -> RunOutcome {
    events.push(EngineEvent {
        sequence: events.len() as u64,
        state: terminal_state.clone(),
        kind: EngineEventKind::Terminal(terminal_state.clone()),
        worker_id: Some(identity.worker_id.clone()),
    });
    let effect_observations = completed_effect_observations(&events);
    RunOutcome {
        terminal_state,
        worker_identity: identity,
        metrics,
        events,
        effect_observations,
        verifier_report_digest,
        failure_code: Some(code.to_owned()),
        blocker: Some(blocker.to_owned()),
    }
}

fn completed_effect_observations(events: &[EngineEvent]) -> Vec<EffectObservation> {
    events
        .iter()
        .filter_map(|event| match &event.kind {
            EngineEventKind::EffectCompleted(observation) => Some(observation.clone()),
            _ => None,
        })
        .collect()
}

fn internal_transition_failure(
    identity: AdapterIdentity,
    metrics: EngineMetrics,
    events: Vec<EngineEvent>,
) -> RunOutcome {
    fail_outcome(
        identity,
        metrics,
        events,
        None,
        "invalid_internal_transition",
        "direct engine attempted an invalid internal state transition",
        RunState::Failed,
    )
}
