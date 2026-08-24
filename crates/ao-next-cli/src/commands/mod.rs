use std::path::{Path, PathBuf};

use clap::{Args, Parser, Subcommand, ValueEnum};

pub mod campaign;
pub mod evaluate;
pub mod inspect;
pub mod instantiate_corpus;
pub mod live;
pub mod live_prepare;
pub mod replay;
pub mod run;
pub mod verify_corpus;
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
    RunLive(LiveRunArgs),
    RunCurrentAoBaseline(LiveRunArgs),
    RunDirectBaseline(LiveRunArgs),
    PrepareLive(PrepareLiveArgs),
    PreflightLiveInput(PreflightLiveInputArgs),
    QualifyLiveCampaign(QualifyLiveCampaignArgs),
    QualifyRecovery(QualifyRecoveryArgs),
    Inspect(InspectArgs),
    VerifyEvidence(VerifyEvidenceArgs),
    Replay(ReplayArgs),
    Evaluate(EvaluateArgs),
    EvaluateLive(EvaluateArgs),
    InstantiateCorpus(InstantiateCorpusArgs),
    VerifyCorpus(VerifyCorpusArgs),
}

#[derive(Debug, Args)]
pub struct LiveRunArgs {
    #[arg(long)]
    pub input: PathBuf,
    #[arg(long)]
    pub prepared_run: Option<PathBuf>,
    #[arg(long)]
    pub trusted_corpus_digest: Option<String>,
    #[arg(long)]
    pub trusted_verifier_profile_digest: Option<String>,
}

#[derive(Debug, Args)]
pub struct PrepareLiveArgs {
    #[arg(long)]
    pub input: PathBuf,
    #[arg(long)]
    pub trusted_corpus_digest: String,
    #[arg(long)]
    pub trusted_verifier_profile_digest: String,
    #[arg(long)]
    pub out: PathBuf,
}

#[derive(Clone, Copy, Debug, ValueEnum)]
pub enum LiveVariantArg {
    N0,
    N4,
    N7,
}

#[derive(Debug, Args)]
pub struct PreflightLiveInputArgs {
    #[arg(long)]
    pub input: PathBuf,
    #[arg(long, value_enum, ignore_case = false)]
    pub variant: LiveVariantArg,
    #[arg(long)]
    pub trusted_corpus_digest: Option<String>,
    #[arg(long)]
    pub trusted_verifier_profile_digest: Option<String>,
}

#[derive(Debug, Args)]
pub struct QualifyLiveCampaignArgs {
    #[arg(long)]
    pub qualification: PathBuf,
    #[arg(long)]
    pub trusted_corpus_digest: String,
    #[arg(long = "trusted-verifier-profile", value_name = "TASK_ID=SHA256")]
    pub trusted_verifier_profiles: Vec<String>,
    #[arg(long)]
    pub fake_provider_program: PathBuf,
    #[arg(long)]
    pub fake_provider_program_digest: String,
}

#[derive(Debug, Args)]
pub struct QualifyRecoveryArgs {
    #[arg(long)]
    pub corpus: PathBuf,
    #[arg(long)]
    pub evidence_root: PathBuf,
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
    #[arg(long)]
    pub recovery_evidence_root: Option<PathBuf>,
}

#[derive(Debug, Args)]
pub struct VerifyCorpusArgs {
    #[arg(long)]
    pub corpus: PathBuf,
}

#[derive(Debug, Args)]
pub struct InstantiateCorpusArgs {
    #[arg(long)]
    pub corpus: PathBuf,
    #[arg(long)]
    pub bindings: PathBuf,
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
    pub diagnostic: Option<serde_json::Value>,
}

impl CommandFailure {
    #[must_use]
    pub fn usage(message: impl Into<String>) -> Self {
        Self {
            status: 2,
            code: "usage",
            message: message.into(),
            diagnostic: None,
        }
    }

    #[must_use]
    pub fn invalid_input(message: impl Into<String>) -> Self {
        Self {
            status: 3,
            code: "invalid_input",
            message: message.into(),
            diagnostic: None,
        }
    }

    #[must_use]
    pub fn evidence(message: impl Into<String>) -> Self {
        Self {
            status: 7,
            code: "evidence_failure",
            message: message.into(),
            diagnostic: None,
        }
    }

    #[must_use]
    pub fn authorization(message: impl Into<String>) -> Self {
        Self {
            status: 8,
            code: "authorization_denied",
            message: message.into(),
            diagnostic: None,
        }
    }

    #[must_use]
    pub fn runtime(message: impl Into<String>) -> Self {
        Self {
            status: 4,
            code: "runtime_failure",
            message: message.into(),
            diagnostic: None,
        }
    }

    #[must_use]
    pub fn runtime_with_diagnostic(
        message: impl Into<String>,
        diagnostic: serde_json::Value,
    ) -> Self {
        Self {
            status: 4,
            code: "runtime_failure",
            message: message.into(),
            diagnostic: Some(diagnostic),
        }
    }
}

pub fn execute(cli: Cli) -> Result<CommandOutput, CommandFailure> {
    match cli.command {
        Command::Run(args) => run::execute(&args),
        Command::RunLive(args) => live::execute(&args, live::LiveVariant::N7),
        Command::RunCurrentAoBaseline(args) => live::execute(&args, live::LiveVariant::N0),
        Command::RunDirectBaseline(args) => live::execute(&args, live::LiveVariant::N4),
        Command::PrepareLive(args) => live_prepare::execute(&args),
        Command::PreflightLiveInput(args) => live::preflight(&args),
        Command::QualifyLiveCampaign(args) => campaign::execute(&args),
        Command::QualifyRecovery(args) => campaign::execute_recovery(&args),
        Command::Inspect(args) => inspect::execute(&args),
        Command::VerifyEvidence(args) => verify_evidence::execute(&args),
        Command::Replay(args) => replay::execute(&args),
        Command::Evaluate(args) => evaluate::execute(&args),
        Command::EvaluateLive(args) => evaluate::execute_live(&args),
        Command::InstantiateCorpus(args) => instantiate_corpus::execute(&args),
        Command::VerifyCorpus(args) => verify_corpus::execute(&args),
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
