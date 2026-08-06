use std::collections::BTreeSet;
use std::path::Path;

use serde_json::Value;

use super::{
    AdapterContractError, AdapterIdentity, CliContract, InvocationLimits, NormalizedAdapterTurn,
    PreparedInvocation,
};
use crate::strict_json::decode_strict_json;

const REQUIRED_FLAGS: [&str; 5] = [
    "--json",
    "--output-schema",
    "--ephemeral",
    "--ignore-user-config",
    "--ignore-rules",
];

/// Parses a sanitized `codex exec --help` capture and checks the flags on which
/// the adapter contract depends.
///
/// # Errors
///
/// Returns [`AdapterContractError`] for invalid UTF-8, absent version/usage, or
/// any missing required flag.
pub fn parse_cli_contract(bytes: &[u8]) -> Result<CliContract, AdapterContractError> {
    parse_contract("codex", bytes, "Usage: codex exec", &REQUIRED_FLAGS)
}

/// Prepares one non-interactive Codex invocation. The prompt is passed only on
/// stdin and the CLI is constrained to an ephemeral, read-only session.
///
/// # Errors
///
/// Returns [`AdapterContractError`] for unsafe paths, empty identities, an
/// unsupported effort, or oversized input.
pub fn prepare_invocation(
    model: &str,
    reasoning_effort: &str,
    workspace: &Path,
    output_schema: &Path,
    prompt: &str,
    limits: InvocationLimits,
) -> Result<PreparedInvocation, AdapterContractError> {
    validate_inputs(model, reasoning_effort, workspace, prompt, limits)?;
    let schema_metadata = std::fs::symlink_metadata(output_schema)
        .map_err(|error| AdapterContractError::InvalidInvocation(error.to_string()))?;
    if schema_metadata.file_type().is_symlink() || !schema_metadata.is_file() {
        return Err(AdapterContractError::InvalidInvocation(
            "output schema must be a regular non-symlink file".into(),
        ));
    }
    Ok(PreparedInvocation {
        program: "codex".into(),
        args: vec![
            "exec".into(),
            "--json".into(),
            "--ephemeral".into(),
            "--ignore-user-config".into(),
            "--ignore-rules".into(),
            "--color".into(),
            "never".into(),
            "--sandbox".into(),
            "read-only".into(),
            "--ask-for-approval".into(),
            "never".into(),
            "--model".into(),
            model.into(),
            "-c".into(),
            format!("model_reasoning_effort=\"{reasoning_effort}\""),
            "--output-schema".into(),
            output_schema.display().to_string(),
            "-C".into(),
            workspace.display().to_string(),
            "-".into(),
        ],
        stdin: prompt.as_bytes().to_vec(),
        cwd: workspace.to_path_buf(),
        limits,
    })
}

/// Prepares the separately authorized native Codex baseline. Unlike the AO
/// adapter, this baseline permits only workspace-local writes through the
/// Codex sandbox and does not request structured AO actions.
///
/// # Errors
///
/// Returns [`AdapterContractError`] for unsafe paths, empty identities, an
/// unsupported effort, or oversized input.
pub fn prepare_direct_invocation(
    model: &str,
    reasoning_effort: &str,
    workspace: &Path,
    prompt: &str,
    limits: InvocationLimits,
) -> Result<PreparedInvocation, AdapterContractError> {
    validate_inputs(model, reasoning_effort, workspace, prompt, limits)?;
    Ok(PreparedInvocation {
        program: "codex".into(),
        args: vec![
            "exec".into(),
            "--json".into(),
            "--ephemeral".into(),
            "--ignore-user-config".into(),
            "--ignore-rules".into(),
            "--color".into(),
            "never".into(),
            "--sandbox".into(),
            "workspace-write".into(),
            "--ask-for-approval".into(),
            "never".into(),
            "--model".into(),
            model.into(),
            "-c".into(),
            format!("model_reasoning_effort=\"{reasoning_effort}\""),
            "-C".into(),
            workspace.display().to_string(),
            "-".into(),
        ],
        stdin: prompt.as_bytes().to_vec(),
        cwd: workspace.to_path_buf(),
        limits,
    })
}

/// Normalizes a bounded Codex JSONL event stream into one core adapter turn.
///
/// # Errors
///
/// Returns [`AdapterContractError`] for identity drift, oversized or malformed
/// JSONL, or an event stream without a structured final agent message.
pub fn normalize_output(
    identity: AdapterIdentity,
    bytes: &[u8],
    maximum_bytes: usize,
) -> Result<NormalizedAdapterTurn, AdapterContractError> {
    validate_identity(&identity, "codex")?;
    if bytes.len() > maximum_bytes {
        return Err(AdapterContractError::OutputTooLarge {
            limit: maximum_bytes,
        });
    }
    let mut turn = None;
    for line in bytes.split(|byte| *byte == b'\n') {
        if line.is_empty() {
            continue;
        }
        let event: Value = decode_strict_json(line, maximum_bytes)
            .map_err(|error| AdapterContractError::MalformedOutput(error.to_string()))?;
        if event.get("type").and_then(Value::as_str) != Some("item.completed")
            || event.pointer("/item/type").and_then(Value::as_str) != Some("agent_message")
        {
            continue;
        }
        let text = event
            .pointer("/item/text")
            .and_then(Value::as_str)
            .ok_or_else(|| {
                AdapterContractError::MalformedOutput("agent message has no text".into())
            })?;
        turn = Some(
            decode_strict_json(text.as_bytes(), maximum_bytes)
                .map_err(|error| AdapterContractError::MalformedOutput(error.to_string()))?,
        );
    }
    Ok(NormalizedAdapterTurn {
        identity,
        turn: turn.ok_or(AdapterContractError::MissingTurn)?,
    })
}

pub(super) fn parse_contract(
    runtime: &str,
    bytes: &[u8],
    usage_prefix: &str,
    flags: &[&str],
) -> Result<CliContract, AdapterContractError> {
    let text =
        std::str::from_utf8(bytes).map_err(|_| AdapterContractError::MalformedCliContract)?;
    let mut lines = text.lines().filter(|line| !line.trim().is_empty());
    let version = lines
        .next()
        .ok_or(AdapterContractError::MalformedCliContract)?
        .trim()
        .to_owned();
    if version.is_empty() || !text.contains(usage_prefix) {
        return Err(AdapterContractError::MalformedCliContract);
    }
    let mut required_flags = BTreeSet::new();
    for flag in flags {
        if !text.contains(flag) {
            return Err(AdapterContractError::MissingCliFlag((*flag).into()));
        }
        required_flags.insert((*flag).to_owned());
    }
    Ok(CliContract {
        runtime: runtime.into(),
        version,
        required_flags,
    })
}

pub(super) fn validate_inputs(
    model: &str,
    reasoning_effort: &str,
    workspace: &Path,
    prompt: &str,
    limits: InvocationLimits,
) -> Result<(), AdapterContractError> {
    if model.trim().is_empty()
        || !matches!(
            reasoning_effort,
            "low" | "medium" | "high" | "xhigh" | "max"
        )
        || prompt.len() > limits.max_input_bytes
        || limits.timeout_ms == 0
        || limits.max_output_bytes == 0
    {
        return Err(AdapterContractError::InvalidInvocation(
            "model, effort, prompt, or invocation limits are invalid".into(),
        ));
    }
    let metadata = std::fs::symlink_metadata(workspace)
        .map_err(|error| AdapterContractError::InvalidInvocation(error.to_string()))?;
    if metadata.file_type().is_symlink() || !metadata.is_dir() {
        return Err(AdapterContractError::InvalidInvocation(
            "workspace must be a regular non-symlink directory".into(),
        ));
    }
    Ok(())
}

pub(super) fn validate_identity(
    identity: &AdapterIdentity,
    runtime: &'static str,
) -> Result<(), AdapterContractError> {
    if identity.runtime != runtime
        || identity.model_identifier.trim().is_empty()
        || identity.adapter_version.trim().is_empty()
        || identity.worker_id.trim().is_empty()
    {
        return Err(AdapterContractError::IdentityMismatch(runtime));
    }
    Ok(())
}
