use std::io::Read;
use std::process::{Command, Stdio};
use std::thread;
use std::time::{Duration, Instant};

use thiserror::Error;

use crate::contracts::{AuthorityEnvelope, EffectKind, EffectRequest};
use crate::policy::{PolicyDenial, validate_effect_request};

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AuthorizedEffect {
    request: EffectRequest,
}

impl AuthorizedEffect {
    #[must_use]
    pub fn request(&self) -> &EffectRequest {
        &self.request
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct EffectOutput {
    pub status: i32,
    pub stdout: Vec<u8>,
    pub stderr: Vec<u8>,
}

#[derive(Debug, Error)]
pub enum EffectBrokerError {
    #[error("effect denied: {0}")]
    Denied(#[from] PolicyDenial),
    #[error("effect I/O failed: {0}")]
    Io(#[from] std::io::Error),
    #[error("effect exceeded its admitted timeout")]
    TimedOut,
    #[error("effect output exceeded {limit} bytes")]
    OutputTooLarge { limit: usize },
    #[error("the admitted effect kind has no local executor: {0:?}")]
    UnsupportedEffect(EffectKind),
    #[error("effect output reader failed")]
    OutputReaderFailed,
}

pub trait EffectBroker {
    /// Admits a request only when deterministic policy accepts every binding.
    ///
    /// # Errors
    ///
    /// Returns [`PolicyDenial`] when any capability, program, path, timeout, or
    /// external-effect rule fails.
    fn authorize(
        &self,
        request: &EffectRequest,
        authority: &AuthorityEnvelope,
    ) -> Result<AuthorizedEffect, PolicyDenial>;
}

#[derive(Clone, Debug)]
pub struct LocalEffectBroker {
    maximum_timeout_ms: u64,
    maximum_output_bytes: usize,
}

impl LocalEffectBroker {
    #[must_use]
    pub const fn new(maximum_timeout_ms: u64, maximum_output_bytes: usize) -> Self {
        Self {
            maximum_timeout_ms,
            maximum_output_bytes,
        }
    }

    /// Authorizes and executes one structured local effect without a shell.
    ///
    /// # Errors
    ///
    /// Returns [`EffectBrokerError`] when admission fails, the process cannot be
    /// started, its timeout or output bound is exceeded, or no local executor
    /// exists for the admitted effect kind.
    pub fn execute(
        &self,
        request: &EffectRequest,
        authority: &AuthorityEnvelope,
    ) -> Result<EffectOutput, EffectBrokerError> {
        let authorized = self.authorize(request, authority)?;
        self.execute_authorized(&authorized)
    }

    fn execute_authorized(
        &self,
        authorized: &AuthorizedEffect,
    ) -> Result<EffectOutput, EffectBrokerError> {
        let request = authorized.request();
        if request.kind != EffectKind::RunProgram {
            return Err(EffectBrokerError::UnsupportedEffect(request.kind.clone()));
        }
        let program = request
            .program
            .as_deref()
            .ok_or(EffectBrokerError::Denied(PolicyDenial::MissingProgram))?;
        let mut child = Command::new(program)
            .args(&request.args)
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()?;

        let stdout = child
            .stdout
            .take()
            .ok_or(EffectBrokerError::OutputReaderFailed)?;
        let stderr = child
            .stderr
            .take()
            .ok_or(EffectBrokerError::OutputReaderFailed)?;
        let output_limit = self.maximum_output_bytes.saturating_add(1);
        let stdout_reader = thread::spawn(move || read_limited(stdout, output_limit));
        let stderr_reader = thread::spawn(move || read_limited(stderr, output_limit));
        let started = Instant::now();
        let timeout = Duration::from_millis(request.timeout_ms);

        let status = loop {
            if let Some(status) = child.try_wait()? {
                break status;
            }
            if started.elapsed() >= timeout {
                child.kill()?;
                child.wait()?;
                stdout_reader
                    .join()
                    .map_err(|_| EffectBrokerError::OutputReaderFailed)??;
                stderr_reader
                    .join()
                    .map_err(|_| EffectBrokerError::OutputReaderFailed)??;
                return Err(EffectBrokerError::TimedOut);
            }
            thread::sleep(Duration::from_millis(5));
        };

        let stdout = stdout_reader
            .join()
            .map_err(|_| EffectBrokerError::OutputReaderFailed)??;
        let stderr = stderr_reader
            .join()
            .map_err(|_| EffectBrokerError::OutputReaderFailed)??;
        if stdout.len().saturating_add(stderr.len()) > self.maximum_output_bytes {
            return Err(EffectBrokerError::OutputTooLarge {
                limit: self.maximum_output_bytes,
            });
        }
        Ok(EffectOutput {
            status: status.code().unwrap_or(-1),
            stdout,
            stderr,
        })
    }
}

impl EffectBroker for LocalEffectBroker {
    fn authorize(
        &self,
        request: &EffectRequest,
        authority: &AuthorityEnvelope,
    ) -> Result<AuthorizedEffect, PolicyDenial> {
        validate_effect_request(request, authority, self.maximum_timeout_ms)?;
        Ok(AuthorizedEffect {
            request: request.clone(),
        })
    }
}

fn read_limited(reader: impl Read, limit: usize) -> Result<Vec<u8>, std::io::Error> {
    let mut bytes = Vec::new();
    reader.take(limit as u64).read_to_end(&mut bytes)?;
    Ok(bytes)
}
