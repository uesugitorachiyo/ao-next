use std::path::Path;

use serde_json::Value;

use super::codex::{parse_contract, validate_identity, validate_inputs};
use super::{
    AdapterContractError, AdapterIdentity, AdapterTurn, CliContract, InvocationLimits,
    NormalizedAdapterTurn, PreparedInvocation,
};
use crate::strict_json::decode_strict_json;

const REQUIRED_FLAGS: [&str; 6] = [
    "--output-format",
    "--json-schema",
    "--tools",
    "--bare",
    "--no-session-persistence",
    "--strict-mcp-config",
];

/// Parses a sanitized `claude --help` capture and checks the flags on which the
/// adapter contract depends.
///
/// # Errors
///
/// Returns [`AdapterContractError`] for invalid UTF-8, absent version/usage, or
/// any missing required flag.
pub fn parse_cli_contract(bytes: &[u8]) -> Result<CliContract, AdapterContractError> {
    parse_contract("claude", bytes, "Usage: claude", &REQUIRED_FLAGS)
}

/// Prepares one non-interactive Claude invocation with all built-in tools,
/// agents, persistence, plugins, and MCP discovery disabled.
///
/// # Errors
///
/// Returns [`AdapterContractError`] for unsafe paths, invalid schema JSON,
/// empty identities, an unsupported effort, or oversized input.
pub fn prepare_invocation(
    model: &str,
    reasoning_effort: &str,
    workspace: &Path,
    output_schema: &[u8],
    prompt: &str,
    limits: InvocationLimits,
) -> Result<PreparedInvocation, AdapterContractError> {
    validate_inputs(model, reasoning_effort, workspace, prompt, limits)?;
    if output_schema.len() > limits.max_input_bytes {
        return Err(AdapterContractError::InvalidInvocation(
            "output schema exceeds input bound".into(),
        ));
    }
    let _: Value = decode_strict_json(output_schema, limits.max_input_bytes)
        .map_err(|error| AdapterContractError::InvalidInvocation(error.to_string()))?;
    let schema = std::str::from_utf8(output_schema)
        .map_err(|error| AdapterContractError::InvalidInvocation(error.to_string()))?;
    Ok(PreparedInvocation {
        program: "claude".into(),
        args: vec![
            "--print".into(),
            "--bare".into(),
            "--disable-slash-commands".into(),
            "--no-session-persistence".into(),
            "--strict-mcp-config".into(),
            "--mcp-config".into(),
            r#"{"mcpServers":{}}"#.into(),
            "--tools".into(),
            String::new(),
            "--permission-mode".into(),
            "dontAsk".into(),
            "--output-format".into(),
            "json".into(),
            "--json-schema".into(),
            schema.into(),
            "--model".into(),
            model.into(),
            "--effort".into(),
            reasoning_effort.into(),
        ],
        stdin: prompt.as_bytes().to_vec(),
        cwd: workspace.to_path_buf(),
        environment: None,
        limits,
    })
}

/// Normalizes a bounded Claude JSON result into one core adapter turn.
///
/// # Errors
///
/// Returns [`AdapterContractError`] for identity drift, oversized or malformed
/// output, an error result, or a missing structured output value.
pub fn normalize_output(
    identity: AdapterIdentity,
    bytes: &[u8],
    maximum_bytes: usize,
) -> Result<NormalizedAdapterTurn, AdapterContractError> {
    validate_identity(&identity, "claude")?;
    if bytes.len() > maximum_bytes {
        return Err(AdapterContractError::OutputTooLarge {
            limit: maximum_bytes,
        });
    }
    let value: Value = decode_strict_json(bytes, maximum_bytes)
        .map_err(|error| AdapterContractError::MalformedOutput(error.to_string()))?;
    if value.get("type").and_then(Value::as_str) != Some("result")
        || value.get("subtype").and_then(Value::as_str) != Some("success")
        || value.get("is_error").and_then(Value::as_bool) != Some(false)
    {
        return Err(AdapterContractError::MalformedOutput(
            "Claude result is not a successful terminal result".into(),
        ));
    }
    let structured = value
        .get("structured_output")
        .ok_or(AdapterContractError::MissingTurn)?;
    let bytes = serde_json::to_vec(structured)
        .map_err(|error| AdapterContractError::MalformedOutput(error.to_string()))?;
    let turn: AdapterTurn = decode_strict_json(&bytes, maximum_bytes)
        .map_err(|error| AdapterContractError::MalformedOutput(error.to_string()))?;
    Ok(NormalizedAdapterTurn { identity, turn })
}
