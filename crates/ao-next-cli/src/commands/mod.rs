use std::path::{Path, PathBuf};

use clap::{Args, Parser, Subcommand};

pub mod evaluate;
pub mod inspect;
pub mod replay;
pub mod run;
pub mod verify_evidence;

const MAXIMUM_INPUT_BYTES: u64 = 1024 * 1024;

#[derive(Debug, Parser)]
#[command(name = "ao-next", version, about = "Bounded AO Next local candidate")]
pub struct Cli {
    #[command(subcommand)]
    command: Command,
}

#[derive(Debug, Subcommand)]
enum Command {
    Run(RunArgs),
    Inspect(InspectArgs),
    VerifyEvidence(VerifyEvidenceArgs),
    Replay(ReplayArgs),
    Evaluate(EvaluateArgs),
}

#[derive(Debug, Args)]
pub struct RunArgs {
    #[arg(long)]
    pub request: PathBuf,
    #[arg(long)]
    pub script: PathBuf,
    #[arg(long)]
    pub evidence: PathBuf,
    #[arg(long)]
    pub now: String,
}

#[derive(Debug, Args)]
pub struct InspectArgs {
    #[arg(long)]
    pub readback: PathBuf,
}

#[derive(Debug, Args)]
pub struct VerifyEvidenceArgs {
    #[arg(long)]
    pub root: PathBuf,
    #[arg(long)]
    pub request: PathBuf,
}

#[derive(Debug, Args)]
pub struct ReplayArgs {
    #[arg(long)]
    pub checkpoint_root: PathBuf,
    #[arg(long)]
    pub request: PathBuf,
    #[arg(long = "pending-effect")]
    pub pending_effects: Vec<String>,
}

#[derive(Debug, Args)]
pub struct EvaluateArgs {
    #[arg(long)]
    pub comparison: PathBuf,
}

#[derive(Debug)]
pub struct CommandOutput {
    pub value: serde_json::Value,
    pub summary: String,
    pub status: u8,
}

impl CommandOutput {
    #[must_use]
    pub fn new(value: serde_json::Value, summary: impl Into<String>, status: u8) -> Self {
        Self {
            value,
            summary: summary.into(),
            status,
        }
    }
}

#[derive(Debug)]
pub struct CommandFailure {
    pub status: u8,
    pub code: &'static str,
    pub message: String,
}

impl CommandFailure {
    #[must_use]
    pub fn usage(message: impl Into<String>) -> Self {
        Self {
            status: 2,
            code: "usage",
            message: message.into(),
        }
    }

    #[must_use]
    pub fn invalid_input(message: impl Into<String>) -> Self {
        Self {
            status: 3,
            code: "invalid_input",
            message: message.into(),
        }
    }

    #[must_use]
    pub fn evidence(message: impl Into<String>) -> Self {
        Self {
            status: 7,
            code: "evidence_failure",
            message: message.into(),
        }
    }

    #[must_use]
    pub fn not_implemented(message: impl Into<String>) -> Self {
        Self {
            status: 5,
            code: "not_implemented",
            message: message.into(),
        }
    }
}

pub fn execute(cli: Cli) -> Result<CommandOutput, CommandFailure> {
    match cli.command {
        Command::Run(args) => run::execute(&args),
        Command::Inspect(args) => inspect::execute(&args),
        Command::VerifyEvidence(args) => verify_evidence::execute(&args),
        Command::Replay(args) => replay::execute(&args),
        Command::Evaluate(args) => evaluate::execute(&args),
    }
}

pub fn read_bounded_regular(path: &Path) -> Result<Vec<u8>, CommandFailure> {
    let metadata = std::fs::symlink_metadata(path)
        .map_err(|error| CommandFailure::invalid_input(format!("{}: {error}", path.display())))?;
    if metadata.file_type().is_symlink() || !metadata.is_file() {
        return Err(CommandFailure::invalid_input(format!(
            "input is not a regular non-symlink file: {}",
            path.display()
        )));
    }
    if metadata.len() > MAXIMUM_INPUT_BYTES {
        return Err(CommandFailure::invalid_input(format!(
            "input exceeds {MAXIMUM_INPUT_BYTES} bytes: {}",
            path.display()
        )));
    }
    std::fs::read(path)
        .map_err(|error| CommandFailure::invalid_input(format!("{}: {error}", path.display())))
}

pub fn decode_file<T>(path: &Path) -> Result<T, CommandFailure>
where
    T: serde::de::DeserializeOwned,
{
    let bytes = read_bounded_regular(path)?;
    let maximum_bytes = usize::try_from(MAXIMUM_INPUT_BYTES).unwrap_or(usize::MAX);
    ao_next_core::strict_json::decode_strict_json(&bytes, maximum_bytes)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))
}
