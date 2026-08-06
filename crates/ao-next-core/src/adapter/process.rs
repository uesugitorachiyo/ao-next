use std::path::{Path, PathBuf};
use std::time::Instant;

use serde::Serialize;
use serde_json::Value;

use super::{
    AdapterError, AdapterIdentity, AdapterTurn, CancellationToken, EffectObservation,
    InvocationError, InvocationLimits, InvocationOutput, PreparedInvocation, RuntimeAdapter,
    TokenUsage, TurnContext, claude, codex, execute_bounded,
};
use crate::contracts::{Digest, RunRequest, SourceIdentity, WorkspaceIdentity};
use crate::evidence::digest_bytes;
use crate::strict_json::{canonical_digest, decode_strict_json};

pub trait ProcessRunner {
    /// Executes one prepared invocation without changing its program, arguments,
    /// working directory, input, or bounds.
    ///
    /// # Errors
    ///
    /// Returns an invocation error when the bounded process cannot complete.
    fn run(
        &mut self,
        invocation: &PreparedInvocation,
        cancellation: &CancellationToken,
    ) -> Result<InvocationOutput, InvocationError>;
}

#[derive(Clone, Copy, Debug, Default)]
pub struct BoundedProcessRunner;

impl ProcessRunner for BoundedProcessRunner {
    fn run(
        &mut self,
        invocation: &PreparedInvocation,
        cancellation: &CancellationToken,
    ) -> Result<InvocationOutput, InvocationError> {
        execute_bounded(invocation, cancellation)
    }
}

#[derive(Clone, Debug)]
pub struct ProcessAdapterConfig {
    objective: String,
    run_id: String,
    source: SourceIdentity,
    workspace: WorkspaceIdentity,
    authority_digest: Digest,
    policy_digest: Digest,
    verifier_profile_digest: Digest,
    identity: AdapterIdentity,
    reasoning_effort: String,
    output_schema_path: PathBuf,
    output_schema: Vec<u8>,
    limits: InvocationLimits,
    cancellation: CancellationToken,
}

impl ProcessAdapterConfig {
    /// Captures the immutable request bindings used by every provider turn.
    ///
    /// # Errors
    ///
    /// Returns an adapter error when the runtime, worker, schema, limits, or
    /// request-bound paths are unsafe.
    pub fn from_request(
        request: &RunRequest,
        worker_id: impl Into<String>,
        output_schema_path: &Path,
        limits: InvocationLimits,
        cancellation: CancellationToken,
    ) -> Result<Self, AdapterError> {
        let worker_id = worker_id.into();
        if request.objective.trim().is_empty()
            || worker_id.trim().is_empty()
            || !matches!(request.model_profile.runtime.as_str(), "codex" | "claude")
            || limits.max_input_bytes
                > usize::try_from(request.limits.max_input_bytes).unwrap_or(usize::MAX)
            || limits.max_output_bytes
                > usize::try_from(request.limits.max_output_bytes).unwrap_or(usize::MAX)
            || limits.timeout_ms == 0
            || limits.timeout_ms > request.limits.max_run_ms
        {
            return Err(AdapterError::Runtime(
                "process adapter configuration is not request-bound".into(),
            ));
        }
        let schema_metadata = std::fs::symlink_metadata(output_schema_path)
            .map_err(|error| AdapterError::Runtime(error.to_string()))?;
        if schema_metadata.file_type().is_symlink()
            || !schema_metadata.is_file()
            || schema_metadata.len() > u64::try_from(limits.max_input_bytes).unwrap_or(u64::MAX)
        {
            return Err(AdapterError::Runtime(
                "output schema must be a bounded regular non-symlink file".into(),
            ));
        }
        let canonical_schema = std::fs::canonicalize(output_schema_path)
            .map_err(|error| AdapterError::Runtime(error.to_string()))?;
        let mut schema_allowed = false;
        for root in &request.authority.allowed_roots {
            let metadata = std::fs::symlink_metadata(root)
                .map_err(|error| AdapterError::Runtime(error.to_string()))?;
            if metadata.file_type().is_symlink() || !metadata.is_dir() {
                return Err(AdapterError::Runtime(
                    "authority root is not a regular non-symlink directory".into(),
                ));
            }
            let canonical_root = std::fs::canonicalize(root)
                .map_err(|error| AdapterError::Runtime(error.to_string()))?;
            if canonical_schema.starts_with(canonical_root) {
                schema_allowed = true;
                break;
            }
        }
        if !schema_allowed {
            return Err(AdapterError::Runtime(
                "output schema is outside the request authority roots".into(),
            ));
        }
        let output_schema = std::fs::read(&canonical_schema)
            .map_err(|error| AdapterError::Runtime(error.to_string()))?;
        decode_strict_json::<Value>(&output_schema, limits.max_input_bytes)
            .map_err(|error| AdapterError::Runtime(error.to_string()))?;
        let authority_digest = canonical_digest(&request.authority)
            .map_err(|error| AdapterError::Runtime(error.to_string()))?;
        Ok(Self {
            objective: request.objective.clone(),
            run_id: request.run_id.clone(),
            source: request.source.clone(),
            workspace: request.workspace.clone(),
            authority_digest,
            policy_digest: request.policy_digest.clone(),
            verifier_profile_digest: request.verifier_profile.profile_digest.clone(),
            identity: AdapterIdentity {
                runtime: request.model_profile.runtime.clone(),
                model_identifier: request.model_profile.model_identifier.clone(),
                adapter_version: request.model_profile.adapter_version.clone(),
                worker_id,
            },
            reasoning_effort: request.model_profile.reasoning_effort.clone(),
            output_schema_path: canonical_schema,
            output_schema,
            limits,
            cancellation,
        })
    }
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct RuntimeCapture {
    pub turn_index: u32,
    pub raw_capture_digest: Digest,
    pub usage: TokenUsage,
    pub model_wait_ms: u64,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct RuntimeEnvelopeCapture {
    pub raw_capture_digest: Digest,
    pub usage: TokenUsage,
}

/// Extracts trusted usage and binds the complete bounded process capture.
/// Model-authored text is not considered usage evidence.
///
/// # Errors
///
/// Returns an adapter error for nonzero status, overflow, malformed runtime
/// envelopes, or missing trusted usage.
pub fn capture_runtime_output(
    runtime: &str,
    output: &InvocationOutput,
    maximum_bytes: usize,
) -> Result<RuntimeEnvelopeCapture, AdapterError> {
    if output.status != 0 {
        return Err(AdapterError::Runtime(format!(
            "adapter process exited {}",
            output.status
        )));
    }
    if output.stdout.len().saturating_add(output.stderr.len()) > maximum_bytes {
        return Err(AdapterError::Runtime(
            "adapter process output exceeded its bound".into(),
        ));
    }
    let usage = match runtime {
        "codex" => codex_usage(&output.stdout, maximum_bytes),
        "claude" => claude_usage(&output.stdout, maximum_bytes),
        _ => Err(AdapterError::Runtime("unsupported runtime identity".into())),
    }?;
    let usage = TokenUsage {
        output_bytes: u64::try_from(output.stdout.len()).unwrap_or(u64::MAX),
        ..usage
    };
    let raw_capture_digest = canonical_digest(&(output.status, &output.stdout, &output.stderr))
        .map_err(|error| AdapterError::Runtime(error.to_string()))?;
    Ok(RuntimeEnvelopeCapture {
        raw_capture_digest,
        usage,
    })
}

pub struct ProcessRuntimeAdapter<R> {
    config: ProcessAdapterConfig,
    runner: R,
    captures: Vec<RuntimeCapture>,
}

impl<R> ProcessRuntimeAdapter<R> {
    #[must_use]
    pub const fn new(config: ProcessAdapterConfig, runner: R) -> Self {
        Self {
            config,
            runner,
            captures: Vec::new(),
        }
    }

    #[must_use]
    pub fn captures(&self) -> &[RuntimeCapture] {
        &self.captures
    }
}

impl<R: ProcessRunner> RuntimeAdapter for ProcessRuntimeAdapter<R> {
    fn identity(&self) -> AdapterIdentity {
        self.config.identity.clone()
    }

    fn execute_turn(&mut self, context: &TurnContext) -> Result<AdapterTurn, AdapterError> {
        self.validate_context(context)?;
        if self.config.cancellation.is_cancelled() {
            return Err(AdapterError::Runtime(
                "adapter invocation was cancelled".into(),
            ));
        }
        let prompt = build_prompt(&self.config, context)?;
        let current_schema = std::fs::read(&self.config.output_schema_path)
            .map_err(|error| AdapterError::Runtime(error.to_string()))?;
        if current_schema != self.config.output_schema {
            return Err(AdapterError::Runtime("output schema drifted".into()));
        }
        let invocation = match self.config.identity.runtime.as_str() {
            "codex" => codex::prepare_invocation(
                &self.config.identity.model_identifier,
                &self.config.reasoning_effort,
                &self.config.workspace.root,
                &self.config.output_schema_path,
                &prompt,
                self.config.limits,
            ),
            "claude" => claude::prepare_invocation(
                &self.config.identity.model_identifier,
                &self.config.reasoning_effort,
                &self.config.workspace.root,
                &self.config.output_schema,
                &prompt,
                self.config.limits,
            ),
            _ => unreachable!("validated runtime"),
        }
        .map_err(|error| AdapterError::Runtime(error.to_string()))?;
        let started = Instant::now();
        let output = self
            .runner
            .run(&invocation, &self.config.cancellation)
            .map_err(|error| AdapterError::Runtime(error.to_string()))?;
        let model_wait_ms = u64::try_from(started.elapsed().as_millis()).unwrap_or(u64::MAX);
        let capture = capture_runtime_output(
            &self.config.identity.runtime,
            &output,
            self.config.limits.max_output_bytes,
        )?;
        let normalized = match self.config.identity.runtime.as_str() {
            "codex" => codex::normalize_output(
                self.config.identity.clone(),
                &output.stdout,
                self.config.limits.max_output_bytes,
            ),
            "claude" => claude::normalize_output(
                self.config.identity.clone(),
                &output.stdout,
                self.config.limits.max_output_bytes,
            ),
            _ => unreachable!("validated runtime"),
        }
        .map_err(|error| AdapterError::Runtime(error.to_string()))?;
        if normalized.identity != self.config.identity {
            return Err(AdapterError::Runtime("runtime identity drifted".into()));
        }
        let mut turn = normalized.turn;
        turn.usage = capture.usage.clone();
        self.captures.push(RuntimeCapture {
            turn_index: context.turn_index,
            raw_capture_digest: capture.raw_capture_digest,
            usage: capture.usage,
            model_wait_ms,
        });
        Ok(turn)
    }
}

impl<R> ProcessRuntimeAdapter<R> {
    fn validate_context(&self, context: &TurnContext) -> Result<(), AdapterError> {
        if context.run_id != self.config.run_id
            || context.source != self.config.source
            || context.workspace != self.config.workspace
            || context.authority_digest != self.config.authority_digest
            || context.policy_digest != self.config.policy_digest
            || context.verifier_profile_digest != self.config.verifier_profile_digest
            || context.turn_index != u32::try_from(self.captures.len()).unwrap_or(u32::MAX)
        {
            return Err(AdapterError::Runtime(
                "runtime turn context drifted from immutable request bindings".into(),
            ));
        }
        Ok(())
    }
}

#[derive(Serialize)]
struct ProviderTurnPrompt<'a> {
    schema_version: &'static str,
    objective: &'a str,
    run_id: &'a str,
    adapter_identity: &'a AdapterIdentity,
    reasoning_effort: &'a str,
    source: &'a SourceIdentity,
    workspace: &'a WorkspaceIdentity,
    authority_digest: &'a Digest,
    policy_digest: &'a Digest,
    verifier_profile_digest: &'a Digest,
    output_schema_digest: Digest,
    allowed_actions: [&'static str; 4],
    turn_index: u32,
    repair_attempt: u32,
    effect_observations: &'a [EffectObservation],
}

fn build_prompt(
    config: &ProcessAdapterConfig,
    context: &TurnContext,
) -> Result<String, AdapterError> {
    let prompt = ProviderTurnPrompt {
        schema_version: "ao.next.provider-turn-prompt.v1",
        objective: &config.objective,
        run_id: &config.run_id,
        adapter_identity: &config.identity,
        reasoning_effort: &config.reasoning_effort,
        source: &config.source,
        workspace: &config.workspace,
        authority_digest: &config.authority_digest,
        policy_digest: &config.policy_digest,
        verifier_profile_digest: &config.verifier_profile_digest,
        output_schema_digest: digest_bytes(&config.output_schema),
        allowed_actions: ["effect", "verify", "blocked", "interrupt"],
        turn_index: context.turn_index,
        repair_attempt: context.repair_attempt,
        effect_observations: &context.effect_observations,
    };
    serde_json::to_string(&prompt).map_err(|error| AdapterError::Runtime(error.to_string()))
}

fn codex_usage(bytes: &[u8], maximum_bytes: usize) -> Result<TokenUsage, AdapterError> {
    let mut usage = None;
    for line in bytes
        .split(|byte| *byte == b'\n')
        .filter(|line| !line.is_empty())
    {
        let event: Value = decode_strict_json(line, maximum_bytes)
            .map_err(|error| AdapterError::Runtime(error.to_string()))?;
        if event.get("type").and_then(Value::as_str) == Some("turn.completed") {
            if usage.is_some() {
                return Err(AdapterError::Runtime(
                    "Codex output duplicated terminal usage".into(),
                ));
            }
            usage = Some(read_usage(
                event
                    .get("usage")
                    .ok_or_else(|| AdapterError::Runtime("Codex usage is missing".into()))?,
                "cached_input_tokens",
            )?);
        }
    }
    usage.ok_or_else(|| AdapterError::Runtime("Codex trusted usage is missing".into()))
}

fn claude_usage(bytes: &[u8], maximum_bytes: usize) -> Result<TokenUsage, AdapterError> {
    let result: Value = decode_strict_json(bytes, maximum_bytes)
        .map_err(|error| AdapterError::Runtime(error.to_string()))?;
    let usage = result
        .get("usage")
        .ok_or_else(|| AdapterError::Runtime("Claude trusted usage is missing".into()))?;
    let mut normalized = read_usage(usage, "cache_read_input_tokens")?;
    normalized.cached_input_tokens = normalized
        .cached_input_tokens
        .checked_add(
            usage
                .get("cache_creation_input_tokens")
                .map_or(Ok(0), required_u64)?,
        )
        .ok_or_else(|| AdapterError::Runtime("Claude cached token usage overflowed".into()))?;
    Ok(normalized)
}

fn read_usage(value: &Value, cached_field: &str) -> Result<TokenUsage, AdapterError> {
    Ok(TokenUsage {
        input_tokens: required_field(value, "input_tokens")?,
        cached_input_tokens: required_field(value, cached_field)?,
        reasoning_tokens: value.get("reasoning_tokens").map_or(Ok(0), required_u64)?,
        output_tokens: required_field(value, "output_tokens")?,
        output_bytes: 0,
    })
}

fn required_field(value: &Value, field: &str) -> Result<u64, AdapterError> {
    required_u64(
        value.get(field).ok_or_else(|| {
            AdapterError::Runtime(format!("trusted usage field {field} is missing"))
        })?,
    )
}

fn required_u64(value: &Value) -> Result<u64, AdapterError> {
    value
        .as_u64()
        .ok_or_else(|| AdapterError::Runtime("trusted usage counter is not a u64".into()))
}
