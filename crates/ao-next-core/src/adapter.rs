use std::collections::BTreeSet;
use std::io::{Read, Write};
use std::path::PathBuf;
use std::process::{Command, Stdio};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::thread;
use std::time::{Duration, Instant};

use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use thiserror::Error;

pub use crate::contracts::AdapterIdentity;
use crate::contracts::{Digest, EffectRequest, SourceIdentity, WorkspaceIdentity};

pub mod claude;
pub mod codex;
pub mod process;
pub mod scripted;

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct InvocationLimits {
    pub max_input_bytes: usize,
    pub max_output_bytes: usize,
    pub timeout_ms: u64,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PreparedInvocation {
    pub program: String,
    pub args: Vec<String>,
    pub stdin: Vec<u8>,
    pub cwd: PathBuf,
    pub limits: InvocationLimits,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct InvocationOutput {
    pub status: i32,
    pub stdout: Vec<u8>,
    pub stderr: Vec<u8>,
}

#[derive(Debug, Error)]
pub enum InvocationError {
    #[error("adapter executable is missing: {0}")]
    MissingExecutable(String),
    #[error("adapter working directory is unsafe: {0}")]
    UnsafeWorkingDirectory(PathBuf),
    #[error("adapter input exceeds {limit} bytes")]
    InputTooLarge { limit: usize },
    #[error("adapter output exceeds {limit} bytes")]
    OutputTooLarge { limit: usize },
    #[error("adapter invocation timed out")]
    TimedOut,
    #[error("adapter invocation was cancelled")]
    Cancelled,
    #[error("adapter invocation I/O failed: {0}")]
    Io(String),
    #[error("adapter output reader failed")]
    OutputReaderFailed,
}

#[derive(Clone, Debug, Default)]
pub struct CancellationToken(Arc<AtomicBool>);

impl CancellationToken {
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    pub fn cancel(&self) {
        self.0.store(true, Ordering::Release);
    }

    #[must_use]
    pub fn is_cancelled(&self) -> bool {
        self.0.load(Ordering::Acquire)
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CliContract {
    pub runtime: String,
    pub version: String,
    pub required_flags: BTreeSet<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct NormalizedAdapterTurn {
    pub identity: AdapterIdentity,
    pub turn: AdapterTurn,
}

#[derive(Debug, Error)]
pub enum AdapterContractError {
    #[error("captured CLI contract is malformed")]
    MalformedCliContract,
    #[error("captured CLI contract is missing required flag {0}")]
    MissingCliFlag(String),
    #[error("adapter identity does not match runtime {0}")]
    IdentityMismatch(&'static str),
    #[error("adapter invocation is invalid: {0}")]
    InvalidInvocation(String),
    #[error("adapter output exceeds {limit} bytes")]
    OutputTooLarge { limit: usize },
    #[error("adapter output is malformed: {0}")]
    MalformedOutput(String),
    #[error("adapter output contains no terminal turn")]
    MissingTurn,
}

/// Executes an already prepared adapter command without a shell and with hard
/// input, output, timeout, and cancellation bounds.
///
/// # Errors
///
/// Returns [`InvocationError`] for unsafe directories, missing programs, I/O
/// errors, cancellation, timeout, or size violations.
pub fn execute_bounded(
    invocation: &PreparedInvocation,
    cancellation: &CancellationToken,
) -> Result<InvocationOutput, InvocationError> {
    if invocation.stdin.len() > invocation.limits.max_input_bytes {
        return Err(InvocationError::InputTooLarge {
            limit: invocation.limits.max_input_bytes,
        });
    }
    let metadata = std::fs::symlink_metadata(&invocation.cwd)
        .map_err(|_| InvocationError::UnsafeWorkingDirectory(invocation.cwd.clone()))?;
    if metadata.file_type().is_symlink() || !metadata.is_dir() {
        return Err(InvocationError::UnsafeWorkingDirectory(
            invocation.cwd.clone(),
        ));
    }
    if cancellation.is_cancelled() {
        return Err(InvocationError::Cancelled);
    }

    let mut child = Command::new(&invocation.program)
        .args(&invocation.args)
        .current_dir(&invocation.cwd)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|error| {
            if error.kind() == std::io::ErrorKind::NotFound {
                InvocationError::MissingExecutable(invocation.program.clone())
            } else {
                InvocationError::Io(error.to_string())
            }
        })?;
    let mut stdin = child
        .stdin
        .take()
        .ok_or(InvocationError::OutputReaderFailed)?;
    let input = invocation.stdin.clone();
    let stdin_writer = thread::spawn(move || stdin.write_all(&input));
    let stdout = child
        .stdout
        .take()
        .ok_or(InvocationError::OutputReaderFailed)?;
    let stderr = child
        .stderr
        .take()
        .ok_or(InvocationError::OutputReaderFailed)?;
    let reader_limit = invocation.limits.max_output_bytes.saturating_add(1);
    let stdout_reader = thread::spawn(move || read_limited(stdout, reader_limit));
    let stderr_reader = thread::spawn(move || read_limited(stderr, reader_limit));

    let started = Instant::now();
    let timeout = Duration::from_millis(invocation.limits.timeout_ms);
    let terminal_error = loop {
        if cancellation.is_cancelled() {
            break Some(InvocationError::Cancelled);
        }
        if started.elapsed() >= timeout {
            break Some(InvocationError::TimedOut);
        }
        match child.try_wait() {
            Ok(Some(_)) => break None,
            Ok(None) => thread::sleep(Duration::from_millis(5)),
            Err(error) => break Some(InvocationError::Io(error.to_string())),
        }
    };
    if terminal_error.is_some() {
        child
            .kill()
            .map_err(|error| InvocationError::Io(error.to_string()))?;
    }
    let status = child
        .wait()
        .map_err(|error| InvocationError::Io(error.to_string()))?;
    let _ = stdin_writer.join();
    let stdout = stdout_reader
        .join()
        .map_err(|_| InvocationError::OutputReaderFailed)?
        .map_err(|error| InvocationError::Io(error.to_string()))?;
    let stderr = stderr_reader
        .join()
        .map_err(|_| InvocationError::OutputReaderFailed)?
        .map_err(|error| InvocationError::Io(error.to_string()))?;
    if let Some(error) = terminal_error {
        return Err(error);
    }
    if stdout.len().saturating_add(stderr.len()) > invocation.limits.max_output_bytes {
        return Err(InvocationError::OutputTooLarge {
            limit: invocation.limits.max_output_bytes,
        });
    }
    Ok(InvocationOutput {
        status: status.code().unwrap_or(-1),
        stdout,
        stderr,
    })
}

/// This gate is read only from an operator-owned process environment. Adapter
/// output is never interpreted as an environment mutation.
#[must_use]
pub fn live_adapter_tests_enabled() -> bool {
    std::env::var("AO_NEXT_LIVE_ADAPTER_TESTS").as_deref() == Ok("operator-authorized")
}

fn read_limited(mut reader: impl Read, limit: usize) -> std::io::Result<Vec<u8>> {
    let mut bytes = Vec::new();
    reader
        .by_ref()
        .take(u64::try_from(limit).unwrap_or(u64::MAX))
        .read_to_end(&mut bytes)?;
    Ok(bytes)
}

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
    pub effect_observations: Vec<EffectObservation>,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct EffectObservation {
    pub effect_id: String,
    pub status: i32,
    pub stdout: Vec<u8>,
    pub stderr: Vec<u8>,
    pub output_digest: Digest,
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
