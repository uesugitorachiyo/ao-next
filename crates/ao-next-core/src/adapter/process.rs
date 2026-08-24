use std::path::{Component, Path, PathBuf};
use std::time::Instant;

#[cfg(windows)]
use std::fs::{File, OpenOptions};
#[cfg(windows)]
use std::io::{Read as _, Take};
#[cfg(windows)]
use std::os::windows::fs::{MetadataExt as _, OpenOptionsExt as _};

use serde::Serialize;
use serde_json::Value;

use super::{
    AdapterError, AdapterIdentity, AdapterTurn, CancellationToken, EffectObservation,
    InvocationError, InvocationLimits, InvocationOutput, PreparedInvocation, RuntimeAdapter,
    TokenUsage, TurnContext, claude, codex, execute_bounded,
};
use crate::contracts::{
    Capability, Digest, ExternalEffectPolicy, NetworkPolicy, RunRequest, SourceIdentity,
    WorkspaceIdentity,
};
use crate::engine::MAX_EFFECTS_PER_TURN;
use crate::evidence::digest_bytes;
use crate::strict_json::{canonical_digest, decode_strict_json};

#[cfg(windows)]
const FILE_FLAG_BACKUP_SEMANTICS: u32 = 0x0200_0000;
#[cfg(windows)]
const FILE_FLAG_OPEN_REPARSE_POINT: u32 = 0x0020_0000;
#[cfg(windows)]
const FILE_SHARE_READ: u32 = 0x1;
#[cfg(windows)]
const FILE_SHARE_WRITE: u32 = 0x2;

#[cfg(any(test, windows))]
const fn windows_reparse_point(attributes: u32) -> bool {
    attributes & 0x400 != 0
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct ProviderVisibleFile {
    pub path: String,
    pub content: String,
    pub digest: Digest,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ProviderVisibility {
    source_files: Vec<ProviderVisibleFile>,
    visible_fixtures: Vec<ProviderVisibleFile>,
    projection_digest: Digest,
}

impl ProviderVisibility {
    /// Builds an independently digest-bound live provider projection.
    ///
    /// # Errors
    ///
    /// Returns an adapter error for path, UTF-8, byte-limit, or snapshot drift.
    pub fn from_live_roots(
        workspace_root: &Path,
        workspace_seed_digest: &Digest,
        visible_fixture_root: &Path,
        visible_fixture_digest: &Digest,
        maximum_bytes: usize,
    ) -> Result<Self, AdapterError> {
        let (source_files, source_identity) = visible_files(workspace_root, maximum_bytes, true)?;
        let (visible_fixtures, fixture_identity) =
            visible_files(visible_fixture_root, maximum_bytes, false)?;
        if &canonical_digest(&source_identity)
            .map_err(|error| AdapterError::Runtime(error.to_string()))?
            != workspace_seed_digest
            || &canonical_digest(&fixture_identity)
                .map_err(|error| AdapterError::Runtime(error.to_string()))?
                != visible_fixture_digest
        {
            return Err(AdapterError::Runtime(
                "provider-visible source or fixture projection drifted".into(),
            ));
        }
        Self::new(source_files, visible_fixtures)
    }

    fn from_workspace(root: &Path, maximum_bytes: usize) -> Result<Self, AdapterError> {
        let (source_files, _) = visible_files(root, maximum_bytes, true)?;
        Self::new(source_files, Vec::new())
    }

    fn new(
        source_files: Vec<ProviderVisibleFile>,
        visible_fixtures: Vec<ProviderVisibleFile>,
    ) -> Result<Self, AdapterError> {
        let projection_digest = canonical_digest(&(&source_files, &visible_fixtures))
            .map_err(|error| AdapterError::Runtime(error.to_string()))?;
        Ok(Self {
            source_files,
            visible_fixtures,
            projection_digest,
        })
    }
}

#[derive(Serialize)]
struct VisibleFileIdentity {
    path: PathBuf,
    sha256: Digest,
    size_bytes: u64,
}

struct CapturedVisibleFile {
    relative: PathBuf,
    text: String,
    bytes: Vec<u8>,
    digest: Digest,
}

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
    authority: ProviderAuthority,
    visibility: ProviderVisibility,
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
        let visibility =
            ProviderVisibility::from_workspace(&request.workspace.root, limits.max_input_bytes)?;
        Self::from_request_with_visibility(
            request,
            worker_id,
            output_schema_path,
            visibility,
            limits,
            cancellation,
        )
    }

    /// Captures a prevalidated provider-visible source and fixture projection.
    ///
    /// # Errors
    ///
    /// Returns an adapter error when any request, schema, authority, or limit
    /// binding is unsafe.
    pub fn from_request_with_visibility(
        request: &RunRequest,
        worker_id: impl Into<String>,
        output_schema_path: &Path,
        visibility: ProviderVisibility,
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
        let supported_capabilities = request.authority.capabilities.iter().all(|capability| {
            matches!(
                capability,
                Capability::ReadWorkspace | Capability::WriteWorkspace
            )
        });
        if request.authority.allowed_roots != [request.workspace.root.clone()]
            || !supported_capabilities
            || request
                .authority
                .capabilities
                .contains(&Capability::RunLocalProgram)
            || !request.authority.allowed_programs.is_empty()
            || request.authority.network != NetworkPolicy::Denied
            || !request.authority.allowed_network_hosts.is_empty()
            || request.authority.external_effects != ExternalEffectPolicy::Denied
        {
            return Err(AdapterError::Runtime(
                "process adapter model authority is not native-workspace-only".into(),
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
        let output_schema = std::fs::read(&canonical_schema)
            .map_err(|error| AdapterError::Runtime(error.to_string()))?;
        decode_strict_json::<Value>(&output_schema, limits.max_input_bytes)
            .map_err(|error| AdapterError::Runtime(error.to_string()))?;
        let authority_digest = canonical_digest(&request.authority)
            .map_err(|error| AdapterError::Runtime(error.to_string()))?;
        let authority = ProviderAuthority::from_request(request);
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
            authority,
            visibility,
            limits,
            cancellation,
        })
    }
}

#[derive(Clone, Debug, Serialize)]
struct ProviderAuthority {
    native_capabilities: Vec<&'static str>,
    allowed_programs: Vec<String>,
    network: &'static str,
    external_effects: &'static str,
    limits: ProviderAuthorityLimits,
    workspace_relative_paths_only: bool,
    parent_traversal: &'static str,
    symlinks: &'static str,
    native_effect_contract: NativeEffectContract,
    denied: [&'static str; 8],
}

impl ProviderAuthority {
    fn from_request(request: &RunRequest) -> Self {
        let mut native_capabilities = Vec::new();
        if request
            .authority
            .capabilities
            .contains(&Capability::ReadWorkspace)
        {
            native_capabilities.push("read_utf8_file");
        }
        if request
            .authority
            .capabilities
            .contains(&Capability::WriteWorkspace)
        {
            native_capabilities.push("write_utf8_file");
        }
        Self {
            native_capabilities,
            allowed_programs: Vec::new(),
            network: "denied",
            external_effects: "denied",
            limits: ProviderAuthorityLimits {
                max_turns: request.limits.max_turns,
                max_effects_per_turn: MAX_EFFECTS_PER_TURN,
                max_input_bytes: request.limits.max_input_bytes,
                max_output_bytes: request.limits.max_output_bytes,
                max_effect_timeout_ms: request.limits.max_effect_timeout_ms,
            },
            workspace_relative_paths_only: true,
            parent_traversal: "denied",
            symlinks: "denied",
            native_effect_contract: NativeEffectContract {
                read: "read_file: one workspace-relative path, no content/program/args, timeout_ms=0",
                write: "write_file: one workspace-relative path plus UTF-8 content, no program/args, timeout_ms=0",
                input_digest: "exact current file SHA-256 preimage",
                nonexistent_input_digest: digest_bytes(b"ao.next.file-does-not-exist.v1"),
            },
            denied: [
                "run_program",
                "shell",
                "python3",
                "rg",
                "network",
                "credentials",
                "remote_mutation",
                "executable_interpretation",
            ],
        }
    }
}

#[allow(
    clippy::struct_field_names,
    reason = "the serialized authority contract intentionally names every maximum explicitly"
)]
#[derive(Clone, Debug, Serialize)]
struct ProviderAuthorityLimits {
    max_turns: u32,
    max_effects_per_turn: usize,
    max_input_bytes: u64,
    max_output_bytes: u64,
    max_effect_timeout_ms: u64,
}

#[derive(Clone, Debug, Serialize)]
struct NativeEffectContract {
    read: &'static str,
    write: &'static str,
    input_digest: &'static str,
    nonexistent_input_digest: Digest,
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
    raw_outputs: Vec<InvocationOutput>,
}

impl<R> ProcessRuntimeAdapter<R> {
    #[must_use]
    pub const fn new(config: ProcessAdapterConfig, runner: R) -> Self {
        Self {
            config,
            runner,
            captures: Vec::new(),
            raw_outputs: Vec::new(),
        }
    }

    #[must_use]
    pub fn captures(&self) -> &[RuntimeCapture] {
        &self.captures
    }

    #[must_use]
    pub fn raw_outputs(&self) -> &[InvocationOutput] {
        &self.raw_outputs
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
        self.raw_outputs.push(output.clone());
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
    workspace: ProviderWorkspace<'a>,
    authority_digest: &'a Digest,
    authority: &'a ProviderAuthority,
    provider_visibility_digest: &'a Digest,
    visible_workspace: &'a [ProviderVisibleFile],
    visible_fixtures: &'a [ProviderVisibleFile],
    hidden_tests: &'static str,
    policy_digest: &'a Digest,
    verifier_profile_digest: &'a Digest,
    output_schema_digest: Digest,
    allowed_actions: [&'static str; 4],
    action_sequence: &'static str,
    turn_index: u32,
    repair_attempt: u32,
    effect_observations: &'a [EffectObservation],
}

#[derive(Serialize)]
struct ProviderWorkspace<'a> {
    workspace_id: &'a str,
    seed_digest: &'a Digest,
}

fn build_prompt(
    config: &ProcessAdapterConfig,
    context: &TurnContext,
) -> Result<String, AdapterError> {
    let prompt = ProviderTurnPrompt {
        schema_version: "ao.next.provider-turn-prompt.v2",
        objective: &config.objective,
        run_id: &config.run_id,
        adapter_identity: &config.identity,
        reasoning_effort: &config.reasoning_effort,
        source: &config.source,
        workspace: ProviderWorkspace {
            workspace_id: &config.workspace.workspace_id,
            seed_digest: &config.workspace.seed_digest,
        },
        authority_digest: &config.authority_digest,
        authority: &config.authority,
        provider_visibility_digest: &config.visibility.projection_digest,
        visible_workspace: &config.visibility.source_files,
        visible_fixtures: &config.visibility.visible_fixtures,
        hidden_tests: "unavailable_and_must_not_be_requested",
        policy_digest: &config.policy_digest,
        verifier_profile_digest: &config.verifier_profile_digest,
        output_schema_digest: digest_bytes(&config.output_schema),
        allowed_actions: ["effect", "verify", "blocked", "interrupt"],
        action_sequence: "when max_turns is 1, include every required effect followed by verify in the same actions array",
        turn_index: context.turn_index,
        repair_attempt: context.repair_attempt,
        effect_observations: &context.effect_observations,
    };
    if canonical_digest(&(
        &config.visibility.source_files,
        &config.visibility.visible_fixtures,
    ))
    .map_err(|error| AdapterError::Runtime(error.to_string()))?
        != config.visibility.projection_digest
    {
        return Err(AdapterError::Runtime(
            "provider visibility projection drifted".into(),
        ));
    }
    let prompt =
        serde_json::to_string(&prompt).map_err(|error| AdapterError::Runtime(error.to_string()))?;
    if prompt.len() > config.limits.max_input_bytes {
        return Err(AdapterError::Runtime(
            "provider prompt exceeded the bound input size".into(),
        ));
    }
    Ok(prompt)
}

fn visible_files(
    root: &Path,
    maximum_bytes: usize,
    omit_root_git: bool,
) -> Result<(Vec<ProviderVisibleFile>, Vec<VisibleFileIdentity>), AdapterError> {
    let metadata = std::fs::symlink_metadata(root)
        .map_err(|error| AdapterError::Runtime(error.to_string()))?;
    #[cfg(windows)]
    let reparse_point = windows_reparse_point(metadata.file_attributes());
    #[cfg(not(windows))]
    let reparse_point = false;
    if metadata.file_type().is_symlink() || reparse_point || !metadata.is_dir() {
        return Err(AdapterError::Runtime(
            "provider-visible root is not a regular non-symlink directory".into(),
        ));
    }
    let mut total = 0_usize;
    let mut captured = Vec::new();
    collect_visible_files(
        root,
        root,
        omit_root_git,
        maximum_bytes,
        &mut total,
        &mut captured,
    )?;
    captured.sort_by(|left, right| left.relative.cmp(&right.relative));
    let mut files = Vec::with_capacity(captured.len());
    let mut identities = Vec::with_capacity(captured.len());
    for captured in captured {
        let path = safe_relative_text(&captured.relative)?;
        files.push(ProviderVisibleFile {
            path,
            content: captured.text,
            digest: captured.digest.clone(),
        });
        identities.push(VisibleFileIdentity {
            path: captured.relative,
            sha256: captured.digest,
            size_bytes: u64::try_from(captured.bytes.len()).unwrap_or(u64::MAX),
        });
    }
    Ok((files, identities))
}

fn collect_visible_files(
    root: &Path,
    directory: &Path,
    omit_root_git: bool,
    maximum_bytes: usize,
    total: &mut usize,
    files: &mut Vec<CapturedVisibleFile>,
) -> Result<(), AdapterError> {
    collect_visible_files_with_probe(
        root,
        directory,
        omit_root_git,
        maximum_bytes,
        total,
        files,
        &mut |_: &Path| {},
    )
}

fn collect_visible_files_with_probe(
    root: &Path,
    directory: &Path,
    omit_root_git: bool,
    maximum_bytes: usize,
    total: &mut usize,
    files: &mut Vec<CapturedVisibleFile>,
    before_file_read: &mut dyn FnMut(&Path),
) -> Result<(), AdapterError> {
    #[cfg(windows)]
    let _directory_anchor = open_visible_directory(directory)?;
    let entries = std::fs::read_dir(directory)
        .map_err(|error| AdapterError::Runtime(error.to_string()))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| AdapterError::Runtime(error.to_string()))?;
    for entry in entries {
        let path = entry.path();
        let metadata = std::fs::symlink_metadata(&path)
            .map_err(|error| AdapterError::Runtime(error.to_string()))?;
        #[cfg(windows)]
        let reparse_point = windows_reparse_point(metadata.file_attributes());
        #[cfg(not(windows))]
        let reparse_point = false;
        if metadata.file_type().is_symlink() || reparse_point {
            return Err(AdapterError::Runtime(
                "provider-visible inventory contains a symlink or reparse point".into(),
            ));
        }
        if omit_root_git && directory == root && entry.file_name() == ".git" && metadata.is_dir() {
            continue;
        }
        let relative = path
            .strip_prefix(root)
            .map_err(|error| AdapterError::Runtime(error.to_string()))?;
        safe_relative_text(relative)?;
        if metadata.is_dir() {
            collect_visible_files_with_probe(
                root,
                &path,
                omit_root_git,
                maximum_bytes,
                total,
                files,
                before_file_read,
            )?;
        } else if metadata.is_file() {
            before_file_read(&path);
            let bytes = read_visible_file(&path, maximum_bytes)?;
            *total = total.checked_add(bytes.len()).ok_or_else(|| {
                AdapterError::Runtime("provider-visible byte count overflowed".into())
            })?;
            if bytes.len() > maximum_bytes || *total > maximum_bytes {
                return Err(AdapterError::Runtime(
                    "provider-visible inventory exceeded its byte bound".into(),
                ));
            }
            let text = std::str::from_utf8(&bytes)
                .map_err(|_| AdapterError::Runtime("provider-visible file is not UTF-8".into()))?
                .to_owned();
            let digest = digest_bytes(&bytes);
            files.push(CapturedVisibleFile {
                relative: relative.to_path_buf(),
                text,
                bytes,
                digest,
            });
        } else {
            return Err(AdapterError::Runtime(
                "provider-visible inventory contains a special file".into(),
            ));
        }
    }
    Ok(())
}

#[cfg(not(windows))]
fn read_visible_file(path: &Path, _: usize) -> Result<Vec<u8>, AdapterError> {
    std::fs::read(path).map_err(|error| AdapterError::Runtime(error.to_string()))
}

#[cfg(windows)]
fn open_visible_directory(path: &Path) -> Result<File, AdapterError> {
    let file = OpenOptions::new()
        .read(true)
        .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE)
        .custom_flags(FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT)
        .open(path)
        .map_err(|error| AdapterError::Runtime(error.to_string()))?;
    let metadata = file
        .metadata()
        .map_err(|error| AdapterError::Runtime(error.to_string()))?;
    if !metadata.is_dir() || windows_reparse_point(metadata.file_attributes()) {
        return Err(AdapterError::Runtime(
            "provider-visible root or directory is a reparse point".into(),
        ));
    }
    Ok(file)
}

#[cfg(windows)]
fn read_visible_file(path: &Path, maximum_bytes: usize) -> Result<Vec<u8>, AdapterError> {
    let file = OpenOptions::new()
        .read(true)
        .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE)
        .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
        .open(path)
        .map_err(|error| AdapterError::Runtime(error.to_string()))?;
    let metadata = file
        .metadata()
        .map_err(|error| AdapterError::Runtime(error.to_string()))?;
    if !metadata.is_file() || windows_reparse_point(metadata.file_attributes()) {
        return Err(AdapterError::Runtime(
            "provider-visible file is a reparse point".into(),
        ));
    }
    let limit = u64::try_from(maximum_bytes).unwrap_or(u64::MAX);
    let mut bytes = Vec::new();
    let mut bounded: Take<File> = file.take(limit.saturating_add(1));
    bounded
        .read_to_end(&mut bytes)
        .map_err(|error| AdapterError::Runtime(error.to_string()))?;
    if bytes.len() > maximum_bytes {
        return Err(AdapterError::Runtime(
            "provider-visible inventory exceeded its byte bound".into(),
        ));
    }
    Ok(bytes)
}

fn safe_relative_text(path: &Path) -> Result<String, AdapterError> {
    if path.as_os_str().is_empty()
        || path.components().any(|component| {
            !matches!(component, Component::Normal(_))
                || match component {
                    Component::Normal(value) => {
                        let value = value.to_string_lossy().to_ascii_lowercase();
                        value == ".git" || value.contains("hidden")
                    }
                    _ => false,
                }
        })
    {
        return Err(AdapterError::Runtime(
            "provider-visible inventory path is unsafe".into(),
        ));
    }
    let components =
        path.components()
            .map(|component| match component {
                Component::Normal(value) => value.to_str().map(str::to_owned).ok_or_else(|| {
                    AdapterError::Runtime("provider-visible path is not UTF-8".into())
                }),
                _ => Err(AdapterError::Runtime(
                    "provider-visible inventory path is unsafe".into(),
                )),
            })
            .collect::<Result<Vec<_>, _>>()?;
    Ok(components.join("/"))
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
            usage = Some(read_codex_usage(event.get("usage").ok_or_else(|| {
                AdapterError::Runtime("Codex usage is missing".into())
            })?)?);
        }
    }
    usage.ok_or_else(|| AdapterError::Runtime("Codex trusted usage is missing".into()))
}

fn read_codex_usage(value: &Value) -> Result<TokenUsage, AdapterError> {
    let mut usage = read_usage(value, "cached_input_tokens")?;
    usage.reasoning_tokens = match (
        value.get("reasoning_tokens"),
        value.get("reasoning_output_tokens"),
    ) {
        (Some(_), Some(_)) => {
            return Err(AdapterError::Runtime(
                "Codex usage contains conflicting reasoning counters".into(),
            ));
        }
        (Some(value), None) | (None, Some(value)) => required_u64(value)?,
        (None, None) => 0,
    };
    Ok(usage)
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

#[cfg(test)]
mod reparse_tests {
    #[test]
    fn windows_reparse_attribute_is_unsafe() {
        assert!(super::windows_reparse_point(0x400));
        assert!(!super::windows_reparse_point(0));
    }

    #[cfg(windows)]
    #[test]
    fn provider_visibility_holds_nested_ancestor_until_file_read() {
        use tempfile::TempDir;

        let root = TempDir::new().expect("root");
        let nested = root.path().join("nested");
        std::fs::create_dir(&nested).expect("nested directory");
        std::fs::write(nested.join("file.txt"), b"original\n").expect("original file");

        let outside = TempDir::new().expect("outside");
        std::fs::write(outside.path().join("file.txt"), b"swapped\n").expect("outside file");
        let junction_parent = TempDir::new().expect("junction parent");
        let junction = junction_parent.path().join("candidate");
        let output = std::process::Command::new("cmd")
            .args(["/C", "mklink", "/J"])
            .arg(&junction)
            .arg(outside.path())
            .output()
            .expect("create junction candidate");
        assert!(
            output.status.success(),
            "stdout={} stderr={}",
            String::from_utf8_lossy(&output.stdout),
            String::from_utf8_lossy(&output.stderr)
        );

        let original = nested.join("file.txt");
        let moved = root.path().join("moved");
        let mut rename_failed = None;
        let mut probe = |path: &std::path::Path| {
            if path == original {
                let result = std::fs::rename(&nested, &moved);
                rename_failed = Some(result.is_err());
                if result.is_ok() {
                    std::fs::rename(&junction, &nested).expect("replace nested with junction");
                }
            }
        };
        let mut total = 0;
        let mut captured = Vec::new();

        super::collect_visible_files_with_probe(
            root.path(),
            root.path(),
            false,
            64 * 1024,
            &mut total,
            &mut captured,
            &mut probe,
        )
        .expect("capture provider visibility");

        assert_eq!(rename_failed, Some(true), "nested ancestor was renameable");
        assert_eq!(captured.len(), 1);
        assert_eq!(
            captured[0].relative,
            std::path::Path::new("nested/file.txt")
        );
        assert_eq!(captured[0].bytes, b"original\n");
    }
}
