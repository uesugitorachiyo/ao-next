use super::{CommandFailure, CommandOutput, LiveRunArgs};

#[derive(Clone, Copy, Debug)]
pub enum LiveVariant {
    N4,
    N7,
}

pub fn execute(args: &LiveRunArgs, variant: LiveVariant) -> Result<CommandOutput, CommandFailure> {
    if std::env::var("AO_NEXT_LIVE_PROVIDER_CALLS").as_deref() != Ok("operator-authorized") {
        return Err(CommandFailure::authorization(
            "live provider calls require separate operator authorization",
        ));
    }
    Err(CommandFailure::invalid_input(format!(
        "{variant:?} live input is not implemented: {}",
        args.input.display()
    )))
}
