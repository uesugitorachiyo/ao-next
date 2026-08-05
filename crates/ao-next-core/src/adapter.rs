use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use thiserror::Error;

pub use crate::contracts::AdapterIdentity;
use crate::contracts::{Digest, EffectRequest, SourceIdentity, WorkspaceIdentity};

pub mod scripted;

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct TurnContext {
    pub run_id: String,
    pub turn_index: u32,
    pub repair_attempt: u32,
    pub source: SourceIdentity,
    pub workspace: WorkspaceIdentity,
    pub authority_digest: Digest,
    pub policy_digest: Digest,
    pub verifier_profile_digest: Digest,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct TokenUsage {
    pub input_tokens: u64,
    pub cached_input_tokens: u64,
    pub reasoning_tokens: u64,
    pub output_tokens: u64,
    pub output_bytes: u64,
}

impl TokenUsage {
    #[must_use]
    pub fn total_tokens(&self) -> u64 {
        self.input_tokens
            .saturating_add(self.cached_input_tokens)
            .saturating_add(self.reasoning_tokens)
            .saturating_add(self.output_tokens)
    }

    pub(crate) fn accumulate(&mut self, other: &Self) {
        self.input_tokens = self.input_tokens.saturating_add(other.input_tokens);
        self.cached_input_tokens = self
            .cached_input_tokens
            .saturating_add(other.cached_input_tokens);
        self.reasoning_tokens = self.reasoning_tokens.saturating_add(other.reasoning_tokens);
        self.output_tokens = self.output_tokens.saturating_add(other.output_tokens);
        self.output_bytes = self.output_bytes.saturating_add(other.output_bytes);
    }
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ControlMutation {
    Authority,
    Policy,
    Verifier,
    TerminalState,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(rename_all = "snake_case", tag = "kind", content = "value")]
pub enum AdapterAction {
    Effect(EffectRequest),
    Verify,
    Blocked(String),
    Interrupt,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct AdapterTurn {
    pub actions: Vec<AdapterAction>,
    pub usage: TokenUsage,
    pub model_claimed_success: bool,
    pub control_mutations: Vec<ControlMutation>,
}

#[derive(Debug, Error)]
pub enum AdapterError {
    #[error("adapter runtime failed: {0}")]
    Runtime(String),
    #[error("scripted adapter has no remaining turn")]
    ScriptExhausted,
}

pub trait RuntimeAdapter {
    fn identity(&self) -> AdapterIdentity;

    /// Executes one turn for the immutable run context.
    ///
    /// # Errors
    ///
    /// Returns [`AdapterError`] when the adapter cannot produce a bounded,
    /// structured turn.
    fn execute_turn(&mut self, context: &TurnContext) -> Result<AdapterTurn, AdapterError>;
}
