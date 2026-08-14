use std::collections::BTreeSet;
use std::fs::OpenOptions;
use std::io::Write;
use std::path::{Path, PathBuf};

use serde_json::Value;

use super::{
    AdapterContractError, AdapterIdentity, CliContract, InvocationLimits, NormalizedAdapterTurn,
    PreparedInvocation,
};
use crate::evidence::digest_bytes;
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
    let provider_schema = prepare_provider_schema(output_schema, limits.max_input_bytes)?;
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
            "-c".into(),
            "approval_policy=\"never\"".into(),
            "-c".into(),
            "features.shell_tool=false".into(),
            "-c".into(),
            "features.unified_exec=false".into(),
            "-c".into(),
            "features.js_repl=false".into(),
            "-c".into(),
            "features.code_mode=false".into(),
            "-c".into(),
            "features.code_mode_host=false".into(),
            "-c".into(),
            "features.deferred_executor=false".into(),
            "-c".into(),
            "features.executor_capability_discovery=false".into(),
            "-c".into(),
            "features.apply_patch_freeform=false".into(),
            "-c".into(),
            "features.apply_patch_streaming_events=false".into(),
            "-c".into(),
            "features.web_search_request=false".into(),
            "-c".into(),
            "features.standalone_web_search=false".into(),
            "-c".into(),
            "features.browser_use=false".into(),
            "-c".into(),
            "features.browser_use_external=false".into(),
            "-c".into(),
            "features.browser_use_full_cdp_access=false".into(),
            "-c".into(),
            "features.in_app_browser=false".into(),
            "-c".into(),
            "features.computer_use=false".into(),
            "-c".into(),
            "features.image_generation=false".into(),
            "-c".into(),
            "features.view_image=false".into(),
            "-c".into(),
            "features.apps=false".into(),
            "-c".into(),
            "features.plugins=false".into(),
            "-c".into(),
            "features.remote_plugin=false".into(),
            "-c".into(),
            "features.multi_agent=false".into(),
            "-c".into(),
            "features.skill_search=false".into(),
            "-c".into(),
            "features.workspace_dependencies=false".into(),
            "-c".into(),
            "features.tool_suggest=false".into(),
            "-c".into(),
            "tools.web_search=false".into(),
            "-c".into(),
            "tools.experimental_request_user_input={enabled=false}".into(),
            "-c".into(),
            "tools.update_plan={enabled=false}".into(),
            "--model".into(),
            model.into(),
            "-c".into(),
            format!("model_reasoning_effort=\"{reasoning_effort}\""),
            "--output-schema".into(),
            provider_schema.display().to_string(),
            "-C".into(),
            workspace.display().to_string(),
            "-".into(),
        ],
        stdin: prompt.as_bytes().to_vec(),
        cwd: workspace.to_path_buf(),
        environment: None,
        limits,
    })
}

fn prepare_provider_schema(
    output_schema: &Path,
    maximum_bytes: usize,
) -> Result<PathBuf, AdapterContractError> {
    let metadata = std::fs::symlink_metadata(output_schema)
        .map_err(|error| AdapterContractError::InvalidInvocation(error.to_string()))?;
    if metadata.file_type().is_symlink()
        || !metadata.is_file()
        || metadata.len() > u64::try_from(maximum_bytes).unwrap_or(u64::MAX)
    {
        return Err(AdapterContractError::InvalidInvocation(
            "output schema must be a bounded regular non-symlink file".into(),
        ));
    }
    let source_path = std::fs::canonicalize(output_schema)
        .map_err(|error| AdapterContractError::InvalidInvocation(error.to_string()))?;
    let source = std::fs::read(&source_path)
        .map_err(|error| AdapterContractError::InvalidInvocation(error.to_string()))?;
    let mut schema: Value = decode_strict_json(&source, maximum_bytes)
        .map_err(|error| AdapterContractError::InvalidInvocation(error.to_string()))?;
    if !project_codex_schema(&mut schema)? {
        return Ok(source_path);
    }
    let projected = serde_json::to_vec(&schema)
        .map_err(|error| AdapterContractError::InvalidInvocation(error.to_string()))?;
    if projected.len() > maximum_bytes {
        return Err(AdapterContractError::InvalidInvocation(
            "projected output schema exceeds input bound".into(),
        ));
    }
    let file_name = source_path
        .file_name()
        .and_then(|name| name.to_str())
        .ok_or_else(|| {
            AdapterContractError::InvalidInvocation("output schema has an unsafe name".into())
        })?;
    let digest = digest_bytes(&projected);
    let projected_path =
        source_path.with_file_name(format!(".{file_name}.codex-{}.json", &digest.as_str()[7..]));
    write_projected_schema(&projected_path, &projected)?;
    Ok(projected_path)
}

fn project_codex_schema(schema: &mut Value) -> Result<bool, AdapterContractError> {
    match schema {
        Value::Array(values) => values.iter_mut().try_fold(false, |changed, value| {
            Ok(project_codex_schema(value)? || changed)
        }),
        Value::Object(object) => {
            let mut changed = false;
            if let Some(one_of) = object.remove("oneOf") {
                if object.contains_key("anyOf") {
                    return Err(AdapterContractError::InvalidInvocation(
                        "output schema combines oneOf and anyOf".into(),
                    ));
                }
                object.insert("anyOf".into(), one_of);
                changed = true;
            }
            if let Some(required) =
                object
                    .get("properties")
                    .and_then(Value::as_object)
                    .map(|properties| {
                        Value::Array(properties.keys().cloned().map(Value::String).collect())
                    })
                && object.get("required") != Some(&required)
            {
                object.insert("required".into(), required);
                changed = true;
            }
            for value in object.values_mut() {
                changed = project_codex_schema(value)? || changed;
            }
            Ok(changed)
        }
        _ => Ok(false),
    }
}

fn write_projected_schema(path: &Path, expected: &[u8]) -> Result<(), AdapterContractError> {
    let mut options = OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    match options.open(path) {
        Ok(mut file) => {
            file.write_all(expected)
                .and_then(|()| file.sync_all())
                .map_err(|error| AdapterContractError::InvalidInvocation(error.to_string()))?;
        }
        Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => {
            let metadata = std::fs::symlink_metadata(path)
                .map_err(|error| AdapterContractError::InvalidInvocation(error.to_string()))?;
            let contents = std::fs::read(path)
                .map_err(|error| AdapterContractError::InvalidInvocation(error.to_string()))?;
            if metadata.file_type().is_symlink() || !metadata.is_file() || contents != expected {
                return Err(AdapterContractError::InvalidInvocation(
                    "projected output schema drifted".into(),
                ));
            }
        }
        Err(error) => {
            return Err(AdapterContractError::InvalidInvocation(error.to_string()));
        }
    }
    Ok(())
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
            "-c".into(),
            "approval_policy=\"never\"".into(),
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
        environment: None,
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
        match event.get("type").and_then(Value::as_str) {
            Some("thread.started" | "turn.started" | "turn.completed") => {}
            Some("item.completed")
                if event.pointer("/item/type").and_then(Value::as_str) == Some("agent_message") =>
            {
                if turn.is_some() {
                    return Err(AdapterContractError::MalformedOutput(
                        "Codex returned multiple agent messages".into(),
                    ));
                }
                let text = event
                    .pointer("/item/text")
                    .and_then(Value::as_str)
                    .ok_or_else(|| {
                        AdapterContractError::MalformedOutput("agent message has no text".into())
                    })?;
                turn = Some(
                    decode_strict_json(text.as_bytes(), maximum_bytes).map_err(|error| {
                        AdapterContractError::MalformedOutput(error.to_string())
                    })?,
                );
            }
            Some("item.completed")
                if event.pointer("/item/type").and_then(Value::as_str) == Some("error") =>
            {
                let item = event
                    .get("item")
                    .and_then(Value::as_object)
                    .ok_or_else(|| {
                        AdapterContractError::MalformedOutput("diagnostic item is malformed".into())
                    })?;
                if item.len() != 3
                    || item.get("id").and_then(Value::as_str).is_none()
                    || item.get("message").and_then(Value::as_str).is_none()
                {
                    return Err(AdapterContractError::MalformedOutput(
                        "diagnostic item is malformed".into(),
                    ));
                }
            }
            _ => {
                return Err(AdapterContractError::MalformedOutput(
                    "Codex emitted unmediated or unknown activity".into(),
                ));
            }
        }
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
