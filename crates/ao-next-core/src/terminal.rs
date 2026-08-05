use crate::contracts::RunState;
use thiserror::Error;

#[derive(Clone, Debug, Eq, Error, PartialEq)]
#[error("invalid run transition from {from:?} to {to:?}")]
pub struct InvalidTransition {
    pub from: RunState,
    pub to: RunState,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RunLifecycle {
    state: RunState,
}

impl RunLifecycle {
    #[must_use]
    pub const fn new() -> Self {
        Self {
            state: RunState::Received,
        }
    }

    #[must_use]
    pub fn state(&self) -> &RunState {
        &self.state
    }

    /// Moves the lifecycle through one permitted state edge.
    ///
    /// # Errors
    ///
    /// Returns [`InvalidTransition`] when the edge is not part of the direct
    /// runtime state machine or the current state is terminal.
    pub fn transition(&mut self, to: RunState) -> Result<(), InvalidTransition> {
        let permitted = matches!(
            (&self.state, &to),
            (RunState::Received, RunState::Validated)
                | (
                    RunState::Validated,
                    RunState::Running | RunState::Failed | RunState::Denied
                )
                | (
                    RunState::Running,
                    RunState::Verifying
                        | RunState::Failed
                        | RunState::Denied
                        | RunState::Interrupted
                )
                | (
                    RunState::Verifying,
                    RunState::Running | RunState::Passed | RunState::Failed | RunState::Interrupted
                )
        );
        if !permitted {
            return Err(InvalidTransition {
                from: self.state.clone(),
                to,
            });
        }
        self.state = to;
        Ok(())
    }
}

impl Default for RunLifecycle {
    fn default() -> Self {
        Self::new()
    }
}
