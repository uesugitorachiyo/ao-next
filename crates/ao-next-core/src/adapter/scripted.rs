use std::collections::VecDeque;

use super::{AdapterError, AdapterIdentity, AdapterTurn, RuntimeAdapter, TurnContext};

#[derive(Debug)]
pub struct ScriptedAdapter {
    identity: AdapterIdentity,
    turns: VecDeque<Result<AdapterTurn, AdapterError>>,
    contexts: Vec<TurnContext>,
}

impl ScriptedAdapter {
    pub fn new(
        identity: AdapterIdentity,
        turns: impl IntoIterator<Item = Result<AdapterTurn, AdapterError>>,
    ) -> Self {
        Self {
            identity,
            turns: turns.into_iter().collect(),
            contexts: Vec::new(),
        }
    }

    #[must_use]
    pub fn contexts(&self) -> &[TurnContext] {
        &self.contexts
    }
}

impl RuntimeAdapter for ScriptedAdapter {
    fn identity(&self) -> AdapterIdentity {
        self.identity.clone()
    }

    fn execute_turn(&mut self, context: &TurnContext) -> Result<AdapterTurn, AdapterError> {
        self.contexts.push(context.clone());
        self.turns
            .pop_front()
            .unwrap_or(Err(AdapterError::ScriptExhausted))
    }
}
