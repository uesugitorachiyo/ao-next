use std::collections::{BTreeMap, BTreeSet};
#[cfg(unix)]
use std::io::Write as _;
use std::io::{Read as _, Seek as _};
#[cfg(unix)]
use std::os::fd::AsRawFd as _;
#[cfg(unix)]
use std::os::unix::fs::OpenOptionsExt as _;
#[cfg(windows)]
use std::os::windows::fs::MetadataExt as _;
#[cfg(windows)]
use std::os::windows::fs::OpenOptionsExt as _;
use std::path::{Component, Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::Instant;

use ao_next_core::adapter::process::{
    BoundedProcessRunner, ProcessAdapterConfig, ProcessRunner, ProcessRuntimeAdapter,
    ProviderVisibility, RuntimeCapture, RuntimeEnvelopeCapture, capture_runtime_output,
};
use ao_next_core::adapter::{
    AdapterError, AdapterIdentity, AdapterTurn, CancellationToken, EffectObservation,
    InvocationError, InvocationLimits, InvocationOutput, PreparedInvocation, RuntimeAdapter,
    TokenUsage, TurnContext, claude, codex,
};
use ao_next_core::capture::{CaptureIndexStore, CapturePublication};
use ao_next_core::contracts::{
    Capability, Digest, ExternalEffectPolicy, N7ExecutionAuthority,
    N7ExecutionAuthorityExpectation, NetworkPolicy, PreparedRunReceipt, RunRequest, RunState,
    n7_requested_write_scope_digest, validate_n7_execution_authority_current,
    validate_n7_execution_authority_identity,
};
use ao_next_core::effects::LocalEffectBroker;
use ao_next_core::engine::{DirectEngine, EngineEventKind, EngineVerifier, RunOutcome};
use ao_next_core::evidence::digest_bytes;
use ao_next_core::recovery::{CheckpointIdentity, CheckpointJournal, ProviderJournalState};
use ao_next_core::strict_json::{canonical_digest, canonical_json_bytes, decode_strict_json};
use ao_next_core::verifier::{CommandEngineVerifier, CommandVerifierProfile};
use ao_next_eval::corpus::{
    CorpusManifest, EvaluationTask, FUNCTIONAL_SENTINEL_TASK_ID, VariantProfile,
};
use ao_next_eval::metrics::{ExecutionVariant, MeasurementOrigin, RunMeasurement, TokenRow};
use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use sha2::{Digest as ShaDigest, Sha256};

use super::{
    CommandFailure, CommandOutput, LiveRunArgs, LiveVariantArg, PreflightLiveInputArgs,
    decode_file, read_bounded_regular,
};

const WORKER_ID: &str = "ao-next-live-worker-01";
#[cfg(unix)]
const GIT_PROGRAM: &str = "/usr/bin/git";
const GIT_BRANCH: &str = "ao-next-sealed-seed";
const GIT_TIMESTAMP: &str = "2000-01-01T00:00:00Z";
const GIT_OUTPUT_LIMIT: usize = 256 * 1024;
const GIT_CONTROL_LIMIT: u64 = 16 * 1024 * 1024;
const GIT_TIMEOUT_MS: u64 = 10_000;
const LIVE_TOKEN_ENVELOPE: u64 = 564_288;
const ADAPTER_TURN_SCHEMA_BYTES: &[u8] =
    include_bytes!("../../../../docs/contracts/adapter-turn-v1.schema.json");
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

fn unsafe_link_metadata(metadata: &std::fs::Metadata) -> bool {
    if metadata.file_type().is_symlink() {
        return true;
    }
    #[cfg(windows)]
    {
        windows_reparse_point(metadata.file_attributes())
    }
    #[cfg(not(windows))]
    {
        false
    }
}

#[cfg(windows)]
fn open_windows_non_reparse(path: &Path, directory: bool) -> Result<std::fs::File, CommandFailure> {
    let flags = FILE_FLAG_OPEN_REPARSE_POINT
        | if directory {
            FILE_FLAG_BACKUP_SEMANTICS
        } else {
            0
        };
    let file = std::fs::OpenOptions::new()
        .read(true)
        .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE)
        .custom_flags(flags)
        .open(path)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let metadata = file
        .metadata()
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if unsafe_link_metadata(&metadata) || metadata.is_dir() != directory {
        return Err(CommandFailure::invalid_input(
            "Windows path is a reparse point or has the wrong file type",
        ));
    }
    Ok(file)
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "UPPERCASE")]
pub(super) enum LiveVariant {
    N0,
    N4,
    N7,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(super) enum CaptureRootMode {
    RequireEmpty,
    RequireRetained,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum RequestAuthorityMode {
    Current,
    RequestedScope,
}

impl LiveVariant {
    pub(crate) const fn execution_variant(self) -> ExecutionVariant {
        match self {
            Self::N0 => ExecutionVariant::N0,
            Self::N4 => ExecutionVariant::N4,
            Self::N7 => ExecutionVariant::N7,
        }
    }
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub(super) struct LiveRunInput {
    schema_version: String,
    corpus: CorpusManifest,
    task_id: String,
    trial_id: String,
    trial_index: u32,
    schedule_position: u32,
    workspace_instance_id: String,
    source_snapshot: PathBuf,
    objective: PathBuf,
    visible_fixtures: PathBuf,
    hidden_tests: PathBuf,
    output_schema: PathBuf,
    pub(super) raw_capture_root: PathBuf,
    pub(super) request: RunRequest,
    command_verifier: CommandVerifierProfile,
    #[serde(default)]
    current_ao: Option<CurrentAoBinding>,
}

pub(super) struct TrustedLiveInput {
    pub(super) input: LiveRunInput,
    pub(super) bytes: Vec<u8>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
struct CurrentAoBinding {
    schema_version: String,
    ao2_program: PathBuf,
    ao2_program_digest: Digest,
    provider_program: PathBuf,
    provider_program_digest: Digest,
    adapter_version: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
struct SnapshotEntry {
    path: PathBuf,
    sha256: Digest,
    size_bytes: u64,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct SourceSnapshot {
    schema_version: String,
    task_id: String,
    tree_digest: Digest,
    files: Vec<SnapshotEntry>,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct LiveRunRecord {
    schema_version: String,
    variant: LiveVariant,
    terminal_state: RunState,
    measurement: RunMeasurement,
    capture_digests: Vec<Digest>,
    raw_capture_index_digest: Digest,
    verifier_report_digest: Option<Digest>,
    n7_execution_authority_digest: Option<Digest>,
    git_workspace: GitWorkspaceIdentity,
    ao2_control_diagnostics: Vec<serde_json::Value>,
    native_effect_observations: Vec<EffectObservation>,
    record_digest: Digest,
}

#[allow(clippy::too_many_arguments)]
fn live_record_digest(
    variant: LiveVariant,
    terminal_state: &RunState,
    measurement: &RunMeasurement,
    capture_digests: &[Digest],
    raw_capture_index_digest: &Digest,
    verifier_report_digest: Option<&Digest>,
    n7_execution_authority_digest: Option<&Digest>,
    git_workspace: &GitWorkspaceIdentity,
    ao2_control_diagnostics: &[serde_json::Value],
    native_effect_observations: &[EffectObservation],
) -> Result<Digest, CommandFailure> {
    let mut semantic_measurement = serde_json::to_value(measurement)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    if variant == LiveVariant::N7 {
        let semantic_measurement = semantic_measurement.as_object_mut().ok_or_else(|| {
            CommandFailure::evidence("live measurement projection is not an object")
        })?;
        if semantic_measurement.remove("wall_clock_ms").is_none()
            || semantic_measurement.remove("model_wait_ms").is_none()
        {
            return Err(CommandFailure::evidence(
                "live measurement timing projection is incomplete",
            ));
        }
    }
    canonical_digest(&(
        variant,
        terminal_state,
        &semantic_measurement,
        capture_digests,
        raw_capture_index_digest,
        verifier_report_digest,
        n7_execution_authority_digest,
        git_workspace,
        ao2_control_diagnostics,
        native_effect_observations,
    ))
    .map_err(|error| CommandFailure::evidence(error.to_string()))
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub(super) struct GitWorkspaceIdentity {
    pub(super) repository_root: PathBuf,
    pub(super) common_dir: PathBuf,
    pub(super) head_commit: String,
    pub(super) branch: String,
    pub(super) control_digest: Digest,
    pub(super) index_digest: Digest,
}

struct ValidatedInput<'a> {
    task: &'a EvaluationTask,
    profile: &'a VariantProfile,
    initial_files: Vec<SnapshotEntry>,
    hidden_file_digests: BTreeSet<Digest>,
    hidden_file_bytes: Vec<Vec<u8>>,
}

type LiveExecution = (
    RunState,
    Option<RunOutcome>,
    Vec<RuntimeCapture>,
    Vec<InvocationOutput>,
    Option<Digest>,
    Vec<serde_json::Value>,
);

struct SingleProviderProcess<R> {
    runner: R,
    started: bool,
}

struct DigestBoundFakeRunner {
    program: PathBuf,
    #[cfg(unix)]
    interpreter: &'static str,
    executable: std::fs::File,
    calls: Arc<Mutex<usize>>,
}

impl DigestBoundFakeRunner {
    fn new(
        program: &Path,
        program_digest: &Digest,
        calls: Arc<Mutex<usize>>,
    ) -> Result<Self, CommandFailure> {
        #[cfg(unix)]
        let (interpreter, executable) = open_bound_fake_program(program, program_digest)?;
        #[cfg(windows)]
        let executable = open_bound_fake_program(program, program_digest)?;
        Ok(Self {
            program: program.to_path_buf(),
            #[cfg(unix)]
            interpreter,
            executable,
            calls,
        })
    }
}

impl ProcessRunner for DigestBoundFakeRunner {
    fn run(
        &mut self,
        invocation: &PreparedInvocation,
        cancellation: &CancellationToken,
    ) -> Result<InvocationOutput, InvocationError> {
        if invocation.program != "codex" && Path::new(&invocation.program) != self.program {
            return Err(InvocationError::Io(
                "provider-free fake program binding drifted".into(),
            ));
        }
        let result = self.run_bound(invocation, cancellation)?;
        *self
            .calls
            .lock()
            .map_err(|error| InvocationError::Io(error.to_string()))? += 1;
        Ok(result)
    }
}

impl DigestBoundFakeRunner {
    #[cfg(unix)]
    fn run_bound(
        &mut self,
        invocation: &PreparedInvocation,
        cancellation: &CancellationToken,
    ) -> Result<InvocationOutput, InvocationError> {
        self.executable
            .rewind()
            .map_err(|error| InvocationError::Io(error.to_string()))?;
        rustix::io::fcntl_setfd(&self.executable, rustix::io::FdFlags::empty())
            .map_err(|error| InvocationError::Io(error.to_string()))?;
        let descriptor = self.executable.as_raw_fd();
        let descriptor_path = format!("/dev/fd/{descriptor}");
        let mut args = if self.interpreter == "/usr/bin/python3" {
            vec![
                "-I".into(),
                "-S".into(),
                "-c".into(),
                format!(
                    "import os,sys;p=sys.argv[1];sys.argv=sys.argv[1:];os.lseek({descriptor},0,0);c=compile(os.fdopen({descriptor},'rb',closefd=False).read(),p,'exec');exec(c,{{'__name__':'__main__','__file__':p}})"
                ),
                descriptor_path,
            ]
        } else {
            vec![descriptor_path]
        };
        args.extend(invocation.args.clone());
        let result = BoundedProcessRunner.run(
            &PreparedInvocation {
                program: self.interpreter.into(),
                args,
                stdin: invocation.stdin.clone(),
                cwd: invocation.cwd.clone(),
                environment: invocation.environment.clone(),
                limits: invocation.limits,
            },
            cancellation,
        );
        rustix::io::fcntl_setfd(&self.executable, rustix::io::FdFlags::CLOEXEC)
            .map_err(|error| InvocationError::Io(error.to_string()))?;
        result
    }

    #[cfg(windows)]
    fn run_bound(
        &mut self,
        invocation: &PreparedInvocation,
        cancellation: &CancellationToken,
    ) -> Result<InvocationOutput, InvocationError> {
        self.executable
            .rewind()
            .map_err(|error| InvocationError::Io(error.to_string()))?;
        BoundedProcessRunner.run(
            &PreparedInvocation {
                program: self.program.display().to_string(),
                args: invocation.args.clone(),
                stdin: invocation.stdin.clone(),
                cwd: invocation.cwd.clone(),
                environment: invocation.environment.clone(),
                limits: invocation.limits,
            },
            cancellation,
        )
    }
}

#[cfg(unix)]
fn open_bound_fake_program(
    program: &Path,
    program_digest: &Digest,
) -> Result<(&'static str, std::fs::File), CommandFailure> {
    if !program.is_absolute() {
        return Err(CommandFailure::invalid_input(
            "provider-free fake program binding drifted",
        ));
    }
    let descriptor = rustix::fs::open(
        program,
        rustix::fs::OFlags::RDONLY | rustix::fs::OFlags::NOFOLLOW,
        rustix::fs::Mode::empty(),
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let mut file = std::fs::File::from(descriptor);
    let metadata = file
        .metadata()
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if !metadata.is_file() || metadata.len() > 512 * 1024 * 1024 {
        return Err(CommandFailure::invalid_input(
            "provider-free fake program is not a bounded regular non-symlink file",
        ));
    }
    let mut bytes = Vec::with_capacity(usize::try_from(metadata.len()).unwrap_or(0));
    file.read_to_end(&mut bytes)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let observed = digest_bytes(&bytes);
    if observed != *program_digest {
        return Err(CommandFailure::invalid_input(
            "provider-free fake program binding drifted",
        ));
    }
    let interpreter = if bytes.starts_with(b"#!/bin/sh\n") {
        "/bin/sh"
    } else if bytes.starts_with(b"#!/usr/bin/python3\n") {
        "/usr/bin/python3"
    } else {
        return Err(CommandFailure::invalid_input(
            "provider-free fake program must use an exact supported interpreter",
        ));
    };
    drop(file);
    Ok((interpreter, seal_fake_program(&bytes)?))
}

#[cfg(windows)]
fn open_bound_fake_program(
    program: &Path,
    program_digest: &Digest,
) -> Result<std::fs::File, CommandFailure> {
    if !program.is_absolute() {
        return Err(CommandFailure::invalid_input(
            "provider-free fake program binding drifted",
        ));
    }
    let metadata = std::fs::symlink_metadata(program)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if unsafe_link_metadata(&metadata) || !metadata.is_file() || metadata.len() > 512 * 1024 * 1024
    {
        return Err(CommandFailure::invalid_input(
            "provider-free fake program is not a bounded regular non-symlink file",
        ));
    }
    let mut file = std::fs::OpenOptions::new()
        .read(true)
        .share_mode(1)
        .custom_flags(0x0020_0000)
        .open(program)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let mut bytes = Vec::with_capacity(usize::try_from(metadata.len()).unwrap_or(0));
    file.read_to_end(&mut bytes)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if digest_bytes(&bytes) != *program_digest {
        return Err(CommandFailure::invalid_input(
            "provider-free fake program binding drifted",
        ));
    }
    file.rewind()
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    Ok(file)
}

#[cfg(unix)]
fn seal_fake_program(bytes: &[u8]) -> Result<std::fs::File, CommandFailure> {
    for sequence in 0_u8..=u8::MAX {
        let path = std::env::temp_dir().join(format!(
            ".ao-next-fake-{}-{sequence}.sealed",
            std::process::id()
        ));
        let writer = std::fs::OpenOptions::new()
            .read(true)
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&path);
        let mut writer = match writer {
            Ok(writer) => writer,
            Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => continue,
            Err(error) => return Err(CommandFailure::invalid_input(error.to_string())),
        };
        let result = (|| {
            writer.write_all(bytes)?;
            writer.sync_all()?;
            std::fs::remove_file(&path)?;
            writer.rewind()?;
            Ok(writer)
        })();
        if result.is_err() {
            let _ = std::fs::remove_file(&path);
        }
        return result
            .map_err(|error: std::io::Error| CommandFailure::invalid_input(error.to_string()));
    }
    Err(CommandFailure::invalid_input(
        "provider-free fake sealing namespace is exhausted",
    ))
}

pub(crate) struct ProviderFreeRowResult {
    pub output: CommandOutput,
    pub input_digest: Digest,
    pub workspace_digest: Digest,
    pub authority_digest: Digest,
    pub run_id: String,
    pub trial_id: String,
    pub workspace_instance_id: String,
    pub task_id: String,
    pub capture_root: PathBuf,
    pub fake_processes: usize,
}

pub(crate) struct ProviderFreePreflight {
    pub expected_ordinal: usize,
    pub task_id: String,
    pub variant: ExecutionVariant,
    pub trial_index: u32,
    pub schedule_position: u32,
    pub verifier_profile_digest: Digest,
    pub capture_root: PathBuf,
    pub input_digest: Digest,
    pub workspace_digest: Digest,
    pub authority_digest: Digest,
}

pub(crate) fn provider_free_capture_root(input_path: &Path) -> Result<PathBuf, CommandFailure> {
    let bytes = read_bounded_regular(input_path)?;
    let value: serde_json::Value = decode_strict_json(&bytes, 1024 * 1024)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    value
        .get("raw_capture_root")
        .and_then(serde_json::Value::as_str)
        .map(PathBuf::from)
        .ok_or_else(|| CommandFailure::invalid_input("live input capture root is missing"))
}

pub(super) fn verify_provider_free_capture(
    input_path: &Path,
    variant: LiveVariant,
    expected_index_digest: &Digest,
) -> Result<(), CommandFailure> {
    let bytes = read_bounded_regular(input_path)?;
    let input: LiveRunInput = decode_strict_json(&bytes, 1024 * 1024)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    verify_raw_capture_index(
        &input.raw_capture_root,
        &capture_context(&input, variant),
        expected_index_digest,
        input.request.limits.max_output_bytes,
    )
}

pub(super) fn preflight_provider_free_row(
    input_path: &Path,
    variant: LiveVariant,
    trusted_corpus_digest: &Digest,
    trusted_verifier_profile_digest: &Digest,
    fake_program: &Path,
    fake_program_digest: &Digest,
) -> Result<ProviderFreePreflight, CommandFailure> {
    let bytes = read_bounded_regular(input_path)?;
    let input: LiveRunInput = decode_strict_json(&bytes, 1024 * 1024)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    validate_trusted_bindings(
        &input,
        &TrustedLiveBindings {
            corpus_digest: trusted_corpus_digest.clone(),
            verifier_profile_digest: trusted_verifier_profile_digest.clone(),
        },
    )?;
    let validated = validate_input(&input, variant, Utc::now())?;
    if input.request.limits.max_tokens != LIVE_TOKEN_ENVELOPE
        || input.request.limits.max_turns != 1
        || input.request.limits.max_repair_attempts != 0
    {
        return Err(CommandFailure::invalid_input(
            "provider-free campaign requires the exact one-turn 564288-token envelope",
        ));
    }
    let forbidden_capabilities = [
        Capability::NetworkAccess,
        Capability::CredentialAccess,
        Capability::RemoteMutation,
        Capability::Release,
        Capability::Deployment,
        Capability::Publication,
    ];
    if input.request.authority.network != NetworkPolicy::Denied
        || !input.request.authority.allowed_network_hosts.is_empty()
        || input.request.authority.external_effects != ExternalEffectPolicy::Denied
        || forbidden_capabilities
            .iter()
            .any(|capability| input.request.authority.capabilities.contains(capability))
    {
        return Err(CommandFailure::invalid_input(
            "provider-free campaign forbids network, credentials, and external effects",
        ));
    }
    DigestBoundFakeRunner::new(fake_program, fake_program_digest, Arc::new(Mutex::new(0)))?;
    if variant == LiveVariant::N0 {
        let binding = input
            .current_ao
            .as_ref()
            .ok_or_else(|| CommandFailure::invalid_input("N0 provider-free binding is missing"))?;
        if binding.ao2_program != fake_program
            || binding.provider_program != fake_program
            || binding.ao2_program_digest != *fake_program_digest
            || binding.provider_program_digest != *fake_program_digest
        {
            return Err(CommandFailure::invalid_input(
                "N0 is not bound to the exact provider-free fake program",
            ));
        }
    }
    let task_index = input
        .corpus
        .tasks
        .iter()
        .position(|task| task.task_id == input.task_id)
        .ok_or_else(|| CommandFailure::invalid_input("campaign task is missing"))?;
    let schedule_index = input
        .corpus
        .schedule
        .iter()
        .position(|entry| {
            entry.trial_index == input.trial_index
                && entry.schedule_position == input.schedule_position
                && entry.variant == variant.execution_variant()
        })
        .ok_or_else(|| CommandFailure::invalid_input("campaign schedule row is missing"))?;
    Ok(ProviderFreePreflight {
        expected_ordinal: task_index * input.corpus.schedule.len() + schedule_index,
        task_id: input.task_id.clone(),
        variant: variant.execution_variant(),
        trial_index: input.trial_index,
        schedule_position: input.schedule_position,
        verifier_profile_digest: validated.task.verifier_profile_digest.clone(),
        capture_root: input.raw_capture_root.clone(),
        input_digest: digest_bytes(&bytes),
        workspace_digest: validated.task.workspace_seed_digest.clone(),
        authority_digest: canonical_digest(&input.request.authority)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?,
    })
}

pub(super) fn execute_provider_free_row(
    input_path: &Path,
    variant: LiveVariant,
    trusted_corpus_digest: &Digest,
    trusted_verifier_profile_digest: &Digest,
    fake_program: &Path,
    fake_program_digest: &Digest,
    expected_preflight: &ProviderFreePreflight,
) -> Result<ProviderFreeRowResult, CommandFailure> {
    if std::env::var_os("AO_NEXT_LIVE_PROVIDER_CALLS").is_some() {
        return Err(CommandFailure::authorization(
            "provider authorization must be absent during provider-free qualification",
        ));
    }
    let expected_processes = if variant == LiveVariant::N0 { 3 } else { 1 };
    let bytes = read_bounded_regular(input_path)?;
    let input: LiveRunInput = decode_strict_json(&bytes, 1024 * 1024)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let trusted = TrustedLiveBindings {
        corpus_digest: trusted_corpus_digest.clone(),
        verifier_profile_digest: trusted_verifier_profile_digest.clone(),
    };
    validate_trusted_bindings(&input, &trusted)?;
    let validated = validate_input(&input, variant, Utc::now())?;
    if input.request.workspace.root.join(".git").exists()
        || canonical_digest(&validated.initial_files)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
            != validated.task.workspace_seed_digest
    {
        return Err(CommandFailure::invalid_input(
            "provider-free preflight changed or prepared the workspace",
        ));
    }
    let input_digest = digest_bytes(&bytes);
    let workspace_digest = validated.task.workspace_seed_digest.clone();
    let authority_digest = canonical_digest(&input.request.authority)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if input_digest != expected_preflight.input_digest
        || workspace_digest != expected_preflight.workspace_digest
        || authority_digest != expected_preflight.authority_digest
    {
        return Err(CommandFailure::invalid_input(
            "provider-free row drifted after its adjacent preflight",
        ));
    }
    let calls = Arc::new(Mutex::new(0));
    let fake_runner = DigestBoundFakeRunner::new(fake_program, fake_program_digest, calls.clone())?;
    let output = execute_with_runners(
        &input,
        variant,
        MeasurementOrigin::OfflineFixture,
        fake_runner,
        BoundedProcessRunner,
    )?;
    let fake_processes = *calls
        .lock()
        .map_err(|error| CommandFailure::runtime(error.to_string()))?;
    if fake_processes != expected_processes {
        return Err(CommandFailure::runtime(
            "fake process execution count drifted",
        ));
    }
    Ok(ProviderFreeRowResult {
        output,
        input_digest,
        workspace_digest,
        authority_digest,
        run_id: input.request.run_id,
        trial_id: input.trial_id,
        workspace_instance_id: input.workspace_instance_id,
        task_id: input.task_id,
        capture_root: input.raw_capture_root,
        fake_processes,
    })
}

pub(super) fn reject_provider_free_input(
    input_path: &Path,
    variant: LiveVariant,
    trusted_corpus_digest: &Digest,
    trusted_verifier_profile_digest: &Digest,
) -> Result<CommandFailure, CommandFailure> {
    let bytes = match read_bounded_regular(input_path) {
        Ok(bytes) => bytes,
        Err(error) => return Ok(error),
    };
    let input: LiveRunInput = match decode_strict_json(&bytes, 1024 * 1024) {
        Ok(input) => input,
        Err(error) => return Ok(CommandFailure::invalid_input(error.to_string())),
    };
    let trusted = TrustedLiveBindings {
        corpus_digest: trusted_corpus_digest.clone(),
        verifier_profile_digest: trusted_verifier_profile_digest.clone(),
    };
    if let Err(error) = validate_trusted_bindings(&input, &trusted) {
        return Ok(error);
    }
    match validate_input(&input, variant, Utc::now()) {
        Ok(_) => Err(CommandFailure::invalid_input(
            "negative live input unexpectedly passed provider-free preflight",
        )),
        Err(error) => Ok(error),
    }
}

struct CaptureFirstRunner<'a, R> {
    runner: R,
    raw_capture_root: PathBuf,
    capture_context: CaptureContext,
    retained_index: Arc<Mutex<Option<Digest>>>,
    retained_failure: Arc<Mutex<Option<CommandFailure>>>,
    runtime: String,
    max_tokens: u64,
    journal: Option<&'a CheckpointJournal>,
    request: &'a RunRequest,
    prepared_run_digest: Digest,
    execution_authority: Option<&'a N7ExecutionAuthority>,
    execution_authority_digest: Option<&'a Digest>,
}

pub(super) struct PreparedN7Context {
    pub(super) git_workspace: GitWorkspaceIdentity,
    pub(super) prepared_run_digest: Digest,
    pub(super) execution_authority: N7ExecutionAuthority,
}

struct RetainedN7Execution {
    turn: AdapterTurn,
    capture: RuntimeCapture,
    output: InvocationOutput,
    index_digest: Digest,
}

struct RetainedN7Adapter {
    identity: AdapterIdentity,
    run_id: String,
    source: ao_next_core::contracts::SourceIdentity,
    workspace: ao_next_core::contracts::WorkspaceIdentity,
    authority_digest: Digest,
    policy_digest: Digest,
    verifier_profile_digest: Digest,
    turn: Option<AdapterTurn>,
}

impl<R: ProcessRunner> ProcessRunner for CaptureFirstRunner<'_, R> {
    #[allow(
        clippy::too_many_lines,
        reason = "capture and journal durability ordering remains visible at the process boundary"
    )]
    fn run(
        &mut self,
        invocation: &PreparedInvocation,
        cancellation: &CancellationToken,
    ) -> Result<InvocationOutput, InvocationError> {
        if self
            .retained_index
            .lock()
            .map_err(|error| InvocationError::Io(error.to_string()))?
            .is_some()
        {
            return Err(InvocationError::Io(
                "duplicate provider capture identity".into(),
            ));
        }
        let provider_invocation_digest =
            invocation_digest(invocation).map_err(|error| InvocationError::Io(error.message))?;
        if self.execution_authority.is_some_and(|authority| {
            validate_n7_execution_authority_current(authority, Utc::now()).is_err()
        }) {
            return Err(InvocationError::Io(
                "N7 execution authority is not current before provider intent".into(),
            ));
        }
        if let Some(journal) = self.journal {
            let execution_authority_digest = self.execution_authority_digest.ok_or_else(|| {
                InvocationError::Io("N7 execution authority digest is missing".into())
            })?;
            journal
                .provider_may_start(self.request)
                .and_then(|()| {
                    journal.record_provider_request_intent(
                        self.request,
                        &self.prepared_run_digest,
                        execution_authority_digest,
                    )
                })
                .and_then(|()| {
                    journal
                        .record_provider_process_started(self.request, &provider_invocation_digest)
                })
                .map_err(|error| InvocationError::Io(error.to_string()))?;
        }
        let output = self.runner.run(invocation, cancellation)?;
        let (index, index_digest) = retain_raw_capture_files(
            &self.raw_capture_root,
            &self.capture_context,
            &[],
            std::slice::from_ref(&output),
            Some(&provider_invocation_digest),
        )
        .map_err(|error| InvocationError::Io(format!("capture failure: {}", error.message)))?;
        let staged_digest = stage_raw_capture_index(&self.raw_capture_root, &index)
            .map_err(|error| InvocationError::Io(format!("capture failure: {}", error.message)))?;
        if staged_digest != index_digest {
            return Err(InvocationError::Io(
                "capture failure: staged index digest drifted".into(),
            ));
        }
        let raw_digest = canonical_digest(&(output.status, &output.stdout, &output.stderr))
            .map_err(|error| InvocationError::Io(error.to_string()))?;
        if let Some(journal) = self.journal {
            journal
                .record_provider_output_retained(self.request, &raw_digest)
                .map_err(|error| InvocationError::Io(error.to_string()))?;
        }
        let publication = publish_staged_raw_capture_index(&self.raw_capture_root, &staged_digest)
            .map_err(|error| InvocationError::Io(format!("capture failure: {}", error.message)))?;
        let digest = publication.digest().clone();
        if let Some(journal) = self.journal {
            journal
                .record_provider_capture_published(self.request, &digest)
                .map_err(|error| InvocationError::Io(error.to_string()))?;
        }
        *self
            .retained_index
            .lock()
            .map_err(|error| InvocationError::Io(error.to_string()))? = Some(digest);
        let expected_digest = self
            .retained_index
            .lock()
            .map_err(|error| InvocationError::Io(error.to_string()))?
            .clone()
            .ok_or_else(|| InvocationError::Io("retained capture index is missing".into()))?;
        verify_raw_capture_index(
            &self.raw_capture_root,
            &self.capture_context,
            &expected_digest,
            self.capture_context.maximum_output_bytes,
        )
        .map_err(|error| InvocationError::Io(error.message))?;
        if let Some(journal) = self.journal {
            journal
                .record_provider_capture_verified(self.request, &expected_digest)
                .map_err(|error| InvocationError::Io(error.to_string()))?;
        }
        if let Err(error) = verify_and_gate_capture(
            &self.raw_capture_root,
            &self.capture_context,
            &expected_digest,
            &self.runtime,
            &output,
            self.max_tokens,
        ) {
            *self
                .retained_failure
                .lock()
                .map_err(|lock_error| InvocationError::Io(lock_error.to_string()))? = Some(error);
            return Err(InvocationError::Io(
                "retained provider capture failed the pre-control gate".into(),
            ));
        }
        Ok(output)
    }
}

impl RuntimeAdapter for RetainedN7Adapter {
    fn identity(&self) -> AdapterIdentity {
        self.identity.clone()
    }

    fn execute_turn(&mut self, context: &TurnContext) -> Result<AdapterTurn, AdapterError> {
        if context.run_id != self.run_id
            || context.turn_index != 0
            || context.repair_attempt != 0
            || context.source != self.source
            || context.workspace != self.workspace
            || context.authority_digest != self.authority_digest
            || context.policy_digest != self.policy_digest
            || context.verifier_profile_digest != self.verifier_profile_digest
            || !context.effect_observations.is_empty()
        {
            return Err(AdapterError::Runtime(
                "retained adapter context drifted from immutable request bindings".into(),
            ));
        }
        self.turn
            .take()
            .ok_or_else(|| AdapterError::Runtime("retained turn already consumed".into()))
    }
}

#[derive(Serialize)]
#[serde(deny_unknown_fields)]
struct ProviderInvocationIdentity<'a> {
    schema_version: &'static str,
    program: &'a str,
    args: &'a [String],
    working_directory_digest: Digest,
    stdin_digest: Digest,
    environment_key_names: Vec<&'a str>,
}

fn invocation_digest(invocation: &PreparedInvocation) -> Result<Digest, CommandFailure> {
    let working_directory_digest = canonical_digest(&invocation.cwd)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let environment_key_names = invocation
        .environment
        .as_ref()
        .map(|environment| environment.keys().map(String::as_str).collect())
        .unwrap_or_default();
    canonical_digest(&ProviderInvocationIdentity {
        schema_version: "ao.next.provider-invocation.v1",
        program: &invocation.program,
        args: &invocation.args,
        working_directory_digest,
        stdin_digest: digest_bytes(&invocation.stdin),
        environment_key_names,
    })
    .map_err(|error| CommandFailure::evidence(error.to_string()))
}

impl<R> SingleProviderProcess<R> {
    const fn new(runner: R) -> Self {
        Self {
            runner,
            started: false,
        }
    }
}

impl<R: ProcessRunner> ProcessRunner for SingleProviderProcess<R> {
    fn run(
        &mut self,
        invocation: &PreparedInvocation,
        cancellation: &CancellationToken,
    ) -> Result<InvocationOutput, InvocationError> {
        if self.started {
            return Err(InvocationError::Io(
                "single provider process budget exhausted".into(),
            ));
        }
        self.started = true;
        self.runner.run(invocation, cancellation)
    }
}

pub(super) fn execute(
    args: &LiveRunArgs,
    variant: LiveVariant,
) -> Result<CommandOutput, CommandFailure> {
    if std::env::var("AO_NEXT_LIVE_PROVIDER_CALLS").as_deref() != Ok("operator-authorized") {
        return Err(CommandFailure::authorization(
            "live provider calls require separate operator authorization",
        ));
    }
    let prepared_run = match (variant, args.prepared_run.as_ref(), args.authority.as_ref()) {
        (LiveVariant::N7, Some(receipt), Some(authority)) => Some((receipt, authority)),
        (LiveVariant::N7, None, _) => {
            return Err(CommandFailure::invalid_input(
                "--prepared-run is required for N7 live execution",
            ));
        }
        (LiveVariant::N7, Some(_), None) => {
            return Err(CommandFailure::invalid_input(
                "--authority is required for N7 live execution",
            ));
        }
        (LiveVariant::N0 | LiveVariant::N4, Some(_), _)
        | (LiveVariant::N0 | LiveVariant::N4, _, Some(_)) => {
            return Err(CommandFailure::invalid_input(
                "--prepared-run and --authority are forbidden for live baselines",
            ));
        }
        (LiveVariant::N0 | LiveVariant::N4, None, None) => None,
    };
    let trusted = trusted_bindings(
        args.trusted_corpus_digest.as_deref(),
        args.trusted_verifier_profile_digest.as_deref(),
    )?;
    if let Some((prepared_run, authority_path)) = prepared_run {
        let input_bytes = read_bounded_regular(&args.input)?;
        let input: LiveRunInput = decode_strict_json(&input_bytes, 1024 * 1024)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
        validate_trusted_bindings(&input, &trusted)?;
        let receipt: PreparedRunReceipt = decode_file(prepared_run)?;
        let authority: N7ExecutionAuthority = decode_file(authority_path)?;
        let prepared = validate_prepared_run(
            &args.input,
            &input_bytes,
            &input,
            &receipt,
            authority,
            Utc::now(),
        )?;
        execute_with_prepared_runners(
            &input,
            variant,
            MeasurementOrigin::LiveProvider,
            BoundedProcessRunner,
            BoundedProcessRunner,
            Some(prepared),
        )
    } else {
        let input: LiveRunInput = decode_file(&args.input)?;
        validate_trusted_bindings(&input, &trusted)?;
        execute_with_runners(
            &input,
            variant,
            MeasurementOrigin::LiveProvider,
            BoundedProcessRunner,
            BoundedProcessRunner,
        )
    }
}

fn validate_prepared_run(
    input_path: &Path,
    input_bytes: &[u8],
    input: &LiveRunInput,
    receipt: &PreparedRunReceipt,
    authority: N7ExecutionAuthority,
    now: DateTime<Utc>,
) -> Result<PreparedN7Context, CommandFailure> {
    let (git_workspace, prepared_run_digest) =
        validate_prepared_run_with_mode(input_path, input_bytes, input, receipt, now, true)?;
    let execution_authority_digest = canonical_digest(&authority)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    validate_execution_authority(
        input,
        receipt,
        &prepared_run_digest,
        &authority,
        &execution_authority_digest,
        now,
        true,
    )?;
    Ok(PreparedN7Context {
        git_workspace,
        prepared_run_digest,
        execution_authority: authority,
    })
}

pub(super) fn validate_prepared_run_for_recovery(
    input_path: &Path,
    input: &LiveRunInput,
    receipt: &PreparedRunReceipt,
    now: DateTime<Utc>,
) -> Result<(GitWorkspaceIdentity, Digest), CommandFailure> {
    let input_bytes = read_bounded_regular(input_path)?;
    validate_prepared_run_with_mode(input_path, &input_bytes, input, receipt, now, false)
}

fn validate_prepared_run_with_mode(
    input_path: &Path,
    input_bytes: &[u8],
    input: &LiveRunInput,
    receipt: &PreparedRunReceipt,
    now: DateTime<Utc>,
    require_current: bool,
) -> Result<(GitWorkspaceIdentity, Digest), CommandFailure> {
    let request_digest = canonical_digest(&input.request)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let journal_identity = CheckpointIdentity::from_request(&input.request)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let repository_root = std::fs::canonicalize(&input.request.workspace.root)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if receipt.schema_version != "ao.next.prepared-run.v1"
        || receipt.run_id != input.request.run_id
        || receipt.input_digest != digest_bytes(input_bytes)
        || receipt.request_digest != request_digest
        || receipt.repository_root != repository_root
        || receipt.branch != GIT_BRANCH
        || receipt.workspace_digest != input.request.workspace.seed_digest
        || receipt.journal_identity_digest
            != canonical_digest(&journal_identity)
                .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
        || receipt.prepared_at >= receipt.expires_at
        || (require_current && receipt.expires_at <= now)
        || receipt.provider_calls != 0
        || receipt.safe_to_execute
    {
        return Err(CommandFailure::invalid_input(
            "prepared-run identity or expiry drifted",
        ));
    }
    let git_workspace = GitWorkspaceIdentity {
        repository_root: receipt.repository_root.clone(),
        common_dir: receipt.common_directory.clone(),
        head_commit: receipt.base_commit.clone(),
        branch: GIT_BRANCH.into(),
        control_digest: receipt.control_digest.clone(),
        index_digest: receipt.index_digest.clone(),
    };
    verify_git_workspace(&git_workspace, require_current)
        .map_err(|error| CommandFailure::invalid_input(error.message))?;
    if require_current {
        validate_prepared_input(input, LiveVariant::N7, now, &git_workspace)?;
        revalidate_prepared_live_input(input, &git_workspace)?;
    } else {
        validate_input_with_capture_mode(
            input,
            LiveVariant::N7,
            now,
            Some(&git_workspace),
            CaptureRootMode::RequireRetained,
            RequestAuthorityMode::RequestedScope,
        )?;
    }
    let journal = CheckpointJournal::new(
        execution_journal_root(input),
        execution_journal_maximum_bytes(&input.request),
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if require_current {
        journal.bind_pristine_request(&input.request)
    } else {
        journal.bind_request(&input.request)
    }
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if read_bounded_regular(input_path)? != input_bytes {
        return Err(CommandFailure::invalid_input(
            "live input drifted after prepared-run validation",
        ));
    }
    let prepared_run_digest = canonical_digest(receipt)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    Ok((git_workspace, prepared_run_digest))
}

pub(super) fn validate_execution_authority(
    input: &LiveRunInput,
    receipt: &PreparedRunReceipt,
    prepared_run_digest: &Digest,
    authority: &N7ExecutionAuthority,
    execution_authority_digest: &Digest,
    now: DateTime<Utc>,
    require_current: bool,
) -> Result<(), CommandFailure> {
    let expectation = N7ExecutionAuthorityExpectation {
        execution_authority_digest: execution_authority_digest.clone(),
        prepared_run_digest: prepared_run_digest.clone(),
        preparation_input_digest: receipt.input_digest.clone(),
        preparation_request_digest: receipt.request_digest.clone(),
        base_commit: receipt.base_commit.clone(),
        workspace_identity_digest: canonical_digest(&input.request.workspace)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?,
        workspace_digest: receipt.workspace_digest.clone(),
        workspace_root: receipt.repository_root.clone(),
        requested_authority_digest: canonical_digest(&input.request.authority)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?,
        write_scope_digest: n7_requested_write_scope_digest(&input.request)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?,
        prepared_at: receipt.prepared_at,
    };
    validate_n7_execution_authority_identity(authority, &expectation)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if require_current {
        validate_n7_execution_authority_current(authority, now)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    }
    Ok(())
}

pub(super) fn revalidate_recovery_before_mutation(
    input: &LiveRunInput,
    git_workspace: &GitWorkspaceIdentity,
) -> Result<(), CommandFailure> {
    verify_git_workspace(git_workspace, true)
        .map_err(|error| CommandFailure::invalid_input(error.message))?;
    revalidate_prepared_live_input(input, git_workspace)
}

pub fn preflight(args: &PreflightLiveInputArgs) -> Result<CommandOutput, CommandFailure> {
    if std::env::var("AO_NEXT_LIVE_PROVIDER_CALLS").is_ok() {
        return Err(CommandFailure::authorization(
            "provider authorization must be absent during offline input preflight",
        ));
    }
    let trusted = trusted_bindings(
        args.trusted_corpus_digest.as_deref(),
        args.trusted_verifier_profile_digest.as_deref(),
    )?;
    let input: LiveRunInput = decode_file(&args.input)?;
    validate_trusted_bindings(&input, &trusted)?;
    let variant = match args.variant {
        LiveVariantArg::N0 => LiveVariant::N0,
        LiveVariantArg::N4 => LiveVariant::N4,
        LiveVariantArg::N7 => LiveVariant::N7,
    };
    let validated = validate_input(&input, variant, Utc::now())?;
    Ok(CommandOutput::new(
        serde_json::json!({
            "schema_version": "ao.next.live-input-preflight.v1",
            "corpus_digest": input.corpus.corpus_digest,
            "task_id": input.task_id,
            "trial_id": input.trial_id,
            "trial_index": input.trial_index,
            "schedule_position": input.schedule_position,
            "workspace_instance_id": input.workspace_instance_id,
            "variant": variant,
            "model_identifier": validated.profile.model_identifier,
            "reasoning_effort": input.request.model_profile.reasoning_effort,
            "runtime": validated.profile.runtime,
            "adapter_version": validated.profile.adapter_version,
            "workspace_prepared": false,
            "provider_calls": 0
        }),
        "validated one provider-free live input",
        0,
    ))
}

#[derive(Clone, Debug)]
struct TrustedLiveBindings {
    corpus_digest: Digest,
    verifier_profile_digest: Digest,
}

fn trusted_bindings(
    corpus_digest: Option<&str>,
    verifier_profile_digest: Option<&str>,
) -> Result<TrustedLiveBindings, CommandFailure> {
    let corpus_digest = corpus_digest.ok_or_else(|| {
        CommandFailure::usage("--trusted-corpus-digest is required for live admission")
    })?;
    let verifier_profile_digest = verifier_profile_digest.ok_or_else(|| {
        CommandFailure::usage("--trusted-verifier-profile-digest is required for live admission")
    })?;
    Ok(TrustedLiveBindings {
        corpus_digest: Digest::new(corpus_digest)
            .map_err(|error| CommandFailure::usage(error.to_string()))?,
        verifier_profile_digest: Digest::new(verifier_profile_digest)
            .map_err(|error| CommandFailure::usage(error.to_string()))?,
    })
}

fn validate_trusted_bindings(
    input: &LiveRunInput,
    trusted: &TrustedLiveBindings,
) -> Result<(), CommandFailure> {
    if input.corpus.corpus_digest != trusted.corpus_digest
        || input.request.verifier_profile.profile_digest != trusted.verifier_profile_digest
        || input.command_verifier.profile_digest != trusted.verifier_profile_digest
    {
        return Err(CommandFailure::invalid_input(
            "operator-owned corpus or verifier profile binding drifted",
        ));
    }
    Ok(())
}

pub(super) fn load_trusted_live_input(
    path: &Path,
    variant: LiveVariant,
    trusted_corpus_digest: &str,
    trusted_verifier_profile_digest: &str,
    now: DateTime<Utc>,
) -> Result<TrustedLiveInput, CommandFailure> {
    let bytes = read_bounded_regular(path)?;
    let input: LiveRunInput = decode_strict_json(&bytes, 1024 * 1024)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let trusted = trusted_bindings(
        Some(trusted_corpus_digest),
        Some(trusted_verifier_profile_digest),
    )?;
    validate_trusted_bindings(&input, &trusted)?;
    validate_input_with_capture_mode(
        &input,
        variant,
        now,
        None,
        CaptureRootMode::RequireEmpty,
        RequestAuthorityMode::RequestedScope,
    )?;
    Ok(TrustedLiveInput { input, bytes })
}

pub(super) fn load_trusted_live_input_for_recovery(
    path: &Path,
    variant: LiveVariant,
    trusted_corpus_digest: &str,
    trusted_verifier_profile_digest: &str,
    now: DateTime<Utc>,
) -> Result<LiveRunInput, CommandFailure> {
    let bytes = read_bounded_regular(path)?;
    let input: LiveRunInput = decode_strict_json(&bytes, 1024 * 1024)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let trusted = trusted_bindings(
        Some(trusted_corpus_digest),
        Some(trusted_verifier_profile_digest),
    )?;
    validate_trusted_bindings(&input, &trusted)?;
    validate_input_with_capture_mode(
        &input,
        variant,
        now,
        None,
        CaptureRootMode::RequireRetained,
        RequestAuthorityMode::RequestedScope,
    )?;
    Ok(input)
}

fn required_live_token_envelope(
    context_limit: u64,
    output_limit: u64,
) -> Result<u64, CommandFailure> {
    if context_limit == 0 || output_limit == 0 {
        return Err(CommandFailure::invalid_input(
            "live model context and output limits must be nonzero",
        ));
    }
    context_limit
        .checked_mul(2)
        .and_then(|context| {
            output_limit
                .checked_mul(2)
                .and_then(|output| context.checked_add(output))
        })
        .ok_or_else(|| CommandFailure::invalid_input("live token envelope overflowed"))
}

fn validate_live_token_envelope(request: &RunRequest) -> Result<(), CommandFailure> {
    let required = required_live_token_envelope(
        request.model_profile.context_limit,
        request.model_profile.output_limit,
    )?;
    if request.limits.max_tokens < required {
        return Err(CommandFailure::invalid_input(format!(
            "sealed max_tokens {} is below the required live envelope {required}",
            request.limits.max_tokens
        )));
    }
    Ok(())
}

fn validate_n7_model_authority(request: &RunRequest) -> Result<(), CommandFailure> {
    let native_capabilities =
        BTreeSet::from([Capability::ReadWorkspace, Capability::WriteWorkspace]);
    if request.authority.capabilities != native_capabilities
        || request.authority.allowed_roots != [request.workspace.root.clone()]
        || !request.authority.allowed_programs.is_empty()
        || request.authority.network != NetworkPolicy::Denied
        || !request.authority.allowed_network_hosts.is_empty()
        || request.authority.external_effects != ExternalEffectPolicy::Denied
        || request.limits.max_turns != 1
        || request.limits.max_repair_attempts != 0
        || request.limits.max_tokens != LIVE_TOKEN_ENVELOPE
    {
        return Err(CommandFailure::invalid_input(
            "N7 model authority must be exact native workspace read/write authority",
        ));
    }
    Ok(())
}

fn validate_trusted_usage(
    usage: &TokenUsage,
    max_tokens: u64,
    capture_digest: &Digest,
) -> Result<u64, CommandFailure> {
    let failure = |reason: &str, message: &str, total_tokens: Option<u64>| {
        CommandFailure::runtime_with_diagnostic(
            message,
            serde_json::json!({
                "schema_version": "ao.next.trusted-usage-gate-diagnostic.v1",
                "stage": "token-envelope",
                "reason": reason,
                "capture_digest": capture_digest,
                "usage": {
                    "input_tokens": usage.input_tokens,
                    "cached_input_tokens": usage.cached_input_tokens,
                    "reasoning_tokens": usage.reasoning_tokens,
                    "output_tokens": usage.output_tokens,
                    "total_tokens": total_tokens,
                    "max_tokens": max_tokens,
                }
            }),
        )
    };
    let Some(total_tokens) = usage.checked_total_tokens() else {
        return Err(failure(
            "arithmetic-overflow",
            "trusted provider usage overflowed",
            None,
        ));
    };
    if usage.cached_input_tokens > usage.input_tokens {
        return Err(failure(
            "cached-input-exceeds-input",
            "trusted cached input usage exceeds input usage",
            Some(total_tokens),
        ));
    }
    if usage.reasoning_tokens > usage.output_tokens {
        return Err(failure(
            "reasoning-exceeds-output",
            "trusted reasoning usage exceeds output usage",
            Some(total_tokens),
        ));
    }
    if total_tokens > max_tokens {
        return Err(failure(
            "over-limit",
            "trusted provider usage exceeded the sealed token limit",
            Some(total_tokens),
        ));
    }
    Ok(total_tokens)
}

fn verify_and_gate_capture(
    root: &Path,
    context: &CaptureContext,
    expected_index_digest: &Digest,
    runtime: &str,
    output: &InvocationOutput,
    max_tokens: u64,
) -> Result<RuntimeEnvelopeCapture, CommandFailure> {
    if let Err(error) = verify_raw_capture_index(
        root,
        context,
        expected_index_digest,
        context.maximum_output_bytes,
    ) {
        record_capture_terminal(root, context, &error, Some("evidence"))?;
        return Err(error);
    }
    if output.status != 0 {
        let error = CommandFailure::runtime(format!(
            "provider output was retained with status {}",
            output.status
        ));
        record_capture_terminal(root, context, &error, Some("provider"))?;
        return Err(error);
    }
    let raw_capture_digest = canonical_digest(&(output.status, &output.stdout, &output.stderr))
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let capture = match capture_runtime_output(
        runtime,
        output,
        usize::try_from(context.maximum_output_bytes).unwrap_or(usize::MAX),
    ) {
        Ok(capture) => capture,
        Err(adapter_error) => {
            let error = CommandFailure::runtime_with_diagnostic(
                "trusted provider usage envelope is invalid",
                serde_json::json!({
                    "schema_version": "ao.next.trusted-usage-gate-diagnostic.v1",
                    "stage": "token-envelope",
                    "reason": "malformed",
                    "capture_digest": raw_capture_digest,
                    "usage": null,
                    "normalization_error": adapter_error.to_string(),
                }),
            );
            record_capture_terminal(root, context, &error, Some("token-envelope"))?;
            return Err(error);
        }
    };
    if let Err(error) = validate_trusted_usage(&capture.usage, max_tokens, &raw_capture_digest) {
        record_capture_terminal(root, context, &error, Some("token-envelope"))?;
        return Err(error);
    }
    Ok(capture)
}

#[allow(
    clippy::too_many_lines,
    reason = "the run record is assembled once so every measured field remains visibly source-bound"
)]
fn execute_with_runners<P: ProcessRunner, V: ProcessRunner>(
    input: &LiveRunInput,
    variant: LiveVariant,
    measurement_origin: MeasurementOrigin,
    provider_runner: P,
    verifier_runner: V,
) -> Result<CommandOutput, CommandFailure> {
    execute_with_prepared_runners(
        input,
        variant,
        measurement_origin,
        provider_runner,
        verifier_runner,
        None,
    )
}

#[allow(
    clippy::too_many_lines,
    reason = "the run record is assembled once so every measured field remains visibly source-bound"
)]
fn execute_with_prepared_runners<P: ProcessRunner, V: ProcessRunner>(
    input: &LiveRunInput,
    variant: LiveVariant,
    measurement_origin: MeasurementOrigin,
    provider_runner: P,
    verifier_runner: V,
    prepared: Option<PreparedN7Context>,
) -> Result<CommandOutput, CommandFailure> {
    execute_with_prepared_runner_mode(
        input,
        variant,
        measurement_origin,
        provider_runner,
        verifier_runner,
        prepared,
        None,
    )
}

pub(super) fn execute_recovered_live(
    input: &LiveRunInput,
    turn: AdapterTurn,
    capture: RuntimeCapture,
    output: InvocationOutput,
    prepared: PreparedN7Context,
    retained_index: &Digest,
) -> Result<CommandOutput, CommandFailure> {
    execute_with_prepared_runner_mode(
        input,
        LiveVariant::N7,
        MeasurementOrigin::LiveProvider,
        BoundedProcessRunner,
        BoundedProcessRunner,
        Some(prepared),
        Some(RetainedN7Execution {
            turn,
            capture,
            output,
            index_digest: retained_index.clone(),
        }),
    )
}

pub(super) fn recovered_terminal_output(
    input: &LiveRunInput,
    git_workspace: &GitWorkspaceIdentity,
    index_digest: &Digest,
    capture: &RuntimeCapture,
    execution_authority_digest: &Digest,
    bytes: &[u8],
) -> Result<CommandOutput, CommandFailure> {
    let record: LiveRunRecord = decode_strict_json(
        bytes,
        usize::try_from(execution_journal_maximum_bytes(&input.request)).unwrap_or(usize::MAX),
    )
    .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let task = input
        .corpus
        .tasks
        .iter()
        .find(|task| task.task_id == input.task_id)
        .ok_or_else(|| CommandFailure::evidence("task is not in the sealed corpus"))?;
    if record.schema_version != "ao.next.live-run-record.v1"
        || record.variant != LiveVariant::N7
        || record.git_workspace != *git_workspace
        || record.raw_capture_index_digest != *index_digest
        || record.capture_digests != [capture.raw_capture_digest.clone()]
        || record.verifier_report_digest.is_none()
        || record.n7_execution_authority_digest.as_ref() != Some(execution_authority_digest)
        || record.measurement.corpus_digest != input.corpus.corpus_digest
        || record.measurement.run_id != input.request.run_id
        || record.measurement.trial_id != input.trial_id
        || record.measurement.workspace_instance_id != input.workspace_instance_id
        || record.measurement.task_id != input.task_id
        || record.measurement.source_digest != task.source_digest
        || record.measurement.workspace_seed_digest != task.workspace_seed_digest
        || live_record_digest(
            record.variant,
            &record.terminal_state,
            &record.measurement,
            &record.capture_digests,
            &record.raw_capture_index_digest,
            record.verifier_report_digest.as_ref(),
            record.n7_execution_authority_digest.as_ref(),
            &record.git_workspace,
            &record.ao2_control_diagnostics,
            &record.native_effect_observations,
        )? != record.record_digest
        || canonical_json_bytes(&record)
            .map_err(|error| CommandFailure::evidence(error.to_string()))?
            != bytes
    {
        return Err(CommandFailure::evidence(
            "retained terminal record identity is contradictory",
        ));
    }
    let status = match record.terminal_state {
        _ if record.measurement.hidden_test_exposure => 7,
        RunState::Passed => 0,
        RunState::Interrupted => 6,
        RunState::Failed => 5,
        _ => 4,
    };
    let summary = format!(
        "N7 run {} ended {:?}",
        input.request.run_id, record.terminal_state
    );
    let value = serde_json::to_value(record)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    Ok(CommandOutput::new(value, summary, status))
}

#[allow(
    clippy::too_many_lines,
    reason = "the run record is assembled once so every measured field remains visibly source-bound"
)]
fn execute_with_prepared_runner_mode<P: ProcessRunner, V: ProcessRunner>(
    input: &LiveRunInput,
    variant: LiveVariant,
    measurement_origin: MeasurementOrigin,
    provider_runner: P,
    verifier_runner: V,
    prepared: Option<PreparedN7Context>,
    recovery: Option<RetainedN7Execution>,
) -> Result<CommandOutput, CommandFailure> {
    let started_at = Utc::now();
    let started = Instant::now();
    let validated = if let Some(prepared) = &prepared {
        validate_input_with_capture_mode(
            input,
            variant,
            started_at,
            Some(&prepared.git_workspace),
            if recovery.is_some() {
                CaptureRootMode::RequireRetained
            } else {
                CaptureRootMode::RequireEmpty
            },
            RequestAuthorityMode::RequestedScope,
        )?
    } else {
        validate_input(input, variant, started_at)?
    };
    let (git_workspace, prepared_run_digest, execution_authority) = if let Some(prepared) = prepared
    {
        if variant != LiveVariant::N7 {
            return Err(CommandFailure::invalid_input(
                "prepared-run context is restricted to N7",
            ));
        }
        (
            prepared.git_workspace,
            prepared.prepared_run_digest,
            Some(prepared.execution_authority),
        )
    } else {
        let git_workspace = prepare_git_workspace(
            &input.request.workspace.root,
            &input.request.authority.allowed_roots,
            &validated.task.workspace_seed_digest,
        )?;
        let digest = canonical_digest(&git_workspace)
            .map_err(|error| CommandFailure::evidence(error.to_string()))?;
        (git_workspace, digest, None)
    };
    let provider_visibility = if variant == LiveVariant::N7 && recovery.is_none() {
        Some(n7_provider_visibility(input, validated.task)?)
    } else {
        None
    };
    let execution_authority_digest = execution_authority
        .as_ref()
        .map(canonical_digest)
        .transpose()
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let provider_intent_authority_digest = (variant == LiveVariant::N7).then(|| {
        execution_authority_digest
            .clone()
            .unwrap_or_else(|| digest_bytes(b"ao.next.provider-free-no-execution-authority.v1"))
    });
    let cancellation = CancellationToken::new();
    let invocation_limits = invocation_limits(&input.request)?;
    let mut verifier = CommandEngineVerifier::new(
        &input.request,
        input.command_verifier.clone(),
        verifier_runner,
        cancellation.clone(),
        if variant == LiveVariant::N7 {
            input.request.authority.issued_at
        } else {
            started_at
        },
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let retained_index = Arc::new(Mutex::new(
        recovery
            .as_ref()
            .map(|retained| retained.index_digest.clone()),
    ));
    let retained_failure = Arc::new(Mutex::new(None));
    let capture_context = capture_context(input, variant);
    verify_git_workspace(&git_workspace, recovery.is_none())?;
    if recovery.is_none() {
        revalidate_post_git_inputs(input, validated.task, &git_workspace)?;
    }
    let journal = (variant == LiveVariant::N7)
        .then(|| {
            CheckpointJournal::new(
                execution_journal_root(input),
                execution_journal_maximum_bytes(&input.request),
            )
        })
        .transpose()
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;

    let execution = match variant {
        LiveVariant::N0 => execute_n0(
            input,
            provider_runner,
            &capture_context,
            &mut verifier,
            &cancellation,
            invocation_limits,
        ),
        LiveVariant::N7 => {
            if let Some(retained) = recovery {
                execute_n7_retained(
                    input,
                    retained,
                    &mut verifier,
                    journal.as_ref().expect("N7 journal constructed"),
                    execution_authority
                        .as_ref()
                        .expect("prepared N7 authority validated"),
                )
            } else {
                execute_n7(
                    input,
                    provider_visibility.expect("N7 visibility constructed"),
                    CaptureFirstRunner {
                        runner: provider_runner,
                        raw_capture_root: input.raw_capture_root.clone(),
                        capture_context: capture_context.clone(),
                        retained_index: retained_index.clone(),
                        retained_failure: retained_failure.clone(),
                        runtime: input.request.model_profile.runtime.clone(),
                        max_tokens: input.request.limits.max_tokens,
                        journal: journal.as_ref(),
                        request: &input.request,
                        prepared_run_digest: prepared_run_digest.clone(),
                        execution_authority: execution_authority.as_ref(),
                        execution_authority_digest: provider_intent_authority_digest.as_ref(),
                    },
                    &mut verifier,
                    cancellation.clone(),
                    invocation_limits,
                    journal.as_ref().expect("N7 journal constructed"),
                    execution_authority.as_ref(),
                )
            }
        }
        LiveVariant::N4 => execute_n4(
            input,
            CaptureFirstRunner {
                runner: provider_runner,
                raw_capture_root: input.raw_capture_root.clone(),
                capture_context: capture_context.clone(),
                retained_index: retained_index.clone(),
                retained_failure: retained_failure.clone(),
                runtime: input.request.model_profile.runtime.clone(),
                max_tokens: input.request.limits.max_tokens,
                journal: None,
                request: &input.request,
                prepared_run_digest,
                execution_authority: None,
                execution_authority_digest: None,
            },
            &mut verifier,
            &cancellation,
            invocation_limits,
        ),
    };
    let (
        terminal_state,
        outcome,
        captures,
        raw_outputs,
        retained_capture_index,
        ao2_control_diagnostics,
    ) = match execution {
        Ok(execution) => execution,
        Err(error) => {
            let error = retained_failure
                .lock()
                .ok()
                .and_then(|mut failure| failure.take())
                .unwrap_or(error);
            if input.raw_capture_root.join("capture-index.json").is_file()
                && !input
                    .raw_capture_root
                    .join("capture-terminal.json")
                    .exists()
            {
                record_capture_terminal(&input.raw_capture_root, &capture_context, &error, None)?;
            }
            return Err(error);
        }
    };
    if let Some(error) = retained_failure
        .lock()
        .ok()
        .and_then(|mut failure| failure.take())
    {
        return Err(error);
    }
    let raw_capture_index_digest = retained_capture_index
        .or_else(|| retained_index.lock().ok().and_then(|value| value.clone()))
        .ok_or_else(|| {
            CommandFailure::evidence(format!(
                "raw provider capture coverage is incomplete for {variant:?} {}",
                input.request.run_id
            ))
        })?;
    if let Err(error) = verify_raw_capture_index(
        &input.raw_capture_root,
        &capture_context,
        &raw_capture_index_digest,
        input.request.limits.max_output_bytes,
    ) {
        record_capture_terminal(
            &input.raw_capture_root,
            &capture_context,
            &error,
            Some("evidence"),
        )?;
        return Err(error);
    }
    if captures.len() != raw_outputs.len() {
        let provider_failed = raw_outputs.iter().any(|output| output.status != 0);
        let error = CommandFailure::runtime(if provider_failed {
            "provider output was retained with a nonzero status"
        } else {
            "provider output was retained but could not be normalized"
        });
        record_capture_terminal(
            &input.raw_capture_root,
            &capture_context,
            &error,
            Some(if provider_failed {
                "provider"
            } else {
                "normalization"
            }),
        )?;
        return Err(error);
    }
    let wall_clock_ms = u64::try_from(started.elapsed().as_millis())
        .unwrap_or(u64::MAX)
        .max(1);
    let final_files = snapshot_product_tree(
        &input.request.workspace.root,
        input.request.limits.max_input_bytes,
        Some(&git_workspace),
    )?;
    let hidden_test_exposure = hidden_material_exposed(
        &input.request.workspace.root,
        &final_files,
        &validated.hidden_file_digests,
        &validated.hidden_file_bytes,
        input.request.limits.max_input_bytes,
    )?;
    let changed_files = count_changed_files(&validated.initial_files, &final_files);
    let report = verifier.reports().last();
    let hidden_tests_total =
        u32::try_from(input.command_verifier.entries.len()).unwrap_or(u32::MAX);
    let hidden_tests_passed = report.map_or(0, |report| {
        u32::try_from(
            report
                .results
                .iter()
                .filter(|result| result.verifier_id.starts_with("command:") && result.passed)
                .count(),
        )
        .unwrap_or(u32::MAX)
    });
    let usage = if let Some(outcome) = &outcome {
        outcome.metrics.usage.clone()
    } else {
        checked_sum_capture_usage(&captures)
            .ok_or_else(|| CommandFailure::runtime("trusted provider usage overflowed"))?
    };
    let total_tokens = usage
        .checked_total_tokens()
        .ok_or_else(|| CommandFailure::runtime("trusted provider usage overflowed"))?;
    if total_tokens > input.request.limits.max_tokens {
        return Err(CommandFailure::evidence(
            "trusted provider usage changed after the pre-control gate",
        ));
    }
    let model_wait_ms = captures
        .iter()
        .fold(0_u64, |total, capture| {
            total.saturating_add(capture.model_wait_ms)
        })
        .min(wall_clock_ms);
    let capture_digests = captures
        .iter()
        .map(|capture| capture.raw_capture_digest.clone())
        .collect::<Vec<_>>();
    let raw_capture_digest = canonical_digest(&capture_digests)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let task_success = terminal_state == RunState::Passed && !hidden_test_exposure;
    if !task_success {
        let stage = if raw_outputs.iter().any(|output| output.status != 0) {
            "provider"
        } else {
            "verifier"
        };
        record_capture_terminal(
            &input.raw_capture_root,
            &capture_context,
            &CommandFailure::runtime(format!("{stage} stage did not pass")),
            Some(stage),
        )?;
    }
    let evidence_complete = task_success && !captures.is_empty() && report.is_some();
    let unauthorized_effects = outcome.as_ref().map_or(0, |value| {
        u32::try_from(
            value
                .events
                .iter()
                .filter(|event| matches!(event.kind, EngineEventKind::EffectDenied(_)))
                .count(),
        )
        .unwrap_or(u32::MAX)
    });
    let repair_attempts = outcome
        .as_ref()
        .map_or(0, |value| value.metrics.repair_attempts);
    let completed_effects = outcome.as_ref().map_or_else(Vec::new, |value| {
        value
            .events
            .iter()
            .filter_map(|event| match &event.kind {
                EngineEventKind::EffectCompleted(observation) => {
                    Some(observation.effect_id.as_str())
                }
                _ => None,
            })
            .collect::<Vec<_>>()
    });
    let unique_completed_effects = completed_effects.iter().copied().collect::<BTreeSet<_>>();
    let recovery_attempted = repair_attempts > 0;
    let recovery_no_duplicate_effect =
        recovery_attempted && unique_completed_effects.len() == completed_effects.len();
    let measurement = RunMeasurement {
        schema_version: "ao.next.run-measurement.v2".into(),
        corpus_digest: input.corpus.corpus_digest.clone(),
        run_id: input.request.run_id.clone(),
        trial_id: input.trial_id.clone(),
        trial_index: input.trial_index,
        schedule_position: input.schedule_position,
        raw_capture_digest,
        raw_capture_digests: capture_digests.clone(),
        workspace_instance_id: input.workspace_instance_id.clone(),
        task_id: input.task_id.clone(),
        variant: variant.execution_variant(),
        source_digest: validated.task.source_digest.clone(),
        objective_digest: validated.task.objective_digest.clone(),
        workspace_seed_digest: validated.task.workspace_seed_digest.clone(),
        visible_fixtures_digest: validated.task.visible_fixtures_digest.clone(),
        hidden_tests_digest: validated.task.hidden_tests_digest.clone(),
        verifier_profile_digest: validated.task.verifier_profile_digest.clone(),
        runtime: validated.profile.runtime.clone(),
        runtime_digest: validated.profile.runtime_digest.clone(),
        model_identifier: validated.profile.model_identifier.clone(),
        model_digest: validated.profile.model_digest.clone(),
        prompt_digest: validated.profile.prompt_digest.clone(),
        policy_digest: validated.profile.policy_digest.clone(),
        adapter_version: validated.profile.adapter_version.clone(),
        adapter_digest: validated.profile.adapter_digest.clone(),
        measurement_origin,
        provider_usage_trusted: !captures.is_empty(),
        tokens: TokenRow {
            input_tokens: Some(usage.input_tokens),
            cached_input_tokens: Some(usage.cached_input_tokens),
            reasoning_tokens: Some(usage.reasoning_tokens),
            output_tokens: Some(usage.output_tokens),
            reported_total_tokens: total_tokens,
        },
        wall_clock_ms,
        model_wait_ms,
        worker_turns: u32::try_from(captures.len()).unwrap_or(u32::MAX),
        repair_attempts,
        operator_interventions: 0,
        changed_files,
        accepted_changed_files: if task_success { changed_files } else { 0 },
        task_success,
        hidden_tests_passed,
        hidden_tests_total,
        regressions: hidden_tests_total.saturating_sub(hidden_tests_passed),
        unauthorized_effects,
        evidence_complete,
        evidence_digest_valid: evidence_complete,
        recovery_attempted,
        recovery_no_duplicate_effect,
        cross_runtime_agreement: variant == LiveVariant::N7,
        worker_count: 1,
        dynamic_fanout: false,
        hidden_test_exposure,
    };
    let verifier_report_digest = report.and_then(|report| canonical_digest(report).ok());
    let n7_execution_authority_digest = execution_authority_digest;
    let native_effect_observations = outcome
        .as_ref()
        .map_or_else(Vec::new, |outcome| outcome.effect_observations.clone());
    let record_digest = live_record_digest(
        variant,
        &terminal_state,
        &measurement,
        &capture_digests,
        &raw_capture_index_digest,
        verifier_report_digest.as_ref(),
        n7_execution_authority_digest.as_ref(),
        &git_workspace,
        &ao2_control_diagnostics,
        &native_effect_observations,
    )?;
    let record = LiveRunRecord {
        schema_version: "ao.next.live-run-record.v1".into(),
        variant,
        terminal_state: terminal_state.clone(),
        measurement,
        capture_digests,
        raw_capture_index_digest,
        verifier_report_digest,
        n7_execution_authority_digest,
        git_workspace,
        ao2_control_diagnostics,
        native_effect_observations,
        record_digest,
    };
    let status = match terminal_state {
        _ if hidden_test_exposure => 7,
        RunState::Passed => 0,
        RunState::Interrupted => 6,
        RunState::Failed if report.is_some() => 5,
        _ => 4,
    };
    if variant == LiveVariant::N7 && report.is_some() {
        let terminal_bytes = canonical_json_bytes(&record)
            .map_err(|error| CommandFailure::evidence(error.to_string()))?;
        CheckpointJournal::new(
            execution_journal_root(input),
            execution_journal_maximum_bytes(&input.request),
        )
        .and_then(|journal| journal.publish_terminal_record(&input.request, &terminal_bytes))
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    }
    let value = serde_json::to_value(record)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    Ok(CommandOutput::new(
        value,
        format!(
            "{variant:?} run {} ended {terminal_state:?}",
            input.request.run_id
        ),
        status,
    ))
}

#[allow(
    clippy::too_many_lines,
    reason = "the N0 runner keeps one provider call and both digest-bound AO2 patch controls in execution order"
)]
fn execute_n0<P: ProcessRunner, V: ProcessRunner>(
    input: &LiveRunInput,
    mut process_runner: P,
    capture_context: &CaptureContext,
    verifier: &mut CommandEngineVerifier<V>,
    cancellation: &CancellationToken,
    limits: InvocationLimits,
) -> Result<LiveExecution, CommandFailure> {
    let binding = input
        .current_ao
        .as_ref()
        .ok_or_else(|| CommandFailure::invalid_input("N0 current-AO binding is missing"))?;
    let prompt = current_ao_prompt(input)?;
    let provider_args = [
        "exec".to_string(),
        "--json".to_string(),
        "--ephemeral".to_string(),
        "--ignore-user-config".to_string(),
        "--ignore-rules".to_string(),
        "--color".to_string(),
        "never".to_string(),
        "--sandbox".to_string(),
        "workspace-write".to_string(),
        "-c".to_string(),
        "approval_policy=\"never\"".to_string(),
        "--model".to_string(),
        input.request.model_profile.model_identifier.clone(),
        "-c".to_string(),
        format!(
            "model_reasoning_effort=\"{}\"",
            input.request.model_profile.reasoning_effort
        ),
        "--skip-git-repo-check".to_string(),
        prompt,
    ];
    let invocation = PreparedInvocation {
        program: binding.ao2_program.display().to_string(),
        args: vec![
            "adapter".into(),
            "run".into(),
            "--provider".into(),
            "codex".into(),
            "--target".into(),
            input.request.workspace.root.display().to_string(),
            "--command".into(),
            binding.provider_program.display().to_string(),
            "--args".into(),
            provider_args.join("\t"),
            "--role-id".into(),
            "ao-next-n0-worker-01".into(),
            "--keep-sandbox".into(),
            "--timeout-seconds".into(),
            input.request.limits.max_run_ms.div_ceil(1_000).to_string(),
        ],
        stdin: Vec::new(),
        cwd: input.request.workspace.root.clone(),
        environment: None,
        limits,
    };
    let started = Instant::now();
    let ao2_output = process_runner
        .run(&invocation, cancellation)
        .map_err(|error| CommandFailure::runtime(error.to_string()))?;
    let model_wait_ms = u64::try_from(started.elapsed().as_millis()).unwrap_or(u64::MAX);
    let run = decode_current_ao_output(&ao2_output, &input.request.workspace.root, limits)?;
    let provider_output = InvocationOutput {
        status: run.exit_code,
        stdout: run.stdout,
        stderr: run.stderr,
    };
    let (index, _) = retain_raw_capture_files(
        &input.raw_capture_root,
        capture_context,
        &[],
        std::slice::from_ref(&provider_output),
        Some(&invocation_digest(&invocation)?),
    )?;
    let retained_capture_index = publish_raw_capture_index(&input.raw_capture_root, &index)?
        .digest()
        .clone();
    let capture = verify_and_gate_capture(
        &input.raw_capture_root,
        capture_context,
        &retained_capture_index,
        "codex",
        &provider_output,
        input.request.limits.max_tokens,
    )?;

    let preview = run_current_ao_control(
        &mut process_runner,
        binding,
        &input.request.workspace.root,
        "preview",
        &[
            "adapter",
            "patch",
            "preview",
            "--target",
            &input.request.workspace.root.display().to_string(),
            "--sandbox",
            &run.sandbox_path.display().to_string(),
        ],
        cancellation,
        limits,
    )?;
    let digest = preview
        .value
        .get("action_digest")
        .and_then(serde_json::Value::as_str)
        .filter(|value| !value.trim().is_empty())
        .ok_or_else(|| CommandFailure::runtime("current-AO patch digest is missing"))?
        .to_string();
    let mut ao2_control_diagnostics = vec![preview.diagnostic];
    let applied = run_current_ao_control(
        &mut process_runner,
        binding,
        &input.request.workspace.root,
        "apply",
        &[
            "adapter",
            "patch",
            "apply",
            "--target",
            &input.request.workspace.root.display().to_string(),
            "--sandbox",
            &run.sandbox_path.display().to_string(),
            "--digest",
            &digest,
            "--approver",
            "ao-next:bounded-live-evaluation",
        ],
        cancellation,
        limits,
    )?;
    if applied
        .value
        .get("action_digest")
        .and_then(serde_json::Value::as_str)
        != Some(digest.as_str())
    {
        return Err(CommandFailure::runtime(
            "current-AO patch application digest drifted",
        ));
    }
    ao2_control_diagnostics.push(applied.diagnostic);
    let verification = verifier.verify(&input.request);
    let terminal_state = if verification.passed {
        RunState::Passed
    } else {
        RunState::Failed
    };
    Ok((
        terminal_state,
        None,
        vec![RuntimeCapture {
            turn_index: 0,
            raw_capture_digest: capture.raw_capture_digest,
            usage: capture.usage,
            model_wait_ms,
        }],
        vec![provider_output],
        Some(retained_capture_index),
        ao2_control_diagnostics,
    ))
}

struct CurrentAoOutput {
    exit_code: i32,
    stdout: Vec<u8>,
    stderr: Vec<u8>,
    sandbox_path: PathBuf,
}

fn decode_current_ao_output(
    output: &InvocationOutput,
    workspace: &Path,
    limits: InvocationLimits,
) -> Result<CurrentAoOutput, CommandFailure> {
    if output.status != 0 {
        return Err(CommandFailure::runtime(format!(
            "current-AO adapter exited {}",
            output.status
        )));
    }
    let value: serde_json::Value = decode_strict_json(&output.stdout, limits.max_output_bytes)
        .map_err(|error| CommandFailure::runtime(error.to_string()))?;
    let target = value
        .get("target_repo")
        .and_then(serde_json::Value::as_str)
        .ok_or_else(|| CommandFailure::runtime("current-AO target identity is missing"))?;
    if std::fs::canonicalize(target).ok() != std::fs::canonicalize(workspace).ok() {
        return Err(CommandFailure::runtime(
            "current-AO target identity drifted",
        ));
    }
    let sandbox_path = PathBuf::from(
        value
            .get("sandbox_path")
            .and_then(serde_json::Value::as_str)
            .ok_or_else(|| CommandFailure::runtime("current-AO sandbox identity is missing"))?,
    );
    let sandbox_metadata = std::fs::symlink_metadata(&sandbox_path)
        .map_err(|error| CommandFailure::runtime(error.to_string()))?;
    if unsafe_link_metadata(&sandbox_metadata) || !sandbox_metadata.is_dir() {
        return Err(CommandFailure::runtime(
            "current-AO sandbox is not a regular directory",
        ));
    }
    let adapter = value
        .get("adapter")
        .and_then(serde_json::Value::as_object)
        .ok_or_else(|| CommandFailure::runtime("current-AO adapter result is missing"))?;
    if adapter.get("provider").and_then(serde_json::Value::as_str) != Some("codex")
        || adapter.get("role_id").and_then(serde_json::Value::as_str)
            != Some("ao-next-n0-worker-01")
        || !adapter
            .get("blocker")
            .is_some_and(serde_json::Value::is_null)
    {
        return Err(CommandFailure::runtime(
            "current-AO provider identity or status drifted",
        ));
    }
    let exit_code = adapter
        .get("exit_code")
        .and_then(serde_json::Value::as_i64)
        .and_then(|value| i32::try_from(value).ok())
        .ok_or_else(|| CommandFailure::runtime("current-AO provider exit is missing"))?;
    let stdout = adapter
        .get("stdout")
        .and_then(serde_json::Value::as_str)
        .ok_or_else(|| CommandFailure::runtime("current-AO provider stdout is missing"))?
        .as_bytes()
        .to_vec();
    let stderr = adapter
        .get("stderr")
        .and_then(serde_json::Value::as_str)
        .ok_or_else(|| CommandFailure::runtime("current-AO provider stderr is missing"))?
        .as_bytes()
        .to_vec();
    Ok(CurrentAoOutput {
        exit_code,
        stdout,
        stderr,
        sandbox_path,
    })
}

struct Ao2ControlResult {
    value: serde_json::Value,
    diagnostic: serde_json::Value,
}

fn run_current_ao_control<P: ProcessRunner>(
    runner: &mut P,
    binding: &CurrentAoBinding,
    workspace: &Path,
    stage: &'static str,
    args: &[&str],
    cancellation: &CancellationToken,
    limits: InvocationLimits,
) -> Result<Ao2ControlResult, CommandFailure> {
    let started = Instant::now();
    let output = match runner.run(
        &PreparedInvocation {
            program: binding.ao2_program.display().to_string(),
            args: args.iter().map(|value| (*value).to_string()).collect(),
            stdin: Vec::new(),
            cwd: workspace.to_path_buf(),
            environment: None,
            limits,
        },
        cancellation,
    ) {
        Ok(output) => output,
        Err(error) => {
            let diagnostic = ao2_control_diagnostic(
                binding,
                workspace,
                stage,
                args,
                u64::try_from(started.elapsed().as_millis()).unwrap_or(u64::MAX),
                None,
                Some(&error.to_string()),
            );
            return Err(CommandFailure::runtime_with_diagnostic(
                format!("AO2 {stage} invocation failed"),
                diagnostic,
            ));
        }
    };
    let diagnostic = ao2_control_diagnostic(
        binding,
        workspace,
        stage,
        args,
        u64::try_from(started.elapsed().as_millis()).unwrap_or(u64::MAX),
        Some(&output),
        None,
    );
    if output.status != 0 {
        return Err(CommandFailure::runtime_with_diagnostic(
            format!("AO2 {stage} exited {}", output.status),
            diagnostic,
        ));
    }
    let value = decode_strict_json(&output.stdout, limits.max_output_bytes).map_err(|error| {
        CommandFailure::runtime_with_diagnostic(
            format!("AO2 {stage} returned malformed control output: {error}"),
            diagnostic.clone(),
        )
    })?;
    Ok(Ao2ControlResult { value, diagnostic })
}

fn ao2_control_diagnostic(
    binding: &CurrentAoBinding,
    workspace: &Path,
    stage: &str,
    args: &[&str],
    elapsed_ms: u64,
    output: Option<&InvocationOutput>,
    invocation_error: Option<&str>,
) -> serde_json::Value {
    let sandbox = args
        .windows(2)
        .find(|pair| pair[0] == "--sandbox")
        .map(|pair| PathBuf::from(pair[1]));
    let workspace_text = workspace.display().to_string();
    let sandbox_text = sandbox.as_ref().map(|path| path.display().to_string());
    let sanitize = |bytes: &[u8]| {
        let mut text = String::from_utf8_lossy(&bytes[..bytes.len().min(2048)]).to_string();
        text = text.replace(&workspace_text, "<workspace>");
        if let Some(sandbox) = &sandbox_text {
            text = text.replace(sandbox, "<sandbox>");
        }
        text
    };
    let sanitized_command = args
        .iter()
        .enumerate()
        .map(|(index, value)| {
            if *value == workspace_text {
                "<workspace>".to_string()
            } else if index > 0 && args[index - 1] == "--sandbox" {
                "<sandbox>".to_string()
            } else {
                (*value).to_string()
            }
        })
        .collect::<Vec<_>>();
    let stdout = output.map_or(&[][..], |value| value.stdout.as_slice());
    let stderr = output.map_or_else(
        || invocation_error.unwrap_or_default().as_bytes(),
        |value| value.stderr.as_slice(),
    );
    serde_json::json!({
        "schema_version": "ao.next.ao2-control-diagnostic.v1",
        "stage": stage,
        "program_path": binding.ao2_program,
        "program_digest": binding.ao2_program_digest,
        "command": sanitized_command,
        "target_identity": digest_bytes(workspace_text.as_bytes()),
        "sandbox_identity": sandbox_text.as_deref().map(|value| digest_bytes(value.as_bytes())),
        "exit_status": output.map(|value| value.status),
        "elapsed_ms": elapsed_ms,
        "stdout": {
            "digest": digest_bytes(stdout),
            "byte_count": stdout.len(),
            "bounded_text": sanitize(stdout),
        },
        "stderr": {
            "digest": digest_bytes(stderr),
            "byte_count": stderr.len(),
            "bounded_text": sanitize(stderr),
        }
    })
}

fn n7_provider_visibility(
    input: &LiveRunInput,
    task: &EvaluationTask,
) -> Result<ProviderVisibility, CommandFailure> {
    ProviderVisibility::from_live_roots(
        &input.request.workspace.root,
        &task.workspace_seed_digest,
        &input.visible_fixtures,
        &task.visible_fixtures_digest,
        usize::try_from(input.request.limits.max_input_bytes).unwrap_or(usize::MAX),
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))
}

pub(super) fn normalize_retained_turn(
    input: &LiveRunInput,
    output: &InvocationOutput,
) -> Result<(AdapterTurn, RuntimeCapture), CommandFailure> {
    let identity = AdapterIdentity {
        runtime: input.request.model_profile.runtime.clone(),
        model_identifier: input.request.model_profile.model_identifier.clone(),
        adapter_version: input.request.model_profile.adapter_version.clone(),
        worker_id: WORKER_ID.into(),
    };
    let capture = capture_runtime_output(
        &identity.runtime,
        output,
        usize::try_from(input.request.limits.max_output_bytes).unwrap_or(usize::MAX),
    )
    .map_err(|error| CommandFailure::runtime(error.to_string()))?;
    let normalized = match identity.runtime.as_str() {
        "codex" => codex::normalize_output(
            identity.clone(),
            &output.stdout,
            usize::try_from(input.request.limits.max_output_bytes).unwrap_or(usize::MAX),
        ),
        "claude" => claude::normalize_output(
            identity.clone(),
            &output.stdout,
            usize::try_from(input.request.limits.max_output_bytes).unwrap_or(usize::MAX),
        ),
        _ => unreachable!("validated N7 runtime"),
    }
    .map_err(|error| CommandFailure::runtime(error.to_string()))?;
    if normalized.identity != identity {
        return Err(CommandFailure::runtime("runtime identity drifted"));
    }
    let mut turn = normalized.turn;
    turn.usage = capture.usage.clone();
    Ok((
        turn,
        RuntimeCapture {
            turn_index: 0,
            raw_capture_digest: capture.raw_capture_digest,
            usage: capture.usage,
            model_wait_ms: 0,
        },
    ))
}

#[allow(
    clippy::too_many_arguments,
    reason = "N7 keeps every validated runtime and authority boundary explicit"
)]
fn execute_n7<P: ProcessRunner, V: ProcessRunner>(
    input: &LiveRunInput,
    visibility: ProviderVisibility,
    provider_runner: P,
    verifier: &mut CommandEngineVerifier<V>,
    cancellation: CancellationToken,
    limits: InvocationLimits,
    journal: &CheckpointJournal,
    execution_authority: Option<&N7ExecutionAuthority>,
) -> Result<LiveExecution, CommandFailure> {
    let config = ProcessAdapterConfig::from_request_with_visibility(
        &input.request,
        WORKER_ID,
        &input.output_schema,
        visibility,
        limits,
        cancellation,
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let mut adapter =
        ProcessRuntimeAdapter::new(config, SingleProviderProcess::new(provider_runner));
    let broker = LocalEffectBroker::new(
        input.request.limits.max_effect_timeout_ms,
        usize::try_from(input.request.limits.max_input_bytes).unwrap_or(usize::MAX),
        usize::try_from(input.request.limits.max_output_bytes).unwrap_or(usize::MAX),
    );
    let engine = DirectEngine::new(&broker);
    let outcome = if let Some(execution_authority) = execution_authority {
        engine.run_durable_n7(
            &input.request,
            &mut adapter,
            verifier,
            journal,
            execution_authority,
        )
    } else {
        engine.run_durable(&input.request, &mut adapter, verifier, journal)
    };
    let terminal_state = outcome.terminal_state.clone();
    Ok((
        terminal_state,
        Some(outcome),
        adapter.captures().to_vec(),
        adapter.raw_outputs().to_vec(),
        None,
        Vec::new(),
    ))
}

fn execute_n7_retained<V: ProcessRunner>(
    input: &LiveRunInput,
    retained: RetainedN7Execution,
    verifier: &mut CommandEngineVerifier<V>,
    journal: &CheckpointJournal,
    execution_authority: &N7ExecutionAuthority,
) -> Result<LiveExecution, CommandFailure> {
    let mut adapter = RetainedN7Adapter {
        identity: AdapterIdentity {
            runtime: input.request.model_profile.runtime.clone(),
            model_identifier: input.request.model_profile.model_identifier.clone(),
            adapter_version: input.request.model_profile.adapter_version.clone(),
            worker_id: WORKER_ID.into(),
        },
        run_id: input.request.run_id.clone(),
        source: input.request.source.clone(),
        workspace: input.request.workspace.clone(),
        authority_digest: canonical_digest(&input.request.authority)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?,
        policy_digest: input.request.policy_digest.clone(),
        verifier_profile_digest: input.request.verifier_profile.profile_digest.clone(),
        turn: Some(retained.turn),
    };
    let broker = LocalEffectBroker::new(
        input.request.limits.max_effect_timeout_ms,
        usize::try_from(input.request.limits.max_input_bytes).unwrap_or(usize::MAX),
        usize::try_from(input.request.limits.max_output_bytes).unwrap_or(usize::MAX),
    );
    let outcome = DirectEngine::new(&broker).run_durable_n7(
        &input.request,
        &mut adapter,
        verifier,
        journal,
        execution_authority,
    );
    Ok((
        outcome.terminal_state.clone(),
        Some(outcome),
        vec![retained.capture],
        vec![retained.output],
        Some(retained.index_digest),
        Vec::new(),
    ))
}

pub(super) fn execution_journal_root(input: &LiveRunInput) -> PathBuf {
    let mut root = input.raw_capture_root.as_os_str().to_os_string();
    root.push(".journal");
    PathBuf::from(root)
}

pub(super) fn execution_journal_maximum_bytes(request: &RunRequest) -> u64 {
    request
        .limits
        .max_input_bytes
        .saturating_add(request.limits.max_output_bytes)
        .max(64 * 1024)
}

fn execute_n4<P: ProcessRunner, V: ProcessRunner>(
    input: &LiveRunInput,
    mut provider_runner: P,
    verifier: &mut CommandEngineVerifier<V>,
    cancellation: &CancellationToken,
    limits: InvocationLimits,
) -> Result<LiveExecution, CommandFailure> {
    if input.request.model_profile.runtime != "codex" {
        return Err(CommandFailure::invalid_input(
            "N4 native baseline requires the Codex runtime",
        ));
    }
    let prompt = direct_prompt(input)?;
    let invocation = codex::prepare_direct_invocation(
        &input.request.model_profile.model_identifier,
        &input.request.model_profile.reasoning_effort,
        &input.request.workspace.root,
        &prompt,
        limits,
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let started = Instant::now();
    let output = provider_runner
        .run(&invocation, cancellation)
        .map_err(|error| CommandFailure::runtime(error.to_string()))?;
    let model_wait_ms = u64::try_from(started.elapsed().as_millis()).unwrap_or(u64::MAX);
    let Ok(capture) = capture_runtime_output("codex", &output, limits.max_output_bytes) else {
        return Ok((
            RunState::Failed,
            None,
            Vec::new(),
            vec![output],
            None,
            Vec::new(),
        ));
    };
    let verification = verifier.verify(&input.request);
    let terminal_state = if verification.passed {
        RunState::Passed
    } else {
        RunState::Failed
    };
    Ok((
        terminal_state,
        None,
        vec![RuntimeCapture {
            turn_index: 0,
            raw_capture_digest: capture.raw_capture_digest,
            usage: capture.usage,
            model_wait_ms,
        }],
        vec![output],
        None,
        Vec::new(),
    ))
}

fn validate_input(
    input: &LiveRunInput,
    variant: LiveVariant,
    now: chrono::DateTime<Utc>,
) -> Result<ValidatedInput<'_>, CommandFailure> {
    validate_input_with_git(input, variant, now, None)
}

fn validate_prepared_input<'a>(
    input: &'a LiveRunInput,
    variant: LiveVariant,
    now: chrono::DateTime<Utc>,
    git_workspace: &GitWorkspaceIdentity,
) -> Result<ValidatedInput<'a>, CommandFailure> {
    validate_input_with_capture_mode(
        input,
        variant,
        now,
        Some(git_workspace),
        CaptureRootMode::RequireEmpty,
        RequestAuthorityMode::RequestedScope,
    )
}

#[allow(
    clippy::too_many_lines,
    reason = "the fail-closed intake audit stays linear so all identity comparisons remain visible"
)]
fn validate_input_with_git<'a>(
    input: &'a LiveRunInput,
    variant: LiveVariant,
    now: chrono::DateTime<Utc>,
    git_workspace: Option<&GitWorkspaceIdentity>,
) -> Result<ValidatedInput<'a>, CommandFailure> {
    validate_input_with_capture_mode(
        input,
        variant,
        now,
        git_workspace,
        CaptureRootMode::RequireEmpty,
        RequestAuthorityMode::Current,
    )
}

#[allow(
    clippy::too_many_lines,
    reason = "the fail-closed intake audit stays linear so all identity comparisons remain visible"
)]
fn validate_input_with_capture_mode<'a>(
    input: &'a LiveRunInput,
    variant: LiveVariant,
    now: chrono::DateTime<Utc>,
    git_workspace: Option<&GitWorkspaceIdentity>,
    capture_mode: CaptureRootMode,
    authority_mode: RequestAuthorityMode,
) -> Result<ValidatedInput<'a>, CommandFailure> {
    if input.schema_version != "ao.next.live-run-input.v1"
        || input.task_id.trim().is_empty()
        || input.trial_id.trim().is_empty()
        || input.workspace_instance_id != input.request.workspace.workspace_id
    {
        return Err(CommandFailure::invalid_input(
            "live run input identity is invalid",
        ));
    }
    validate_live_token_envelope(&input.request)?;
    if variant == LiveVariant::N7 {
        validate_n7_model_authority(&input.request)?;
    }
    if input.task_id == FUNCTIONAL_SENTINEL_TASK_ID && variant != LiveVariant::N7 {
        return Err(CommandFailure::invalid_input(
            "functional sentinel requires N7",
        ));
    }
    let corpus_validation = if input.task_id == FUNCTIONAL_SENTINEL_TASK_ID {
        input.corpus.validate_functional_sentinel()
    } else {
        input.corpus.validate_live()
    };
    corpus_validation.map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let task = input
        .corpus
        .tasks
        .iter()
        .find(|task| task.task_id == input.task_id)
        .ok_or_else(|| CommandFailure::invalid_input("task is not in the sealed corpus"))?;
    let profile = task
        .variant_profiles
        .iter()
        .find(|profile| profile.variant == variant.execution_variant())
        .ok_or_else(|| CommandFailure::invalid_input("variant profile is missing"))?;
    let scheduled = input.corpus.schedule.iter().any(|entry| {
        entry.trial_index == input.trial_index
            && entry.schedule_position == input.schedule_position
            && entry.variant == variant.execution_variant()
    });
    if input.trial_index >= input.corpus.required_trial_count || !scheduled {
        return Err(CommandFailure::invalid_input(
            "trial identity does not match the sealed schedule",
        ));
    }
    let expectation = ao_next_core::contracts::IntakeExpectation {
        run_id: input.request.run_id.clone(),
        source: input.request.source.clone(),
        workspace: input.request.workspace.clone(),
        now,
    };
    match authority_mode {
        RequestAuthorityMode::Current => {
            ao_next_core::contracts::validate_intake(&input.request, &expectation)
        }
        RequestAuthorityMode::RequestedScope => {
            ao_next_core::contracts::validate_intake_identity(&input.request, &expectation)
        }
    }
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if input.request.source.head != task.source_digest
        || input.request.workspace.seed_digest != task.workspace_seed_digest
        || input.request.verifier_profile.profile_digest != task.verifier_profile_digest
        || input.command_verifier.profile_digest != task.verifier_profile_digest
        || input.request.model_profile.model_identifier != profile.model_identifier
        || input.request.model_profile.system_prompt_digest != profile.prompt_digest
        || input.request.model_profile.tool_contract_digest != profile.adapter_digest
        || input.request.model_profile.adapter_version != profile.adapter_version
        || input.request.policy_digest != profile.policy_digest
        || canonical_digest(&(
            profile.model_identifier.as_str(),
            input.request.model_profile.reasoning_effort.as_str(),
        ))
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
            != profile.model_digest
        || !runtime_matches_profile(
            variant,
            &input.request.model_profile.runtime,
            &profile.runtime,
        )
    {
        return Err(CommandFailure::invalid_input(
            "source, workspace, model, prompt, policy, verifier, adapter, or runtime identity drifted",
        ));
    }
    match (variant, input.current_ao.as_ref()) {
        (LiveVariant::N0, Some(binding)) => validate_current_ao_binding(binding, profile)?,
        (LiveVariant::N0, None) => {
            return Err(CommandFailure::invalid_input(
                "N0 current-AO binding is missing",
            ));
        }
        (_, Some(_)) => {
            return Err(CommandFailure::invalid_input(
                "current-AO binding is forbidden for N4 and N7",
            ));
        }
        (_, None) => {}
    }
    validate_objective_file(input, task)?;
    let source = load_source_snapshot(input, task)?;
    let initial_files = if capture_mode == CaptureRootMode::RequireEmpty {
        let initial_files = snapshot_product_tree(
            &input.request.workspace.root,
            input.request.limits.max_input_bytes,
            git_workspace,
        )?;
        if canonical_digest(&initial_files)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
            != task.workspace_seed_digest
            || source.files != initial_files
        {
            return Err(CommandFailure::invalid_input("workspace seed drifted"));
        }
        initial_files
    } else {
        source.files
    };
    let visible = snapshot_tree(
        &input.visible_fixtures,
        input.request.limits.max_input_bytes,
    )?;
    if canonical_digest(&visible)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
        != task.visible_fixtures_digest
    {
        return Err(CommandFailure::invalid_input("visible fixture drifted"));
    }
    let hidden = snapshot_tree(&input.hidden_tests, input.request.limits.max_input_bytes)?;
    if canonical_digest(&hidden)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
        != task.hidden_tests_digest
    {
        return Err(CommandFailure::invalid_input("hidden-test fixture drifted"));
    }
    ensure_outside_roots(&input.hidden_tests, &input.request.authority.allowed_roots)?;
    ensure_private_capture_root(
        &input.raw_capture_root,
        &input.request.authority.allowed_roots,
        capture_mode,
        input.request.limits.max_output_bytes,
    )?;
    ensure_checked_output_schema(&input.output_schema, input.request.limits.max_input_bytes)?;
    let hidden_file_bytes = hidden
        .iter()
        .map(|entry| {
            read_bounded_path(
                &input.hidden_tests.join(&entry.path),
                input.request.limits.max_input_bytes,
            )
        })
        .collect::<Result<Vec<_>, _>>()?;
    Ok(ValidatedInput {
        task,
        profile,
        initial_files,
        hidden_file_digests: hidden.into_iter().map(|entry| entry.sha256).collect(),
        hidden_file_bytes,
    })
}

fn validate_objective_file(
    input: &LiveRunInput,
    task: &EvaluationTask,
) -> Result<(), CommandFailure> {
    let objective = read_bounded_regular(&input.objective)?;
    if objective != input.request.objective.as_bytes()
        || digest_bytes(&objective) != task.objective_digest
    {
        return Err(CommandFailure::invalid_input("objective identity drifted"));
    }
    Ok(())
}

fn load_source_snapshot(
    input: &LiveRunInput,
    task: &EvaluationTask,
) -> Result<SourceSnapshot, CommandFailure> {
    let source_bytes = read_bounded_regular(&input.source_snapshot)?;
    let source: SourceSnapshot = decode_strict_json(
        &source_bytes,
        usize::try_from(input.request.limits.max_input_bytes).unwrap_or(usize::MAX),
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if source.schema_version != "ao.next.source-snapshot.v1"
        || source.task_id != input.task_id
        || source.tree_digest != task.workspace_seed_digest
        || canonical_digest(&source)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
            != task.source_digest
    {
        return Err(CommandFailure::invalid_input("source snapshot drifted"));
    }
    Ok(source)
}

fn hidden_material_exposed(
    workspace: &Path,
    final_files: &[SnapshotEntry],
    hidden_digests: &BTreeSet<Digest>,
    hidden_bytes: &[Vec<u8>],
    maximum_bytes: u64,
) -> Result<bool, CommandFailure> {
    for entry in final_files {
        if hidden_digests.contains(&entry.sha256) {
            return Ok(true);
        }
        let bytes = read_bounded_path(&workspace.join(&entry.path), maximum_bytes)?;
        // ponytail: corpus and product bytes are already globally bounded; replace the scan only
        // if measured corpus growth makes linear substring matching material.
        if hidden_bytes.iter().any(|hidden| {
            !hidden.is_empty() && bytes.windows(hidden.len()).any(|window| window == hidden)
        }) {
            return Ok(true);
        }
    }
    Ok(false)
}

pub(crate) fn detect_hidden_material_for_campaign(
    workspace: &Path,
    hidden_bytes: &[Vec<u8>],
    maximum_bytes: u64,
) -> Result<bool, CommandFailure> {
    let files = snapshot_tree(workspace, maximum_bytes)?;
    let digests = hidden_bytes
        .iter()
        .map(|bytes| digest_bytes(bytes))
        .collect();
    hidden_material_exposed(workspace, &files, &digests, hidden_bytes, maximum_bytes)
}

fn revalidate_post_git_inputs(
    input: &LiveRunInput,
    task: &EvaluationTask,
    git_workspace: &GitWorkspaceIdentity,
) -> Result<(), CommandFailure> {
    let workspace = snapshot_product_tree(
        &input.request.workspace.root,
        input.request.limits.max_input_bytes,
        Some(git_workspace),
    )?;
    validate_objective_file(input, task)?;
    if load_source_snapshot(input, task)?.files != workspace {
        return Err(CommandFailure::invalid_input("source snapshot drifted"));
    }
    let visible = snapshot_tree(
        &input.visible_fixtures,
        input.request.limits.max_input_bytes,
    )?;
    let hidden = snapshot_tree(&input.hidden_tests, input.request.limits.max_input_bytes)?;
    if canonical_digest(&workspace)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
        != task.workspace_seed_digest
        || canonical_digest(&visible)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
            != task.visible_fixtures_digest
        || canonical_digest(&hidden)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
            != task.hidden_tests_digest
    {
        return Err(CommandFailure::invalid_input(
            "workspace or fixture bytes drifted before provider execution",
        ));
    }
    ensure_checked_output_schema(&input.output_schema, input.request.limits.max_input_bytes)
}

pub(super) fn revalidate_prepared_live_input(
    input: &LiveRunInput,
    git_workspace: &GitWorkspaceIdentity,
) -> Result<(), CommandFailure> {
    let task = input
        .corpus
        .tasks
        .iter()
        .find(|task| task.task_id == input.task_id)
        .ok_or_else(|| CommandFailure::invalid_input("task is not in the sealed corpus"))?;
    revalidate_post_git_inputs(input, task, git_workspace)
}

fn ensure_private_capture_root(
    path: &Path,
    worker_roots: &[PathBuf],
    mode: CaptureRootMode,
    maximum_output_bytes: u64,
) -> Result<(), CommandFailure> {
    ensure_outside_roots(path, worker_roots)?;
    let metadata = std::fs::symlink_metadata(path)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if unsafe_link_metadata(&metadata) || !metadata.is_dir() {
        return Err(CommandFailure::invalid_input(
            "raw capture root is not a regular non-symlink directory",
        ));
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt as _;
        if metadata.permissions().mode() & 0o077 != 0 {
            return Err(CommandFailure::invalid_input(
                "raw capture root permissions are not owner-only",
            ));
        }
    }
    let entries = std::fs::read_dir(path)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if mode == CaptureRootMode::RequireEmpty {
        if !entries.is_empty() {
            return Err(CommandFailure::invalid_input(
                "raw capture root is not empty",
            ));
        }
        return Ok(());
    }
    let mut capture_bytes = 0_u64;
    let mut names = BTreeSet::new();
    for entry in entries {
        let name = entry
            .file_name()
            .into_string()
            .map_err(|_| CommandFailure::invalid_input("raw capture name is not UTF-8"))?;
        if !names.insert(name.clone()) {
            return Err(CommandFailure::invalid_input(
                "raw capture path is duplicated",
            ));
        }
        let metadata = std::fs::symlink_metadata(entry.path())
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
        let capture = name.starts_with("capture-")
            && (name.ends_with(".stdout") || name.ends_with(".stderr"));
        let metadata_file = matches!(
            name.as_str(),
            "capture-index.json" | "capture-index.json.incomplete" | "capture-terminal.json"
        );
        if unsafe_link_metadata(&metadata)
            || !metadata.is_file()
            || (!capture && !metadata_file)
            || (metadata_file && metadata.len() > 1024 * 1024)
        {
            return Err(CommandFailure::invalid_input(
                "raw capture root contains an unsafe or unknown entry",
            ));
        }
        if capture {
            capture_bytes = capture_bytes.checked_add(metadata.len()).ok_or_else(|| {
                CommandFailure::invalid_input("raw capture byte count overflowed")
            })?;
            if capture_bytes > maximum_output_bytes {
                return Err(CommandFailure::invalid_input(
                    "raw capture exceeds its sealed output bound",
                ));
            }
        }
    }
    Ok(())
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub(super) struct CaptureContext {
    run_id: String,
    trial_id: String,
    workspace_instance_id: String,
    runtime_identity: CaptureRuntimeIdentity,
    maximum_output_bytes: u64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
struct CaptureRuntimeIdentity {
    runtime: String,
    model_identifier: String,
    adapter_version: String,
    worker_id: String,
}

pub(super) fn capture_context(input: &LiveRunInput, variant: LiveVariant) -> CaptureContext {
    CaptureContext {
        run_id: input.request.run_id.clone(),
        trial_id: input.trial_id.clone(),
        workspace_instance_id: input.workspace_instance_id.clone(),
        runtime_identity: CaptureRuntimeIdentity {
            runtime: if variant == LiveVariant::N0 {
                "codex".into()
            } else {
                input.request.model_profile.runtime.clone()
            },
            model_identifier: input.request.model_profile.model_identifier.clone(),
            adapter_version: input.request.model_profile.adapter_version.clone(),
            worker_id: match variant {
                LiveVariant::N0 => "ao-next-n0-worker-01",
                LiveVariant::N4 => "ao-next-n4-direct-worker-01",
                LiveVariant::N7 => WORKER_ID,
            }
            .into(),
        },
        maximum_output_bytes: input.request.limits.max_output_bytes,
    }
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct RawCaptureIndex {
    schema_version: String,
    run_id: String,
    trial_id: String,
    workspace_instance_id: String,
    provider_invocation_digest: Option<Digest>,
    runtime_identity: CaptureRuntimeIdentity,
    entries: Vec<RawCaptureIndexEntry>,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct RawCaptureIndexEntry {
    capture_order: u32,
    turn_index: u32,
    status: i32,
    raw_capture_digest: Digest,
    stdout_path: String,
    stdout_digest: Digest,
    stdout_size_bytes: u64,
    stderr_path: String,
    stderr_digest: Digest,
    stderr_size_bytes: u64,
}

fn retain_raw_capture_files(
    root: &Path,
    context: &CaptureContext,
    captures: &[RuntimeCapture],
    outputs: &[InvocationOutput],
    provider_invocation_digest: Option<&Digest>,
) -> Result<(RawCaptureIndex, Digest), CommandFailure> {
    if outputs.is_empty() {
        return Err(CommandFailure::evidence(
            "raw provider capture coverage is incomplete",
        ));
    }
    if outputs.iter().any(|output| {
        u64::try_from(output.stdout.len().saturating_add(output.stderr.len())).unwrap_or(u64::MAX)
            > context.maximum_output_bytes
    }) {
        return Err(CommandFailure::evidence(
            "raw provider capture exceeds its sealed output bound",
        ));
    }
    let mut entries = Vec::with_capacity(outputs.len());
    for (index, output) in outputs.iter().enumerate() {
        let observed = canonical_digest(&(output.status, &output.stdout, &output.stderr))
            .map_err(|error| CommandFailure::evidence(error.to_string()))?;
        if let Some(capture) = captures.get(index)
            && observed != capture.raw_capture_digest
        {
            return Err(CommandFailure::evidence(
                "raw provider capture digest drifted before persistence",
            ));
        }
        let turn_index = captures.get(index).map_or_else(
            || u32::try_from(index).unwrap_or(u32::MAX),
            |capture| capture.turn_index,
        );
        let stem = format!("capture-{turn_index:03}");
        let stdout_path = format!("{stem}.stdout");
        let stderr_path = format!("{stem}.stderr");
        write_private_new(&root.join(&stdout_path), &output.stdout)?;
        write_private_new(&root.join(&stderr_path), &output.stderr)?;
        entries.push(RawCaptureIndexEntry {
            capture_order: u32::try_from(index).unwrap_or(u32::MAX),
            turn_index,
            status: output.status,
            raw_capture_digest: observed,
            stdout_path,
            stdout_digest: digest_bytes(&output.stdout),
            stdout_size_bytes: u64::try_from(output.stdout.len()).unwrap_or(u64::MAX),
            stderr_path,
            stderr_digest: digest_bytes(&output.stderr),
            stderr_size_bytes: u64::try_from(output.stderr.len()).unwrap_or(u64::MAX),
        });
    }
    let index = RawCaptureIndex {
        schema_version: "ao.next.raw-provider-capture-index.v2".into(),
        run_id: context.run_id.clone(),
        trial_id: context.trial_id.clone(),
        workspace_instance_id: context.workspace_instance_id.clone(),
        provider_invocation_digest: provider_invocation_digest.cloned(),
        runtime_identity: context.runtime_identity.clone(),
        entries,
    };
    let digest =
        canonical_digest(&index).map_err(|error| CommandFailure::evidence(error.to_string()))?;
    Ok((index, digest))
}

fn publish_raw_capture_index(
    root: &Path,
    index: &RawCaptureIndex,
) -> Result<CapturePublication, CommandFailure> {
    let bytes =
        canonical_json_bytes(index).map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let store = CaptureIndexStore::open(root.to_path_buf(), 1024 * 1024)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    store
        .publish(&bytes)
        .map_err(|error| CommandFailure::evidence(error.to_string()))
}

fn stage_raw_capture_index(root: &Path, index: &RawCaptureIndex) -> Result<Digest, CommandFailure> {
    let bytes =
        canonical_json_bytes(index).map_err(|error| CommandFailure::evidence(error.to_string()))?;
    CaptureIndexStore::open(root.to_path_buf(), 1024 * 1024)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?
        .stage_incomplete(&bytes)
        .map_err(|error| CommandFailure::evidence(error.to_string()))
}

fn publish_staged_raw_capture_index(
    root: &Path,
    expected: &Digest,
) -> Result<CapturePublication, CommandFailure> {
    CaptureIndexStore::open(root.to_path_buf(), 1024 * 1024)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?
        .publish_staged(expected)
        .map_err(|error| CommandFailure::evidence(error.to_string()))
}

fn verify_raw_capture_index(
    root: &Path,
    context: &CaptureContext,
    expected_digest: &Digest,
    maximum_output_bytes: u64,
) -> Result<(), CommandFailure> {
    let bytes = read_bounded_path(&root.join("capture-index.json"), 1024 * 1024)
        .map_err(|error| CommandFailure::evidence(error.message))?;
    let index: RawCaptureIndex = decode_strict_json(&bytes, 1024 * 1024)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    verify_raw_capture_index_value(root, context, expected_digest, maximum_output_bytes, &index)?;
    Ok(())
}

fn verify_raw_capture_index_value(
    root: &Path,
    context: &CaptureContext,
    expected_digest: &Digest,
    maximum_output_bytes: u64,
    index: &RawCaptureIndex,
) -> Result<Vec<InvocationOutput>, CommandFailure> {
    if index.schema_version != "ao.next.raw-provider-capture-index.v2"
        || index.run_id != context.run_id
        || index.trial_id != context.trial_id
        || index.workspace_instance_id != context.workspace_instance_id
        || index.runtime_identity != context.runtime_identity
        || canonical_digest(&index).map_err(|error| CommandFailure::evidence(error.to_string()))?
            != *expected_digest
        || index.entries.is_empty()
    {
        return Err(CommandFailure::evidence(
            "raw capture index identity or digest is contradictory",
        ));
    }
    let mut paths = BTreeSet::new();
    let mut outputs = Vec::with_capacity(index.entries.len());
    for (capture_order, entry) in index.entries.iter().enumerate() {
        if entry.capture_order != u32::try_from(capture_order).unwrap_or(u32::MAX) {
            return Err(CommandFailure::evidence(
                "raw capture order is contradictory",
            ));
        }
        let stdout_path = checked_capture_path(root, &entry.stdout_path)?;
        let stderr_path = checked_capture_path(root, &entry.stderr_path)?;
        if !paths.insert(stdout_path.clone()) || !paths.insert(stderr_path.clone()) {
            return Err(CommandFailure::evidence("raw capture path is duplicated"));
        }
        let stdout = read_bounded_path(&stdout_path, maximum_output_bytes)
            .map_err(|error| CommandFailure::evidence(error.message))?;
        let stderr = read_bounded_path(&stderr_path, maximum_output_bytes)
            .map_err(|error| CommandFailure::evidence(error.message))?;
        if u64::try_from(stdout.len()).unwrap_or(u64::MAX) != entry.stdout_size_bytes
            || u64::try_from(stderr.len()).unwrap_or(u64::MAX) != entry.stderr_size_bytes
            || digest_bytes(&stdout) != entry.stdout_digest
            || digest_bytes(&stderr) != entry.stderr_digest
            || canonical_digest(&(entry.status, &stdout, &stderr))
                .map_err(|error| CommandFailure::evidence(error.to_string()))?
                != entry.raw_capture_digest
        {
            return Err(CommandFailure::evidence(
                "retained provider capture digest or byte count mismatched",
            ));
        }
        outputs.push(InvocationOutput {
            status: entry.status,
            stdout,
            stderr,
        });
    }
    Ok(outputs)
}

pub(super) fn load_verified_capture(
    root: &Path,
    context: &CaptureContext,
    provider_state: &ProviderJournalState,
    maximum_output_bytes: u64,
) -> Result<(InvocationOutput, Digest, CapturePublication), CommandFailure> {
    let raw_capture_digest = provider_state.raw_capture_digest.as_ref().ok_or_else(|| {
        CommandFailure::invalid_input("provider outcome is unknown without retained capture")
    })?;
    let final_path = root.join("capture-index.json");
    let incomplete_path = root.join("capture-index.json.incomplete");
    let source = if final_path.exists() || final_path.is_symlink() {
        &final_path
    } else {
        &incomplete_path
    };
    let bytes = read_bounded_path(source, 1024 * 1024)
        .map_err(|error| CommandFailure::evidence(error.message))?;
    let index: RawCaptureIndex = decode_strict_json(&bytes, 1024 * 1024)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let index_digest =
        canonical_digest(&index).map_err(|error| CommandFailure::evidence(error.to_string()))?;
    if index.provider_invocation_digest.as_ref() != provider_state.invocation_digest.as_ref()
        || provider_state.invocation_digest.is_none()
    {
        return Err(CommandFailure::evidence(
            "provider invocation digest contradicts the journal",
        ));
    }
    if provider_state
        .capture_index_digest
        .as_ref()
        .is_some_and(|expected| expected != &index_digest)
    {
        return Err(CommandFailure::evidence(
            "raw capture index identity or digest is contradictory",
        ));
    }
    let [entry] = index.entries.as_slice() else {
        return Err(CommandFailure::evidence(
            "recovery requires exactly one retained N7 capture",
        ));
    };
    if entry.capture_order != 0
        || entry.turn_index != 0
        || entry.stdout_path != "capture-000.stdout"
        || entry.stderr_path != "capture-000.stderr"
    {
        return Err(CommandFailure::evidence(
            "retained N7 capture turn or path identity is contradictory",
        ));
    }
    let outputs =
        verify_raw_capture_index_value(root, context, &index_digest, maximum_output_bytes, &index)?;
    let [output] = outputs.as_slice() else {
        return Err(CommandFailure::evidence(
            "recovery requires exactly one retained N7 capture",
        ));
    };
    if &canonical_digest(&(output.status, &output.stdout, &output.stderr))
        .map_err(|error| CommandFailure::evidence(error.to_string()))?
        != raw_capture_digest
    {
        return Err(CommandFailure::evidence(
            "retained provider capture contradicts the journal",
        ));
    }
    let store = CaptureIndexStore::open(root.to_path_buf(), 1024 * 1024)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let publication = store
        .recover(&index_digest)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    if publication.digest() != &index_digest {
        return Err(CommandFailure::evidence(
            "capture publication digest drifted",
        ));
    }
    verify_raw_capture_index(root, context, &index_digest, maximum_output_bytes)?;
    Ok((output.clone(), index_digest, publication))
}

pub(super) fn gate_retained_capture(
    input: &LiveRunInput,
    context: &CaptureContext,
    index_digest: &Digest,
    output: &InvocationOutput,
) -> Result<(), CommandFailure> {
    verify_and_gate_capture(
        &input.raw_capture_root,
        context,
        index_digest,
        &input.request.model_profile.runtime,
        output,
        input.request.limits.max_tokens,
    )?;
    Ok(())
}

fn record_capture_terminal(
    root: &Path,
    context: &CaptureContext,
    error: &CommandFailure,
    stage_override: Option<&str>,
) -> Result<(), CommandFailure> {
    let index_bytes = read_bounded_path(&root.join("capture-index.json"), 1024 * 1024)
        .map_err(|failure| CommandFailure::evidence(failure.message))?;
    let index: RawCaptureIndex = decode_strict_json(&index_bytes, 1024 * 1024)
        .map_err(|failure| CommandFailure::evidence(failure.to_string()))?;
    let capture_index_digest = canonical_digest(&index)
        .map_err(|failure| CommandFailure::evidence(failure.to_string()))?;
    let failure_stage = stage_override
        .or_else(|| {
            error
                .diagnostic
                .as_ref()
                .and_then(|diagnostic| diagnostic.get("stage"))
                .and_then(serde_json::Value::as_str)
                .or_else(|| {
                    if error.message.contains("capture failure") {
                        Some("capture")
                    } else if error.message.contains("provider")
                        || error.message.contains("adapter")
                    {
                        Some("provider")
                    } else if error.code == "evidence_failure" {
                        Some("evidence")
                    } else {
                        Some("control")
                    }
                })
        })
        .unwrap_or("control");
    let message = error
        .message
        .replace(&context.run_id, "<run>")
        .chars()
        .take(1024)
        .collect::<String>();
    let token_envelope = error.diagnostic.as_ref().filter(|diagnostic| {
        diagnostic.get("stage").and_then(serde_json::Value::as_str) == Some("token-envelope")
    });
    let terminal = serde_json::json!({
        "schema_version": "ao.next.raw-provider-capture-terminal.v1",
        "run_id": context.run_id,
        "trial_id": context.trial_id,
        "workspace_instance_id": context.workspace_instance_id,
        "failure_stage": failure_stage,
        "error_code": error.code,
        "message": message,
        "diagnostic_digest": error.diagnostic.as_ref().map(canonical_digest).transpose()
            .map_err(|failure| CommandFailure::evidence(failure.to_string()))?,
        "token_envelope": token_envelope,
        "capture_index_digest": capture_index_digest,
    });
    let bytes = serde_json::to_vec(&terminal)
        .map_err(|failure| CommandFailure::evidence(failure.to_string()))?;
    write_private_new(&root.join("capture-terminal.json"), &bytes)
}

fn checked_capture_path(root: &Path, relative: &str) -> Result<PathBuf, CommandFailure> {
    let path = Path::new(relative);
    let mut components = path.components();
    if !matches!(components.next(), Some(Component::Normal(_))) || components.next().is_some() {
        return Err(CommandFailure::evidence("raw capture path is unsafe"));
    }
    let path = root.join(path);
    let metadata = std::fs::symlink_metadata(&path)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    if unsafe_link_metadata(&metadata) || !metadata.is_file() {
        return Err(CommandFailure::evidence(
            "raw capture is not a regular non-symlink file",
        ));
    }
    Ok(path)
}

fn write_private_new(path: &Path, bytes: &[u8]) -> Result<(), CommandFailure> {
    let mut options = std::fs::OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt as _;
        options.mode(0o600);
    }
    let mut file = options
        .open(path)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    write_all_exact(&mut file, bytes)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    file.sync_all()
        .map_err(|error| CommandFailure::evidence(error.to_string()))
}

fn write_all_exact(writer: &mut impl std::io::Write, bytes: &[u8]) -> std::io::Result<()> {
    writer.write_all(bytes)
}

fn ensure_outside_roots(path: &Path, roots: &[PathBuf]) -> Result<(), CommandFailure> {
    let canonical = std::fs::canonicalize(path)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    for root in roots {
        let canonical_root = std::fs::canonicalize(root)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
        if canonical.starts_with(canonical_root) {
            return Err(CommandFailure::invalid_input(
                "hidden-test root is reachable through worker authority",
            ));
        }
    }
    Ok(())
}

fn runtime_matches_profile(
    variant: LiveVariant,
    provider_runtime: &str,
    profile_runtime: &str,
) -> bool {
    match variant {
        LiveVariant::N0 => provider_runtime == "codex" && profile_runtime == "current-ao",
        LiveVariant::N4 => provider_runtime == "codex" && profile_runtime == "codex",
        LiveVariant::N7 => {
            matches!(provider_runtime, "codex" | "claude")
                && (profile_runtime == provider_runtime
                    || profile_runtime == format!("ao-next-{provider_runtime}"))
        }
    }
}

fn validate_current_ao_binding(
    binding: &CurrentAoBinding,
    profile: &VariantProfile,
) -> Result<(), CommandFailure> {
    if binding.schema_version != "ao.next.current-ao-binding.v1"
        || binding.adapter_version != profile.adapter_version
        || canonical_digest(binding)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
            != profile.adapter_digest
    {
        return Err(CommandFailure::invalid_input(
            "current-AO adapter binding drifted",
        ));
    }
    for (program, expected) in [
        (&binding.ao2_program, &binding.ao2_program_digest),
        (&binding.provider_program, &binding.provider_program_digest),
    ] {
        if !program.is_absolute() {
            return Err(CommandFailure::invalid_input(
                "current-AO program path is not absolute",
            ));
        }
        let metadata = std::fs::symlink_metadata(program)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
        if unsafe_link_metadata(&metadata)
            || !metadata.is_file()
            || metadata.len() > 512 * 1024 * 1024
        {
            return Err(CommandFailure::invalid_input(
                "current-AO program is not a bounded regular non-symlink file",
            ));
        }
        let observed = digest_regular_file(program)?;
        if &observed != expected {
            return Err(CommandFailure::invalid_input(
                "current-AO program digest drifted",
            ));
        }
    }
    Ok(())
}

fn digest_regular_file(path: &Path) -> Result<Digest, CommandFailure> {
    let mut file = std::fs::File::open(path)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let mut hasher = Sha256::new();
    std::io::copy(&mut file, &mut hasher)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    Digest::new(format!("sha256:{:x}", hasher.finalize()))
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))
}

fn direct_prompt(input: &LiveRunInput) -> Result<String, CommandFailure> {
    serde_json::to_string(&serde_json::json!({
        "schema_version": "ao.next.native-direct-prompt.v1",
        "objective": input.request.objective,
        "run_id": input.request.run_id,
        "source": input.request.source,
        "workspace": input.request.workspace,
        "policy_digest": input.request.policy_digest,
        "verifier_profile_digest": input.request.verifier_profile.profile_digest,
        "constraints": {
            "worker_count": 1,
            "dynamic_fanout": false,
            "network": "denied",
            "credentials": "denied"
        }
    }))
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))
}

fn current_ao_prompt(input: &LiveRunInput) -> Result<String, CommandFailure> {
    serde_json::to_string(&serde_json::json!({
        "schema_version": "ao.next.current-ao-prompt.v1",
        "objective": input.request.objective,
        "run_id": input.request.run_id,
        "source": input.request.source,
        "workspace": input.request.workspace,
        "policy_digest": input.request.policy_digest,
        "verifier_profile_digest": input.request.verifier_profile.profile_digest,
        "constraints": {
            "worker_count": 1,
            "dynamic_fanout": false,
            "network": "denied",
            "credentials": "denied",
            "external_effects": "denied"
        }
    }))
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))
}

fn invocation_limits(request: &RunRequest) -> Result<InvocationLimits, CommandFailure> {
    Ok(InvocationLimits {
        max_input_bytes: usize::try_from(request.limits.max_input_bytes)
            .map_err(|_| CommandFailure::invalid_input("input limit is too large"))?,
        max_output_bytes: usize::try_from(request.limits.max_output_bytes)
            .map_err(|_| CommandFailure::invalid_input("output limit is too large"))?,
        timeout_ms: request.limits.max_run_ms,
    })
}

fn ensure_checked_output_schema(path: &Path, maximum_bytes: u64) -> Result<(), CommandFailure> {
    let bytes = read_bounded_path(path, maximum_bytes)?;
    decode_strict_json::<serde_json::Value>(
        &bytes,
        usize::try_from(maximum_bytes).unwrap_or(usize::MAX),
    )
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if bytes != ADAPTER_TURN_SCHEMA_BYTES {
        return Err(CommandFailure::invalid_input(
            "output schema drifted from the checked adapter-turn contract",
        ));
    }
    Ok(())
}

#[allow(
    clippy::too_many_lines,
    reason = "the deterministic seed repository contract is intentionally visible in one boundary"
)]
pub(super) fn prepare_git_workspace(
    root: &Path,
    allowed_roots: &[PathBuf],
    seed_digest: &Digest,
) -> Result<GitWorkspaceIdentity, CommandFailure> {
    let metadata = std::fs::symlink_metadata(root)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if unsafe_link_metadata(&metadata) || !metadata.is_dir() {
        return Err(CommandFailure::invalid_input(
            "workspace is not a regular non-symlink directory",
        ));
    }
    #[cfg(windows)]
    let _workspace_anchor = open_windows_non_reparse(root, true)?;
    let repository_root = std::fs::canonicalize(root)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    #[cfg(windows)]
    let mut allowed_anchors = Vec::with_capacity(allowed_roots.len());
    let allowed = allowed_roots
        .iter()
        .try_fold(false, |matched, allowed_root| {
            let metadata = std::fs::symlink_metadata(allowed_root)
                .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
            if unsafe_link_metadata(&metadata) || !metadata.is_dir() {
                return Err(CommandFailure::invalid_input(
                    "authority root is not a regular non-symlink directory",
                ));
            }
            #[cfg(windows)]
            allowed_anchors.push(open_windows_non_reparse(allowed_root, true)?);
            let allowed_root = std::fs::canonicalize(allowed_root)
                .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
            Ok(matched || repository_root.starts_with(allowed_root))
        })?;
    if !allowed {
        return Err(CommandFailure::invalid_input(
            "workspace is outside exact authority roots",
        ));
    }
    reject_git_metadata(&repository_root)?;

    run_git_checked(
        &repository_root,
        vec![
            "init".into(),
            "--quiet".into(),
            format!("--initial-branch={GIT_BRANCH}"),
            "--template=".into(),
            ".".into(),
        ],
        "initialize repository",
    )?;
    run_git_checked(
        &repository_root,
        vec![
            "-c".into(),
            "core.autocrlf=false".into(),
            "-c".into(),
            "core.safecrlf=false".into(),
            "add".into(),
            "--all".into(),
            "--".into(),
            ".".into(),
        ],
        "stage sealed seed",
    )?;
    run_git_checked(
        &repository_root,
        vec![
            "-c".into(),
            "user.name=AO Next".into(),
            "-c".into(),
            "user.email=ao-next@invalid".into(),
            "-c".into(),
            "commit.gpgSign=false".into(),
            "commit".into(),
            "--quiet".into(),
            "--allow-empty".into(),
            "--no-verify".into(),
            "--cleanup=verbatim".into(),
            "--message".into(),
            format!(
                "AO Next sealed workspace seed\n\nSeed-Digest: {}",
                seed_digest.as_str()
            ),
        ],
        "commit sealed seed",
    )?;

    let common_dir = PathBuf::from(git_text(
        &repository_root,
        vec![
            "rev-parse".into(),
            "--path-format=absolute".into(),
            "--git-common-dir".into(),
        ],
        "resolve Git common directory",
    )?);
    let repository_top = PathBuf::from(git_text(
        &repository_root,
        vec![
            "rev-parse".into(),
            "--path-format=absolute".into(),
            "--show-toplevel".into(),
        ],
        "resolve Git repository root",
    )?);
    let head_commit = git_text(
        &repository_root,
        vec!["rev-parse".into(), "HEAD".into()],
        "resolve Git HEAD",
    )?;
    let branch = git_text(
        &repository_root,
        vec!["symbolic-ref".into(), "--short".into(), "HEAD".into()],
        "resolve Git branch",
    )?;
    if branch != GIT_BRANCH
        || std::fs::canonicalize(&repository_top).ok() != Some(repository_root.clone())
        || head_commit.len() != 40
        || !head_commit.bytes().all(|byte| byte.is_ascii_hexdigit())
    {
        return Err(CommandFailure::runtime(
            "prepared Git repository identity drifted",
        ));
    }
    let status = run_git_checked(
        &repository_root,
        vec![
            "status".into(),
            "--porcelain=v1".into(),
            "--untracked-files=all".into(),
        ],
        "verify clean workspace",
    )?;
    if !status.stdout.is_empty() || !status.stderr.is_empty() {
        return Err(CommandFailure::runtime(
            "prepared Git workspace is dirty before provider spawn",
        ));
    }
    let identity = GitWorkspaceIdentity {
        repository_root,
        control_digest: git_control_digest(&common_dir)?,
        index_digest: git_index_digest(&repository_top)?,
        common_dir,
        head_commit,
        branch: GIT_BRANCH.into(),
    };
    verify_git_workspace(&identity, false)?;
    Ok(identity)
}

fn reject_git_metadata(root: &Path) -> Result<(), CommandFailure> {
    #[cfg(windows)]
    let mut directory_anchors = vec![open_windows_non_reparse(root, true)?];
    let mut pending = vec![root.to_path_buf()];
    while let Some(directory) = pending.pop() {
        for entry in std::fs::read_dir(&directory)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
        {
            let path = entry
                .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
                .path();
            if path
                .file_name()
                .is_some_and(|name| name.eq_ignore_ascii_case(".git"))
            {
                return Err(CommandFailure::invalid_input(
                    "workspace contains preexisting or nested Git metadata",
                ));
            }
            let metadata = std::fs::symlink_metadata(&path)
                .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
            if unsafe_link_metadata(&metadata) {
                return Err(CommandFailure::invalid_input(
                    "workspace contains a symlink",
                ));
            }
            if metadata.is_dir() {
                #[cfg(windows)]
                directory_anchors.push(open_windows_non_reparse(&path, true)?);
                pending.push(path);
            } else if metadata.is_file() {
                #[cfg(windows)]
                drop(open_windows_non_reparse(&path, false)?);
            } else {
                return Err(CommandFailure::invalid_input(
                    "workspace contains a non-regular entry",
                ));
            }
        }
    }
    Ok(())
}

fn git_environment(root: &Path, program: &Path) -> BTreeMap<String, String> {
    #[cfg(unix)]
    let _ = program;
    let mut environment = BTreeMap::from([
        ("HOME".into(), root.display().to_string()),
        ("LANG".into(), "C".into()),
        ("LC_ALL".into(), "C".into()),
        ("TZ".into(), "UTC".into()),
        ("GIT_CONFIG_NOSYSTEM".into(), "1".into()),
        ("GIT_AUTHOR_NAME".into(), "AO Next".into()),
        ("GIT_AUTHOR_EMAIL".into(), "ao-next@invalid".into()),
        ("GIT_AUTHOR_DATE".into(), GIT_TIMESTAMP.into()),
        ("GIT_COMMITTER_NAME".into(), "AO Next".into()),
        ("GIT_COMMITTER_EMAIL".into(), "ao-next@invalid".into()),
        ("GIT_COMMITTER_DATE".into(), GIT_TIMESTAMP.into()),
    ]);
    #[cfg(unix)]
    environment.extend([
        ("PATH".into(), "/usr/bin:/bin".into()),
        ("GIT_CONFIG_GLOBAL".into(), "/dev/null".into()),
        ("GIT_CONFIG_SYSTEM".into(), "/dev/null".into()),
    ]);
    #[cfg(windows)]
    {
        let system_root = std::env::var("SystemRoot").unwrap_or_else(|_| r"C:\Windows".into());
        let program_dir = program.parent().unwrap_or_else(|| Path::new(""));
        environment.extend([
            (
                "PATH".into(),
                format!(r"{};{}\System32", program_dir.display(), system_root),
            ),
            ("GIT_CONFIG_GLOBAL".into(), "NUL".into()),
            ("GIT_CONFIG_SYSTEM".into(), "NUL".into()),
            ("SystemRoot".into(), system_root),
        ]);
    }
    environment
}

#[cfg(unix)]
#[allow(
    clippy::unnecessary_wraps,
    reason = "Windows Git discovery can fail and shares this call site"
)]
fn git_program() -> Result<PathBuf, CommandFailure> {
    Ok(PathBuf::from(GIT_PROGRAM))
}

#[cfg(windows)]
fn git_program() -> Result<PathBuf, CommandFailure> {
    let path = std::env::var_os("PATH")
        .ok_or_else(|| CommandFailure::runtime("Git executable cannot be resolved without PATH"))?;
    for directory in std::env::split_paths(&path) {
        let candidate = directory.join("git.exe");
        let Ok(metadata) = std::fs::symlink_metadata(&candidate) else {
            continue;
        };
        if !unsafe_link_metadata(&metadata) && metadata.is_file() {
            return std::fs::canonicalize(candidate)
                .map_err(|error| CommandFailure::runtime(error.to_string()));
        }
    }
    Err(CommandFailure::runtime(
        "Git executable is missing from the operator PATH",
    ))
}

fn run_git_checked(
    root: &Path,
    args: Vec<String>,
    stage: &str,
) -> Result<InvocationOutput, CommandFailure> {
    let mut runner = BoundedProcessRunner;
    run_git_checked_with_runner(root, args, stage, &mut runner)
}

fn run_git_checked_with_runner<R: ProcessRunner>(
    root: &Path,
    args: Vec<String>,
    stage: &str,
    runner: &mut R,
) -> Result<InvocationOutput, CommandFailure> {
    let program = git_program()?;
    let output = runner
        .run(
            &PreparedInvocation {
                program: program.display().to_string(),
                args,
                stdin: Vec::new(),
                cwd: root.to_path_buf(),
                environment: Some(git_environment(root, &program)),
                limits: InvocationLimits {
                    max_input_bytes: 0,
                    max_output_bytes: GIT_OUTPUT_LIMIT,
                    timeout_ms: GIT_TIMEOUT_MS,
                },
            },
            &CancellationToken::new(),
        )
        .map_err(|error| CommandFailure::runtime(format!("Git {stage} failed: {error}")))?;
    if output.stdout.len().saturating_add(output.stderr.len()) > GIT_OUTPUT_LIMIT {
        return Err(CommandFailure::runtime(format!(
            "Git {stage} output exceeds bounded limit"
        )));
    }
    if output.status != 0 {
        let diagnostic = String::from_utf8_lossy(&output.stderr)
            .replace(&root.display().to_string(), "<workspace>");
        return Err(CommandFailure::runtime(format!(
            "Git {stage} exited {}: {}",
            output.status,
            diagnostic.trim()
        )));
    }
    Ok(output)
}

fn git_text(root: &Path, args: Vec<String>, stage: &str) -> Result<String, CommandFailure> {
    let output = run_git_checked(root, args, stage)?;
    let text = String::from_utf8(output.stdout)
        .map_err(|_| CommandFailure::runtime(format!("Git {stage} returned non-UTF-8")))?;
    let text = text.trim_end_matches(['\r', '\n']);
    if text.is_empty() || text.contains('\0') || text.contains('\n') || text.contains('\r') {
        return Err(CommandFailure::runtime(format!(
            "Git {stage} returned malformed output"
        )));
    }
    Ok(text.to_string())
}

fn verify_git_workspace(
    identity: &GitWorkspaceIdentity,
    require_clean: bool,
) -> Result<(), CommandFailure> {
    let git_metadata = std::fs::symlink_metadata(identity.repository_root.join(".git"))
        .map_err(|error| CommandFailure::runtime(error.to_string()))?;
    if unsafe_link_metadata(&git_metadata) || !git_metadata.is_dir() {
        return Err(CommandFailure::runtime(
            "prepared root Git metadata is not an ordinary directory",
        ));
    }
    let common_dir = PathBuf::from(git_text(
        &identity.repository_root,
        vec![
            "rev-parse".into(),
            "--path-format=absolute".into(),
            "--git-common-dir".into(),
        ],
        "verify common directory",
    )?);
    let head = git_text(
        &identity.repository_root,
        vec!["rev-parse".into(), "HEAD".into()],
        "verify HEAD",
    )?;
    let branch = git_text(
        &identity.repository_root,
        vec!["symbolic-ref".into(), "--short".into(), "HEAD".into()],
        "verify branch",
    )?;
    if std::fs::canonicalize(common_dir).ok() != std::fs::canonicalize(&identity.common_dir).ok()
        || head != identity.head_commit
        || branch != identity.branch
    {
        return Err(CommandFailure::runtime(
            "prepared Git workspace identity changed",
        ));
    }
    if require_clean {
        let status = run_git_checked(
            &identity.repository_root,
            vec![
                "status".into(),
                "--porcelain=v1".into(),
                "--untracked-files=all".into(),
            ],
            "verify clean workspace",
        )?;
        if !status.stdout.is_empty() || !status.stderr.is_empty() {
            return Err(CommandFailure::runtime(
                "prepared Git workspace is dirty before provider spawn",
            ));
        }
    }
    if git_control_digest(&identity.common_dir)? != identity.control_digest {
        return Err(CommandFailure::runtime("prepared Git control data changed"));
    }
    if git_index_digest(&identity.repository_root)? != identity.index_digest {
        return Err(CommandFailure::runtime(
            "prepared Git index projection changed",
        ));
    }
    Ok(())
}

fn git_control_digest(common_dir: &Path) -> Result<Digest, CommandFailure> {
    let mut control = snapshot_tree(common_dir, GIT_CONTROL_LIMIT)?;
    // `git status` refreshes index stat data without changing repository authority.
    control.retain(|entry| entry.path != Path::new("index"));
    canonical_digest(&control).map_err(|error| CommandFailure::runtime(error.to_string()))
}

fn git_index_digest(repository_root: &Path) -> Result<Digest, CommandFailure> {
    let staged = run_git_checked(
        repository_root,
        vec!["ls-files".into(), "--stage".into(), "-z".into()],
        "project index",
    )?;
    let flags = run_git_checked(
        repository_root,
        vec!["ls-files".into(), "-v".into(), "-z".into()],
        "project index flags",
    )?;
    if !staged.stderr.is_empty() || !flags.stderr.is_empty() {
        return Err(CommandFailure::runtime(
            "Git index projection returned diagnostics",
        ));
    }
    canonical_digest(&(staged.stdout, flags.stdout))
        .map_err(|error| CommandFailure::runtime(error.to_string()))
}

fn snapshot_tree(root: &Path, maximum_bytes: u64) -> Result<Vec<SnapshotEntry>, CommandFailure> {
    snapshot_product_tree(root, maximum_bytes, None)
}

fn snapshot_product_tree(
    root: &Path,
    maximum_bytes: u64,
    git_workspace: Option<&GitWorkspaceIdentity>,
) -> Result<Vec<SnapshotEntry>, CommandFailure> {
    if let Some(identity) = git_workspace {
        verify_git_workspace(identity, false)?;
    }
    let metadata = std::fs::symlink_metadata(root)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    if unsafe_link_metadata(&metadata) || !metadata.is_dir() {
        return Err(CommandFailure::invalid_input(
            "snapshot root is not a regular non-symlink directory",
        ));
    }
    let canonical_root = std::fs::canonicalize(root)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    #[cfg(windows)]
    let mut directory_anchors = vec![open_windows_non_reparse(&canonical_root, true)?];
    let mut pending = vec![canonical_root.clone()];
    let mut paths = Vec::new();
    while let Some(directory) = pending.pop() {
        let entries = std::fs::read_dir(&directory)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
        for entry in entries {
            let path = entry
                .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
                .path();
            if path
                .file_name()
                .is_some_and(|name| name.eq_ignore_ascii_case(".git"))
            {
                if git_workspace.is_some() && directory == canonical_root {
                    continue;
                }
                return Err(CommandFailure::invalid_input(
                    "product snapshot contains unexpected Git metadata",
                ));
            }
            let metadata = std::fs::symlink_metadata(&path)
                .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
            if unsafe_link_metadata(&metadata) {
                return Err(CommandFailure::invalid_input(
                    "snapshot tree contains a symlink",
                ));
            }
            if metadata.is_dir() {
                #[cfg(windows)]
                directory_anchors.push(open_windows_non_reparse(&path, true)?);
                pending.push(path);
            } else if metadata.is_file() {
                #[cfg(windows)]
                drop(open_windows_non_reparse(&path, false)?);
                paths.push(path);
            } else {
                return Err(CommandFailure::invalid_input(
                    "snapshot tree contains a non-regular entry",
                ));
            }
        }
    }
    paths.sort();
    let mut total = 0_u64;
    let mut snapshot = Vec::with_capacity(paths.len());
    for path in paths {
        let relative = path
            .strip_prefix(&canonical_root)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?
            .to_path_buf();
        if relative
            .components()
            .any(|component| !matches!(component, Component::Normal(_)))
        {
            return Err(CommandFailure::invalid_input(
                "snapshot tree contains an unsafe path",
            ));
        }
        let bytes = read_bounded_path(&path, maximum_bytes)?;
        total = total.saturating_add(u64::try_from(bytes.len()).unwrap_or(u64::MAX));
        if total > maximum_bytes {
            return Err(CommandFailure::invalid_input(
                "snapshot tree exceeds its byte bound",
            ));
        }
        snapshot.push(SnapshotEntry {
            path: relative,
            sha256: digest_bytes(&bytes),
            size_bytes: u64::try_from(bytes.len()).unwrap_or(u64::MAX),
        });
    }
    Ok(snapshot)
}

fn read_bounded_path(path: &Path, maximum_bytes: u64) -> Result<Vec<u8>, CommandFailure> {
    #[cfg(windows)]
    {
        let file = open_windows_non_reparse(path, false)?;
        let metadata = file
            .metadata()
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
        if metadata.len() > maximum_bytes {
            return Err(CommandFailure::invalid_input(
                "file is not a bounded regular non-reparse file",
            ));
        }
        let mut bytes = Vec::new();
        file.take(maximum_bytes.saturating_add(1))
            .read_to_end(&mut bytes)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
        if u64::try_from(bytes.len()).unwrap_or(u64::MAX) > maximum_bytes {
            return Err(CommandFailure::invalid_input(
                "file is not a bounded regular non-reparse file",
            ));
        }
        Ok(bytes)
    }
    #[cfg(not(windows))]
    {
        let metadata = std::fs::symlink_metadata(path)
            .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
        if unsafe_link_metadata(&metadata) || !metadata.is_file() || metadata.len() > maximum_bytes
        {
            return Err(CommandFailure::invalid_input(
                "file is not a bounded regular non-symlink file",
            ));
        }
        std::fs::read(path).map_err(|error| CommandFailure::invalid_input(error.to_string()))
    }
}

fn count_changed_files(before: &[SnapshotEntry], after: &[SnapshotEntry]) -> u32 {
    let before = before
        .iter()
        .map(|entry| (&entry.path, &entry.sha256))
        .collect::<BTreeMap<_, _>>();
    let after = after
        .iter()
        .map(|entry| (&entry.path, &entry.sha256))
        .collect::<BTreeMap<_, _>>();
    u32::try_from(
        before
            .keys()
            .chain(after.keys())
            .collect::<BTreeSet<_>>()
            .into_iter()
            .filter(|path| before.get(*path) != after.get(*path))
            .count(),
    )
    .unwrap_or(u32::MAX)
}

fn checked_sum_capture_usage(captures: &[RuntimeCapture]) -> Option<TokenUsage> {
    captures
        .iter()
        .try_fold(TokenUsage::default(), |mut total, capture| {
            total.input_tokens = total.input_tokens.checked_add(capture.usage.input_tokens)?;
            total.cached_input_tokens = total
                .cached_input_tokens
                .checked_add(capture.usage.cached_input_tokens)?;
            total.reasoning_tokens = total
                .reasoning_tokens
                .checked_add(capture.usage.reasoning_tokens)?;
            total.output_tokens = total
                .output_tokens
                .checked_add(capture.usage.output_tokens)?;
            total.output_bytes = total.output_bytes.checked_add(capture.usage.output_bytes)?;
            Some(total)
        })
}

#[cfg(all(test, unix))]
mod tests {
    use std::collections::{BTreeSet, VecDeque};
    use std::process::Command;
    use std::sync::{Arc, Mutex};

    use ao_next_core::adapter::{
        AdapterAction, AdapterTurn, InvocationError, InvocationOutput, PreparedInvocation,
        TokenUsage,
    };
    use ao_next_core::contracts::{
        AuthorityEnvelope, Capability, EffectKind, EffectRequest, ExternalEffectPolicy,
        ModelProfile, NetworkPolicy, RunLimits, SourceIdentity, StructuredCommand, VerifierProfile,
        WorkspaceIdentity,
    };
    use ao_next_core::verifier::CommandVerifierEntry;
    use ao_next_eval::corpus::{CorpusKind, counterbalanced_schedule};
    use chrono::Duration;
    use tempfile::TempDir;

    use super::*;

    #[test]
    fn windows_reparse_attribute_is_unsafe() {
        assert!(windows_reparse_point(0x400));
        assert!(!windows_reparse_point(0));
    }

    struct FakeProvider {
        outputs: VecDeque<InvocationOutput>,
        direct_write: Option<PathBuf>,
        additional_write: Option<(PathBuf, Vec<u8>)>,
    }

    impl ProcessRunner for FakeProvider {
        fn run(
            &mut self,
            invocation: &PreparedInvocation,
            _: &CancellationToken,
        ) -> Result<InvocationOutput, InvocationError> {
            if invocation.args.first().is_some_and(|arg| arg == "adapter") {
                assert!(invocation.program.ends_with("true"));
            } else if invocation
                .args
                .windows(2)
                .any(|args| args == ["--sandbox", "workspace-write"])
            {
                if let Some(path) = self.direct_write.take() {
                    std::fs::write(path, b"ready\n").expect("direct fixture write");
                }
            } else {
                assert!(
                    invocation
                        .args
                        .windows(2)
                        .any(|args| args == ["--sandbox", "read-only"])
                );
            }
            if let Some((path, bytes)) = self.additional_write.take() {
                std::fs::write(path, bytes).expect("additional fixture write");
            }
            Ok(self.outputs.pop_front().expect("provider fixture output"))
        }
    }

    struct GitCheckingN0Runner {
        adapter_output: InvocationOutput,
        workspace: PathBuf,
        product: PathBuf,
    }

    struct CaptureCheckingVerifier {
        capture_root: PathBuf,
        inner: BoundedProcessRunner,
    }

    #[derive(Default)]
    struct InvocationCounts {
        provider: u32,
        preview: u32,
        apply: u32,
        verifier: u32,
    }

    struct CountingN0Runner {
        outputs: VecDeque<InvocationOutput>,
        counts: Arc<Mutex<InvocationCounts>>,
        apply_product: Option<PathBuf>,
    }

    struct CountingVerifierRunner {
        counts: Arc<Mutex<InvocationCounts>>,
    }

    struct ProviderFreeRunner {
        program: PathBuf,
        inner: BoundedProcessRunner,
    }

    struct FixedProcessResult {
        result: Option<Result<InvocationOutput, InvocationError>>,
    }

    struct CollidingJournalRunner {
        event_root: PathBuf,
    }

    impl ProcessRunner for CollidingJournalRunner {
        fn run(
            &mut self,
            _: &PreparedInvocation,
            _: &CancellationToken,
        ) -> Result<InvocationOutput, InvocationError> {
            std::fs::write(self.event_root.join("invalid-event"), b"collision")
                .expect("journal collision");
            Ok(InvocationOutput {
                status: 0,
                stdout: b"retained".to_vec(),
                stderr: Vec::new(),
            })
        }
    }

    impl ProcessRunner for FixedProcessResult {
        fn run(
            &mut self,
            _: &PreparedInvocation,
            _: &CancellationToken,
        ) -> Result<InvocationOutput, InvocationError> {
            self.result.take().expect("one fixed process result")
        }
    }

    impl ProcessRunner for CountingN0Runner {
        fn run(
            &mut self,
            invocation: &PreparedInvocation,
            _: &CancellationToken,
        ) -> Result<InvocationOutput, InvocationError> {
            match invocation.args.get(1).map(String::as_str) {
                Some("run") => self.counts.lock().expect("invocation counts").provider += 1,
                Some("patch") if invocation.args.get(2).map(String::as_str) == Some("preview") => {
                    self.counts.lock().expect("invocation counts").preview += 1;
                }
                Some("patch") if invocation.args.get(2).map(String::as_str) == Some("apply") => {
                    self.counts.lock().expect("invocation counts").apply += 1;
                    if let Some(product) = self.apply_product.take() {
                        std::fs::write(product, b"ready\n").expect("applied product");
                    }
                }
                _ => panic!("unexpected N0 invocation: {:?}", invocation.args),
            }
            Ok(self.outputs.pop_front().expect("N0 fixture output"))
        }
    }

    impl ProcessRunner for CountingVerifierRunner {
        fn run(
            &mut self,
            _: &PreparedInvocation,
            _: &CancellationToken,
        ) -> Result<InvocationOutput, InvocationError> {
            self.counts.lock().expect("invocation counts").verifier += 1;
            Ok(InvocationOutput {
                status: 0,
                stdout: Vec::new(),
                stderr: Vec::new(),
            })
        }
    }

    impl ProviderFreeRunner {
        fn from_environment() -> Result<Self, String> {
            let program = PathBuf::from(
                std::env::var_os("AO_NEXT_PROVIDER_FREE_PROGRAM")
                    .ok_or("AO_NEXT_PROVIDER_FREE_PROGRAM is missing")?,
            );
            if !program.is_absolute() {
                return Err("provider-free program is not absolute".into());
            }
            let metadata =
                std::fs::symlink_metadata(&program).map_err(|error| error.to_string())?;
            if unsafe_link_metadata(&metadata) || !metadata.is_file() {
                return Err("provider-free program is not a regular non-symlink file".into());
            }
            let expected = Digest::new(
                std::env::var("AO_NEXT_PROVIDER_FREE_PROGRAM_DIGEST")
                    .map_err(|error| error.to_string())?,
            )
            .map_err(|error| error.to_string())?;
            if digest_regular_file(&program).map_err(|error| error.message)? != expected {
                return Err("provider-free program digest drifted".into());
            }
            Ok(Self {
                program,
                inner: BoundedProcessRunner,
            })
        }
    }

    impl ProcessRunner for ProviderFreeRunner {
        fn run(
            &mut self,
            invocation: &PreparedInvocation,
            cancellation: &CancellationToken,
        ) -> Result<InvocationOutput, InvocationError> {
            if invocation.program != "codex" {
                return Err(InvocationError::Io(
                    "provider-free runner received an unexpected program".into(),
                ));
            }
            let mut prepared = invocation.clone();
            prepared.program = self.program.display().to_string();
            self.inner.run(&prepared, cancellation)
        }
    }

    impl ProcessRunner for CaptureCheckingVerifier {
        fn run(
            &mut self,
            invocation: &PreparedInvocation,
            cancellation: &CancellationToken,
        ) -> Result<InvocationOutput, InvocationError> {
            if !self.capture_root.join("capture-index.json").is_file() {
                return Err(InvocationError::Io(
                    "capture missing before verifier".into(),
                ));
            }
            self.inner.run(invocation, cancellation)
        }
    }

    impl ProcessRunner for GitCheckingN0Runner {
        fn run(
            &mut self,
            invocation: &PreparedInvocation,
            _: &CancellationToken,
        ) -> Result<InvocationOutput, InvocationError> {
            match invocation.args.get(1).map(String::as_str) {
                Some("run") => Ok(self.adapter_output.clone()),
                Some("patch") if invocation.args.get(2).map(String::as_str) == Some("preview") => {
                    let output = Command::new("/usr/bin/git")
                        .args(["rev-parse", "--git-common-dir"])
                        .current_dir(&self.workspace)
                        .output()
                        .expect("real Git preview prerequisite");
                    if output.status.success() {
                        Ok(InvocationOutput {
                            status: 0,
                            stdout: serde_json::to_vec(&serde_json::json!({
                                "action_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
                            }))
                            .expect("preview JSON"),
                            stderr: output.stderr,
                        })
                    } else {
                        Ok(InvocationOutput {
                            status: output.status.code().unwrap_or(-1),
                            stdout: output.stdout,
                            stderr: output.stderr,
                        })
                    }
                }
                Some("patch") if invocation.args.get(2).map(String::as_str) == Some("apply") => {
                    std::fs::write(&self.product, b"ready\n").expect("applied product");
                    Ok(InvocationOutput {
                        status: 0,
                        stdout: serde_json::to_vec(&serde_json::json!({
                            "action_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
                        }))
                        .expect("apply JSON"),
                        stderr: Vec::new(),
                    })
                }
                _ => panic!("unexpected N0 command: {:?}", invocation.args),
            }
        }
    }

    struct Fixture {
        root: TempDir,
        input: LiveRunInput,
        product: PathBuf,
    }

    #[allow(
        clippy::too_many_lines,
        reason = "one self-contained fixture keeps all sealed identities internally consistent"
    )]
    fn fixture(variant: LiveVariant) -> Fixture {
        let root = TempDir::new().expect("temporary");
        let workspace = root.path().join("workspace");
        let protected = root.path().join("protected");
        let controls = root.path().join("controls");
        let visible = protected.join("visible");
        let hidden = protected.join("hidden");
        let raw_capture_root = protected.join("raw-captures");
        std::fs::create_dir_all(&workspace).expect("workspace");
        std::fs::create_dir_all(&visible).expect("visible");
        std::fs::create_dir_all(&hidden).expect("hidden");
        std::fs::create_dir_all(&raw_capture_root).expect("raw captures");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt as _;
            std::fs::set_permissions(&raw_capture_root, std::fs::Permissions::from_mode(0o700))
                .expect("private raw captures");
        }
        std::fs::create_dir_all(&controls).expect("controls");
        std::fs::write(visible.join("example.txt"), b"visible\n").expect("visible fixture");
        std::fs::write(
            hidden.join("test_product.py"),
            b"from pathlib import Path\nassert Path.cwd().joinpath('product.txt').read_text() == 'ready\\n'\n",
        )
        .expect("hidden fixture");
        let objective_text = "Create product.txt containing ready followed by a newline.";
        let objective_path = protected.join("objective.md");
        std::fs::write(&objective_path, objective_text).expect("objective");
        let output_schema = controls.join("turn.schema.json");
        std::fs::write(&output_schema, ADAPTER_TURN_SCHEMA_BYTES).expect("schema");

        let workspace_files = snapshot_tree(&workspace, 64 * 1024).expect("workspace snapshot");
        let workspace_digest = canonical_digest(&workspace_files).expect("workspace digest");
        let source_snapshot = SourceSnapshot {
            schema_version: "ao.next.source-snapshot.v1".into(),
            task_id: "greenfield-engineering-app".into(),
            tree_digest: workspace_digest.clone(),
            files: workspace_files,
        };
        let source_digest = canonical_digest(&source_snapshot).expect("source digest");
        let source_path = protected.join("source-snapshot.json");
        std::fs::write(
            &source_path,
            serde_json::to_vec(&source_snapshot).expect("source JSON"),
        )
        .expect("source snapshot");
        let visible_digest =
            canonical_digest(&snapshot_tree(&visible, 64 * 1024).expect("visible snapshot"))
                .expect("visible digest");
        let hidden_digest =
            canonical_digest(&snapshot_tree(&hidden, 64 * 1024).expect("hidden snapshot"))
                .expect("hidden digest");
        let verifier_program = "/usr/bin/python3".to_owned();
        let mut verifier_entry = CommandVerifierEntry {
            verifier_id: "hidden-product-check".into(),
            verifier_digest: digest_bytes(b"unsealed verifier entry"),
            program: verifier_program.clone(),
            args: vec![hidden.join("test_product.py").display().to_string()],
            working_directory: PathBuf::new(),
            timeout_ms: 5_000,
            max_output_bytes: 16 * 1024,
            expected_exit_status: 0,
            required_artifacts: Vec::new(),
        };
        verifier_entry.verifier_digest = verifier_entry.calculated_digest().expect("entry digest");
        let mut command_verifier = CommandVerifierProfile {
            schema_version: "ao.next.command-verifier-profile.v1".into(),
            profile_id: "hidden-product-verifier-v1".into(),
            profile_digest: digest_bytes(b"unsealed verifier profile"),
            entries: vec![verifier_entry.clone()],
        };
        command_verifier.profile_digest = command_verifier
            .calculated_digest()
            .expect("verifier profile digest");
        let current_program = PathBuf::from("/usr/bin/true");
        let current_program_digest =
            digest_bytes(&std::fs::read(&current_program).expect("current-AO fixture program"));
        let current_ao = CurrentAoBinding {
            schema_version: "ao.next.current-ao-binding.v1".into(),
            ao2_program: current_program.clone(),
            ao2_program_digest: current_program_digest.clone(),
            provider_program: current_program,
            provider_program_digest: current_program_digest,
            adapter_version: "current-ao-native-v1".into(),
        };
        let mut profiles = [
            profile(ExecutionVariant::N0, "current-ao", "current-ao-native-v1"),
            profile(ExecutionVariant::N4, "codex", "native-codex-direct-v1"),
            profile(ExecutionVariant::N7, "ao-next-codex", "ao-next-process-v1"),
        ];
        profiles[0].adapter_digest =
            canonical_digest(&current_ao).expect("current-AO binding digest");
        let selected_profile = profiles
            .iter()
            .find(|profile| profile.variant == variant.execution_variant())
            .expect("selected profile")
            .clone();
        let selected_task = EvaluationTask {
            task_id: "greenfield-engineering-app".into(),
            task_kind: "greenfield_engineering_application".into(),
            source_digest: source_digest.clone(),
            objective_digest: digest_bytes(objective_text.as_bytes()),
            workspace_seed_digest: workspace_digest.clone(),
            visible_fixtures_digest: visible_digest,
            hidden_tests_digest: hidden_digest,
            verifier_profile_digest: command_verifier.profile_digest.clone(),
            variant_profiles: profiles.to_vec(),
        };
        let mut corpus = CorpusManifest {
            schema_version: "ao.next.evaluation-corpus.v2".into(),
            corpus_kind: CorpusKind::SealedLive,
            corpus_digest: digest_bytes(b"unsealed corpus"),
            required_trial_count: 3,
            schedule: counterbalanced_schedule(),
            tasks: vec![
                selected_task,
                placeholder_task("bounded-defect-repair", "defect"),
                placeholder_task("artifact-reconciliation", "reconciliation"),
            ],
        };
        corpus.corpus_digest = corpus.calculated_digest().expect("corpus digest");
        let now = Utc::now();
        let request = RunRequest {
            schema_version: "ao.next.run-request.v1".into(),
            run_id: format!("fixture-run-{variant:?}"),
            objective: objective_text.into(),
            source: SourceIdentity {
                repository: "sealed-local-fixture".into(),
                head: source_digest,
            },
            workspace: WorkspaceIdentity {
                workspace_id: format!("fixture-workspace-{variant:?}"),
                root: workspace.clone(),
                seed_digest: workspace_digest,
            },
            model_profile: ModelProfile {
                runtime: "codex".into(),
                model_identifier: selected_profile.model_identifier.clone(),
                reasoning_effort: "high".into(),
                system_prompt_digest: selected_profile.prompt_digest.clone(),
                tool_contract_digest: selected_profile.adapter_digest.clone(),
                context_limit: if variant == LiveVariant::N7 {
                    262_144
                } else {
                    32_000
                },
                output_limit: if variant == LiveVariant::N7 {
                    20_000
                } else {
                    4_000
                },
                adapter_version: selected_profile.adapter_version.clone(),
            },
            authority: AuthorityEnvelope {
                schema_version: "ao.next.authority-envelope.v1".into(),
                issued_by: "offline-fixture".into(),
                issued_at: now - Duration::minutes(1),
                expires_at: now + Duration::hours(1),
                capabilities: if variant == LiveVariant::N7 {
                    BTreeSet::from([Capability::ReadWorkspace, Capability::WriteWorkspace])
                } else {
                    BTreeSet::from([Capability::RunLocalProgram])
                },
                allowed_roots: if variant == LiveVariant::N7 {
                    vec![workspace.clone()]
                } else {
                    vec![workspace.clone(), controls.clone()]
                },
                allowed_programs: if variant == LiveVariant::N7 {
                    BTreeSet::new()
                } else {
                    BTreeSet::from([verifier_program.clone()])
                },
                network: NetworkPolicy::Denied,
                allowed_network_hosts: BTreeSet::new(),
                external_effects: ExternalEffectPolicy::Denied,
            },
            verifier_profile: VerifierProfile {
                profile_id: command_verifier.profile_id.clone(),
                profile_digest: command_verifier.profile_digest.clone(),
                commands: vec![StructuredCommand {
                    program: verifier_program,
                    args: verifier_entry.args.clone(),
                    timeout_ms: verifier_entry.timeout_ms,
                }],
                required_artifacts: Vec::new(),
            },
            policy_digest: selected_profile.policy_digest.clone(),
            limits: RunLimits {
                max_input_bytes: 64 * 1024,
                max_turns: if variant == LiveVariant::N7 { 1 } else { 2 },
                max_repair_attempts: 0,
                max_run_ms: 10_000,
                max_effect_timeout_ms: 5_000,
                max_output_bytes: 64 * 1024,
                max_tokens: if variant == LiveVariant::N7 {
                    LIVE_TOKEN_ENVELOPE
                } else {
                    72_000
                },
            },
        };
        let schedule_position = match variant {
            LiveVariant::N0 => 0,
            LiveVariant::N4 => 1,
            LiveVariant::N7 => 2,
        };
        Fixture {
            product: workspace.join("product.txt"),
            input: LiveRunInput {
                schema_version: "ao.next.live-run-input.v1".into(),
                corpus,
                task_id: "greenfield-engineering-app".into(),
                trial_id: format!("fixture-trial-{variant:?}"),
                trial_index: 0,
                schedule_position,
                workspace_instance_id: request.workspace.workspace_id.clone(),
                source_snapshot: source_path,
                objective: objective_path,
                visible_fixtures: visible,
                hidden_tests: hidden,
                output_schema,
                raw_capture_root,
                request,
                command_verifier,
                current_ao: (variant == LiveVariant::N0).then_some(current_ao),
            },
            root,
        }
    }

    fn retarget_as_functional_sentinel(fixture: &mut Fixture, task_kind: &str) {
        let source_bytes = std::fs::read(&fixture.input.source_snapshot).expect("source fixture");
        let mut source: SourceSnapshot =
            decode_strict_json(&source_bytes, 64 * 1024).expect("source snapshot");
        source.task_id = FUNCTIONAL_SENTINEL_TASK_ID.into();
        std::fs::write(
            &fixture.input.source_snapshot,
            serde_json::to_vec(&source).expect("source JSON"),
        )
        .expect("sentinel source snapshot");
        let source_digest = canonical_digest(&source).expect("source digest");
        let task = fixture
            .input
            .corpus
            .tasks
            .iter_mut()
            .find(|task| task.task_id == fixture.input.task_id)
            .expect("selected task");
        task.task_id = FUNCTIONAL_SENTINEL_TASK_ID.into();
        task.task_kind = task_kind.into();
        task.source_digest = source_digest.clone();
        fixture.input.task_id = FUNCTIONAL_SENTINEL_TASK_ID.into();
        fixture.input.request.source.head = source_digest;
        fixture.input.corpus.corpus_digest = fixture
            .input
            .corpus
            .calculated_digest()
            .expect("sentinel corpus digest");
    }

    fn profile(variant: ExecutionVariant, runtime: &str, adapter_version: &str) -> VariantProfile {
        VariantProfile {
            variant,
            runtime: runtime.into(),
            runtime_digest: digest_bytes(format!("{runtime}:runtime").as_bytes()),
            model_identifier: "fixed-live-model".into(),
            model_digest: canonical_digest(&("fixed-live-model", "high"))
                .expect("model binding digest"),
            prompt_digest: digest_bytes(format!("{runtime}:prompt").as_bytes()),
            policy_digest: digest_bytes(format!("{runtime}:policy").as_bytes()),
            adapter_version: adapter_version.into(),
            adapter_digest: digest_bytes(format!("{runtime}:{adapter_version}").as_bytes()),
        }
    }

    fn placeholder_task(task_id: &str, salt: &str) -> EvaluationTask {
        EvaluationTask {
            task_id: task_id.into(),
            task_kind: "sealed_local_task".into(),
            source_digest: digest_bytes(format!("{salt}:source").as_bytes()),
            objective_digest: digest_bytes(format!("{salt}:objective").as_bytes()),
            workspace_seed_digest: digest_bytes(format!("{salt}:seed").as_bytes()),
            visible_fixtures_digest: digest_bytes(format!("{salt}:visible").as_bytes()),
            hidden_tests_digest: digest_bytes(format!("{salt}:hidden").as_bytes()),
            verifier_profile_digest: digest_bytes(format!("{salt}:verifier").as_bytes()),
            variant_profiles: vec![
                profile(ExecutionVariant::N0, "current-ao", "current-ao-native-v1"),
                profile(ExecutionVariant::N4, "codex", "native-codex-direct-v1"),
                profile(ExecutionVariant::N7, "ao-next-codex", "ao-next-process-v1"),
            ],
        }
    }

    fn codex_output(turn: Option<&AdapterTurn>) -> InvocationOutput {
        codex_output_with_usage(turn, 11, 3, 2, 2)
    }

    fn codex_output_with_usage(
        turn: Option<&AdapterTurn>,
        input_tokens: u64,
        cached_input_tokens: u64,
        reasoning_tokens: u64,
        output_tokens: u64,
    ) -> InvocationOutput {
        let mut lines = Vec::new();
        if let Some(turn) = turn {
            lines.push(
                serde_json::to_string(&serde_json::json!({
                    "type": "item.completed",
                    "item": {
                        "type": "agent_message",
                        "text": serde_json::to_string(turn).expect("turn JSON")
                    }
                }))
                .expect("event JSON"),
            );
        }
        lines.push(
            serde_json::to_string(&serde_json::json!({
                "type": "turn.completed",
                "usage": {
                    "input_tokens": input_tokens,
                    "cached_input_tokens": cached_input_tokens,
                    "reasoning_tokens": reasoning_tokens,
                    "output_tokens": output_tokens
                }
            }))
            .expect("usage JSON"),
        );
        InvocationOutput {
            status: 0,
            stdout: format!("{}\n", lines.join("\n")).into_bytes(),
            stderr: Vec::new(),
        }
    }

    fn current_ao_outputs(workspace: &Path, sandbox: &Path) -> VecDeque<InvocationOutput> {
        current_ao_outputs_with_provider(workspace, sandbox, codex_output(None))
    }

    fn current_ao_outputs_with_provider(
        workspace: &Path,
        sandbox: &Path,
        provider: InvocationOutput,
    ) -> VecDeque<InvocationOutput> {
        let adapter = serde_json::json!({
            "adapter": {
                "provider": "codex",
                "role_id": "ao-next-n0-worker-01",
                "command": "codex exec",
                "exit_code": 0,
                "stdout": String::from_utf8(provider.stdout).expect("provider stdout"),
                "stderr": String::from_utf8(provider.stderr).expect("provider stderr"),
                "transcript": "",
                "blocker": null
            },
            "target_repo": workspace,
            "sandbox_path": sandbox,
            "changed_files": ["product.txt"],
            "diff_summary": "added product.txt",
            "transcript_summary": {
                "changed_files": ["product.txt"],
                "concerns": [],
                "blockers": [],
                "usage": {
                    "input_tokens": 0,
                    "output_tokens": 0,
                    "total_tokens": 0
                },
                "cost_usd": null,
                "raw_summary": null,
                "transcript_ids": []
            }
        });
        let digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
        VecDeque::from([
            InvocationOutput {
                status: 0,
                stdout: serde_json::to_vec(&adapter).expect("current-AO JSON"),
                stderr: Vec::new(),
            },
            InvocationOutput {
                status: 0,
                stdout: serde_json::to_vec(&serde_json::json!({
                    "action_digest": digest
                }))
                .expect("preview JSON"),
                stderr: Vec::new(),
            },
            InvocationOutput {
                status: 0,
                stdout: serde_json::to_vec(&serde_json::json!({
                    "action_digest": digest
                }))
                .expect("apply JSON"),
                stderr: Vec::new(),
            },
        ])
    }

    fn counting_verifier(
        input: &LiveRunInput,
        counts: Arc<Mutex<InvocationCounts>>,
        cancellation: CancellationToken,
    ) -> CommandEngineVerifier<CountingVerifierRunner> {
        CommandEngineVerifier::new(
            &input.request,
            input.command_verifier.clone(),
            CountingVerifierRunner { counts },
            cancellation,
            Utc::now(),
        )
        .expect("counting verifier")
    }

    #[test]
    fn live_preflight_envelope_is_checked_before_workspace_preparation() {
        assert!(std::env::var_os("AO_NEXT_LIVE_PROVIDER_CALLS").is_none());
        assert!(std::env::var_os("AO_NEXT_LIVE_ADAPTER_TESTS").is_none());

        let mut rejected = fixture(LiveVariant::N4);
        rejected.input.request.model_profile.context_limit = 262_144;
        rejected.input.request.model_profile.output_limit = 20_000;
        rejected.input.request.limits.max_tokens = 20_000;
        let Err(error) = validate_input(&rejected.input, LiveVariant::N4, Utc::now()) else {
            panic!("impossible live token envelope must fail preflight");
        };
        assert_eq!(error.code, "invalid_input");
        assert!(!rejected.input.request.workspace.root.join(".git").exists());

        let mut accepted = fixture(LiveVariant::N4);
        accepted.input.request.model_profile.context_limit = 262_144;
        accepted.input.request.model_profile.output_limit = 20_000;
        accepted.input.request.limits.max_tokens = 564_288;
        validate_input(&accepted.input, LiveVariant::N4, Utc::now())
            .expect("live-compatible envelope passes provider-free validation");
        assert!(!accepted.input.request.workspace.root.join(".git").exists());

        let mut multiplication_overflow = fixture(LiveVariant::N4);
        multiplication_overflow
            .input
            .request
            .model_profile
            .context_limit = u64::MAX;
        multiplication_overflow
            .input
            .request
            .model_profile
            .output_limit = 1;
        multiplication_overflow.input.request.limits.max_tokens = u64::MAX;
        assert!(
            validate_input(&multiplication_overflow.input, LiveVariant::N4, Utc::now()).is_err()
        );

        let mut addition_overflow = fixture(LiveVariant::N4);
        addition_overflow.input.request.model_profile.context_limit = u64::MAX / 2;
        addition_overflow.input.request.model_profile.output_limit = u64::MAX / 2;
        addition_overflow.input.request.limits.max_tokens = u64::MAX;
        assert!(validate_input(&addition_overflow.input, LiveVariant::N4, Utc::now()).is_err());
    }

    #[test]
    fn n7_input_rejects_verifier_programs_as_model_authority() {
        let mut fixture = fixture(LiveVariant::N7);
        fixture
            .input
            .request
            .authority
            .capabilities
            .insert(Capability::RunLocalProgram);
        fixture
            .input
            .request
            .authority
            .allowed_programs
            .insert("/usr/bin/python3".into());

        let Err(error) = validate_input(&fixture.input, LiveVariant::N7, Utc::now()) else {
            panic!("N7 model authority inherited trusted verifier programs");
        };

        assert!(error.message.contains("N7 model authority"));
    }

    #[test]
    fn n7_functional_sentinel_identity_passes_without_relaxing_live_campaign_identity() {
        let campaign = fixture(LiveVariant::N7);
        validate_input(&campaign.input, LiveVariant::N7, Utc::now())
            .expect("ordinary live campaign identity remains valid");

        let mut sentinel = fixture(LiveVariant::N7);
        retarget_as_functional_sentinel(&mut sentinel, "functional_native_write_sentinel");

        assert!(sentinel.input.corpus.validate_live().is_err());
        sentinel
            .input
            .corpus
            .validate_functional_sentinel()
            .expect("sentinel-only validator accepts exact identity");

        validate_input(&sentinel.input, LiveVariant::N7, Utc::now())
            .expect("exact functional sentinel identity is accepted");

        let input_path = sentinel.root.path().join("sentinel-input.json");
        std::fs::write(
            &input_path,
            serde_json::to_vec(&sentinel.input).expect("sentinel input JSON"),
        )
        .expect("sentinel input");
        let preflight = preflight(&PreflightLiveInputArgs {
            input: input_path,
            variant: LiveVariantArg::N7,
            trusted_corpus_digest: Some(sentinel.input.corpus.corpus_digest.to_string()),
            trusted_verifier_profile_digest: Some(
                sentinel.input.command_verifier.profile_digest.to_string(),
            ),
        })
        .expect("functional sentinel preflight");
        assert_eq!(preflight.status, 0);
        assert!(!sentinel.input.request.workspace.root.join(".git").exists());
    }

    #[test]
    fn functional_sentinel_rejects_non_n7_and_wrong_task_kind() {
        let mut n4 = fixture(LiveVariant::N4);
        retarget_as_functional_sentinel(&mut n4, "functional_native_write_sentinel");
        assert!(validate_input(&n4.input, LiveVariant::N4, Utc::now()).is_err());

        let mut wrong_kind = fixture(LiveVariant::N7);
        retarget_as_functional_sentinel(&mut wrong_kind, "greenfield_engineering_application");
        assert!(
            validate_input(&wrong_kind.input, LiveVariant::N7, Utc::now()).is_err(),
            "sentinel identity must not admit arbitrary task semantics"
        );
    }

    #[test]
    fn operator_binding_rejects_a_self_consistent_forged_verifier_profile() {
        let mut forged = fixture(LiveVariant::N7);
        let trusted = TrustedLiveBindings {
            corpus_digest: forged.input.corpus.corpus_digest.clone(),
            verifier_profile_digest: forged.input.command_verifier.profile_digest.clone(),
        };
        let entry = &mut forged.input.command_verifier.entries[0];
        entry.program = "/usr/bin/true".into();
        entry.args.clear();
        entry.verifier_digest = entry.calculated_digest().expect("forged entry digest");
        forged.input.command_verifier.profile_digest = forged
            .input
            .command_verifier
            .calculated_digest()
            .expect("forged profile digest");
        forged.input.request.verifier_profile.profile_digest =
            forged.input.command_verifier.profile_digest.clone();
        forged.input.request.verifier_profile.commands[0].program = "/usr/bin/true".into();
        forged.input.request.verifier_profile.commands[0]
            .args
            .clear();
        let task = forged
            .input
            .corpus
            .tasks
            .iter_mut()
            .find(|task| task.task_id == forged.input.task_id)
            .expect("task");
        task.verifier_profile_digest = forged.input.command_verifier.profile_digest.clone();
        forged.input.corpus.corpus_digest = forged
            .input
            .corpus
            .calculated_digest()
            .expect("forged corpus digest");

        validate_input(&forged.input, LiveVariant::N7, Utc::now())
            .expect("self-consistent attacker-selected verifier passes embedded checks");
        let error = validate_trusted_bindings(&forged.input, &trusted)
            .expect_err("external operator binding must reject attacker-selected verifier");
        assert!(error.message.contains("operator-owned"));
    }

    #[test]
    fn trusted_live_input_keeps_the_exact_bytes_it_decoded() {
        let n7 = fixture(LiveVariant::N7);
        let input_path = n7.root.path().join("trusted-live-input.json");
        let bytes = serde_json::to_vec(&n7.input).expect("live input bytes");
        std::fs::write(&input_path, &bytes).expect("live input");

        let loaded = load_trusted_live_input(
            &input_path,
            LiveVariant::N7,
            n7.input.corpus.corpus_digest.as_str(),
            n7.input.command_verifier.profile_digest.as_str(),
            Utc::now(),
        )
        .expect("trusted live input");

        assert_eq!(loaded.bytes, bytes);
        assert_eq!(loaded.input.request.run_id, n7.input.request.run_id);
    }

    #[test]
    fn provider_free_preflight_leaves_workspace_ready_for_exact_live_execution() {
        assert!(std::env::var_os("AO_NEXT_LIVE_PROVIDER_CALLS").is_none());
        let n7 = fixture(LiveVariant::N7);
        let input_path = n7.root.path().join("live-input.json");
        std::fs::write(
            &input_path,
            serde_json::to_vec(&n7.input).expect("live input JSON"),
        )
        .expect("live input");

        let output = preflight(&PreflightLiveInputArgs {
            input: input_path,
            variant: LiveVariantArg::N7,
            trusted_corpus_digest: Some(n7.input.corpus.corpus_digest.to_string()),
            trusted_verifier_profile_digest: Some(
                n7.input.command_verifier.profile_digest.to_string(),
            ),
        })
        .expect("provider-free preflight");
        assert_eq!(output.status, 0);
        assert!(!n7.input.request.workspace.root.join(".git").exists());
        assert_eq!(output.value["workspace_prepared"], false);
        let repeated = preflight(&PreflightLiveInputArgs {
            input: n7.root.path().join("live-input.json"),
            variant: LiveVariantArg::N7,
            trusted_corpus_digest: Some(n7.input.corpus.corpus_digest.to_string()),
            trusted_verifier_profile_digest: Some(
                n7.input.command_verifier.profile_digest.to_string(),
            ),
        })
        .expect("repeated provider-free preflight");
        assert_eq!(output.value, repeated.value);
        assert!(!n7.input.request.workspace.root.join(".git").exists());

        let turn = AdapterTurn {
            actions: vec![
                AdapterAction::Effect(EffectRequest {
                    effect_id: "write-product".into(),
                    run_id: n7.input.request.run_id.clone(),
                    kind: EffectKind::WriteFile,
                    program: None,
                    content: Some("ready\n".into()),
                    args: Vec::new(),
                    paths: vec![PathBuf::from("product.txt")],
                    timeout_ms: 0,
                    input_digest: digest_bytes(b"ao.next.file-does-not-exist.v1"),
                }),
                AdapterAction::Verify,
            ],
            usage: TokenUsage::default(),
            model_claimed_success: true,
            control_mutations: Vec::new(),
        };
        let output = execute_with_runners(
            &n7.input,
            LiveVariant::N7,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(Some(&turn))]),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect("exact live path after provider-free preflight");
        assert_eq!(output.status, 0);
        assert_eq!(output.value["terminal_state"], "passed");
        assert_eq!(
            output.value["native_effect_observations"][0]["effect_id"],
            "write-product"
        );
        assert!(
            output.value["native_effect_observations"][0]["output_digest"]
                .as_str()
                .is_some()
        );
    }

    #[test]
    fn provider_free_campaign_preflight_requires_the_exact_envelope_for_n4() {
        let n4 = fixture(LiveVariant::N4);
        let input_path = n4.root.path().join("n4-input.json");
        std::fs::write(
            &input_path,
            serde_json::to_vec(&n4.input).expect("live input JSON"),
        )
        .expect("live input");
        let program = Path::new("/usr/bin/true");

        let Err(error) = preflight_provider_free_row(
            &input_path,
            LiveVariant::N4,
            &n4.input.corpus.corpus_digest,
            &n4.input.command_verifier.profile_digest,
            program,
            &digest_regular_file(program).expect("fake digest"),
        ) else {
            panic!("N4 campaign row used a two-turn, sub-envelope fixture");
        };
        assert_eq!(error.code, "invalid_input");
    }

    #[test]
    fn provider_free_campaign_preflight_rejects_credentials_and_network_hosts_for_n4() {
        for mutation in ["credentials", "network"] {
            let mut n4 = fixture(LiveVariant::N4);
            n4.input.request.limits.max_turns = 1;
            n4.input.request.limits.max_tokens = LIVE_TOKEN_ENVELOPE;
            match mutation {
                "credentials" => {
                    n4.input
                        .request
                        .authority
                        .capabilities
                        .insert(Capability::CredentialAccess);
                }
                "network" => {
                    n4.input.request.authority.network = NetworkPolicy::Allowlisted;
                    n4.input
                        .request
                        .authority
                        .allowed_network_hosts
                        .insert("example.invalid".into());
                }
                _ => unreachable!(),
            }
            let input_path = n4.root.path().join(format!("n4-{mutation}.json"));
            std::fs::write(
                &input_path,
                serde_json::to_vec(&n4.input).expect("live input JSON"),
            )
            .expect("live input");
            let program = Path::new("/usr/bin/true");

            assert!(
                preflight_provider_free_row(
                    &input_path,
                    LiveVariant::N4,
                    &n4.input.corpus.corpus_digest,
                    &n4.input.command_verifier.profile_digest,
                    program,
                    &digest_regular_file(program).expect("fake digest"),
                )
                .is_err(),
                "provider-free campaign accepted {mutation} authority"
            );
        }
    }

    #[cfg(unix)]
    #[test]
    #[allow(
        clippy::too_many_lines,
        reason = "the one-process integration keeps preparation and exact journal order visible"
    )]
    fn provider_journal_orders_the_real_n7_provider_free_path() {
        use std::os::unix::fs::PermissionsExt as _;

        let n7 = fixture(LiveVariant::N7);
        let turn = AdapterTurn {
            actions: vec![
                AdapterAction::Effect(EffectRequest {
                    effect_id: "write-product".into(),
                    run_id: n7.input.request.run_id.clone(),
                    kind: EffectKind::WriteFile,
                    program: None,
                    content: Some("ready\n".into()),
                    args: Vec::new(),
                    paths: vec![PathBuf::from("product.txt")],
                    timeout_ms: 0,
                    input_digest: digest_bytes(b"ao.next.file-does-not-exist.v1"),
                }),
                AdapterAction::Verify,
            ],
            usage: TokenUsage::default(),
            model_claimed_success: false,
            control_mutations: Vec::new(),
        };
        let output_path = n7.root.path().join("fake-output.jsonl");
        std::fs::write(&output_path, codex_output(Some(&turn)).stdout).expect("fake output");
        let program = n7.root.path().join("fake-codex");
        std::fs::write(
            &program,
            format!("#!/bin/sh\nexec /bin/cat '{}'\n", output_path.display()),
        )
        .expect("fake program");
        std::fs::set_permissions(&program, std::fs::Permissions::from_mode(0o700))
            .expect("fake permissions");
        let input_path = n7.root.path().join("fake-live-input.json");
        std::fs::write(
            &input_path,
            serde_json::to_vec(&n7.input).expect("live input JSON"),
        )
        .expect("live input");

        let fake_digest = digest_regular_file(&program).expect("fake digest");
        let preflight = preflight_provider_free_row(
            &input_path,
            LiveVariant::N7,
            &n7.input.corpus.corpus_digest,
            &n7.input.command_verifier.profile_digest,
            &program,
            &fake_digest,
        )
        .expect("provider-free preflight");
        let result = execute_provider_free_row(
            &input_path,
            LiveVariant::N7,
            &n7.input.corpus.corpus_digest,
            &n7.input.command_verifier.profile_digest,
            &program,
            &fake_digest,
            &preflight,
        )
        .expect("provider-free row");

        assert_eq!(result.fake_processes, 1);
        assert_eq!(result.output.status, 0);
        assert_eq!(result.output.value["measurement"]["task_success"], true);
        assert_eq!(
            result.output.value["native_effect_observations"][0]["effect_id"],
            "write-product"
        );
        let journal_root = execution_journal_root(&n7.input);
        assert!(journal_root.join("execution-identity.json").is_file());
        let mut event_paths = std::fs::read_dir(journal_root.join("execution-events"))
            .expect("execution events")
            .map(|entry| entry.expect("event entry").path())
            .collect::<Vec<_>>();
        event_paths.sort();
        let event_kinds = event_paths
            .into_iter()
            .map(|path| {
                let bytes = std::fs::read(path).expect("event");
                serde_json::from_slice::<serde_json::Value>(&bytes).expect("event JSON")["kind"]
                    ["kind"]
                    .as_str()
                    .expect("event kind")
                    .to_string()
            })
            .collect::<Vec<_>>();
        assert_eq!(
            event_kinds,
            [
                "provider_request_intent",
                "provider_process_started",
                "provider_output_retained",
                "provider_capture_index_published",
                "provider_capture_verified",
                "adapter_turn_normalized",
                "effect_intent",
                "effect_completed",
                "verification_started",
                "verifier_recorded",
                "terminal_published",
            ]
        );
        let terminal_records = std::fs::read_dir(&journal_root)
            .expect("journal root")
            .filter_map(Result::ok)
            .filter(|entry| {
                entry.file_name().to_str().is_some_and(|name| {
                    name.starts_with("terminal-") && name.as_bytes().ends_with(b".json")
                })
            })
            .count();
        assert_eq!(terminal_records, 1);
    }

    #[cfg(unix)]
    #[test]
    fn provider_free_execution_rejects_post_preflight_input_drift_before_spawn() {
        use std::os::unix::fs::PermissionsExt as _;

        let mut n7 = fixture(LiveVariant::N7);
        let marker = n7.root.path().join("fake-provider-started");
        let program = n7.root.path().join("fake-codex");
        std::fs::write(
            &program,
            format!("#!/bin/sh\n/usr/bin/touch '{}'\nexit 1\n", marker.display()),
        )
        .expect("fake program");
        std::fs::set_permissions(&program, std::fs::Permissions::from_mode(0o700))
            .expect("fake permissions");
        let digest = digest_regular_file(&program).expect("fake digest");
        let input_path = n7.root.path().join("live-input.json");
        std::fs::write(
            &input_path,
            serde_json::to_vec(&n7.input).expect("live input JSON"),
        )
        .expect("live input");
        let preflight = preflight_provider_free_row(
            &input_path,
            LiveVariant::N7,
            &n7.input.corpus.corpus_digest,
            &n7.input.command_verifier.profile_digest,
            &program,
            &digest,
        )
        .expect("provider-free preflight");
        n7.input.request.authority.issued_by = "drifted-but-valid-operator".into();
        std::fs::write(
            &input_path,
            serde_json::to_vec(&n7.input).expect("drifted live input JSON"),
        )
        .expect("drifted live input");

        let Err(error) = execute_provider_free_row(
            &input_path,
            LiveVariant::N7,
            &n7.input.corpus.corpus_digest,
            &n7.input.command_verifier.profile_digest,
            &program,
            &digest,
            &preflight,
        ) else {
            panic!("post-preflight input drift must fail");
        };
        assert_eq!(error.code, "invalid_input");
        assert!(!marker.exists(), "drifted row spawned fake provider");
    }

    #[cfg(unix)]
    #[test]
    fn digest_bound_fake_runner_counts_direct_n0_invocations() {
        use std::os::unix::fs::PermissionsExt as _;

        let n0 = fixture(LiveVariant::N0);
        let program = n0.root.path().join("fake-ao2");
        std::fs::write(&program, b"#!/bin/sh\nexit 0\n").expect("fake program");
        std::fs::set_permissions(&program, std::fs::Permissions::from_mode(0o700))
            .expect("fake permissions");
        let calls = Arc::new(Mutex::new(0));
        let mut runner = DigestBoundFakeRunner::new(
            &program,
            &digest_regular_file(&program).expect("fake digest"),
            calls.clone(),
        )
        .expect("digest-bound runner");

        let output = runner
            .run(
                &PreparedInvocation {
                    program: program.display().to_string(),
                    args: Vec::new(),
                    stdin: Vec::new(),
                    cwd: n0.input.request.workspace.root.clone(),
                    environment: None,
                    limits: invocation_limits(&n0.input.request).expect("invocation limits"),
                },
                &CancellationToken::new(),
            )
            .expect("direct fake invocation");

        assert_eq!(output.status, 0);
        assert_eq!(*calls.lock().expect("fake call count"), 1);
    }

    #[cfg(unix)]
    #[test]
    fn digest_bound_fake_runner_rejects_a_symlink_program() {
        use std::os::unix::fs::symlink;

        let temporary = TempDir::new().expect("temporary");
        let link = temporary.path().join("fake-provider-link");
        symlink("/usr/bin/true", &link).expect("fake provider symlink");
        let digest = digest_regular_file(&link).expect("linked program digest");

        assert!(
            DigestBoundFakeRunner::new(&link, &digest, Arc::new(Mutex::new(0))).is_err(),
            "provider-free executable must be a regular non-symlink file"
        );
    }

    #[cfg(unix)]
    #[test]
    fn digest_bound_fake_runner_executes_the_bound_descriptor_after_a_path_swap() {
        use std::os::unix::fs::PermissionsExt as _;

        let temporary = TempDir::new().expect("temporary");
        let program = temporary.path().join("fake-provider");
        std::fs::write(&program, b"#!/bin/sh\nprintf 'bound-descriptor\\n'\n")
            .expect("regular fake provider");
        std::fs::set_permissions(&program, std::fs::Permissions::from_mode(0o700))
            .expect("fake permissions");
        let digest = digest_regular_file(&program).expect("fake provider digest");
        let calls = Arc::new(Mutex::new(0));
        let mut runner =
            DigestBoundFakeRunner::new(&program, &digest, calls.clone()).expect("bound fake");
        std::fs::remove_file(&program).expect("remove regular fake");
        std::fs::write(&program, b"#!/bin/sh\nprintf 'replacement-path\\n'\n")
            .expect("replacement fake provider");
        std::fs::set_permissions(&program, std::fs::Permissions::from_mode(0o700))
            .expect("replacement permissions");

        let output = runner
            .run(
                &PreparedInvocation {
                    program: "codex".into(),
                    args: Vec::new(),
                    stdin: Vec::new(),
                    cwd: temporary.path().to_path_buf(),
                    environment: None,
                    limits: InvocationLimits {
                        timeout_ms: 1_000,
                        max_input_bytes: 1_024,
                        max_output_bytes: 1_024,
                    },
                },
                &CancellationToken::new(),
            )
            .expect("held fake descriptor executes");

        assert_eq!(output.status, 0);
        assert_eq!(output.stdout, b"bound-descriptor\n");
        assert_eq!(*calls.lock().expect("fake call count"), 1);
    }

    #[cfg(unix)]
    #[test]
    fn digest_bound_fake_runner_executes_sealed_bytes_after_an_in_place_overwrite() {
        use std::os::unix::fs::PermissionsExt as _;

        let temporary = TempDir::new().expect("temporary");
        let program = temporary.path().join("fake-provider");
        std::fs::write(&program, b"#!/bin/sh\nprintf 'sealed-bytes\\n'\n")
            .expect("regular fake provider");
        std::fs::set_permissions(&program, std::fs::Permissions::from_mode(0o700))
            .expect("fake permissions");
        let digest = digest_regular_file(&program).expect("fake provider digest");
        let calls = Arc::new(Mutex::new(0));
        let mut runner =
            DigestBoundFakeRunner::new(&program, &digest, calls.clone()).expect("bound fake");
        std::fs::write(&program, b"#!/bin/sh\nprintf 'overwritten-path\\n'\n")
            .expect("overwrite bound path inode");

        let output = runner
            .run(
                &PreparedInvocation {
                    program: "codex".into(),
                    args: Vec::new(),
                    stdin: Vec::new(),
                    cwd: temporary.path().to_path_buf(),
                    environment: None,
                    limits: InvocationLimits {
                        timeout_ms: 1_000,
                        max_input_bytes: 1_024,
                        max_output_bytes: 1_024,
                    },
                },
                &CancellationToken::new(),
            )
            .expect("sealed fake descriptor executes");

        assert_eq!(output.stdout, b"sealed-bytes\n");
        assert_eq!(*calls.lock().expect("fake call count"), 1);
    }

    #[cfg(unix)]
    #[test]
    #[allow(
        clippy::too_many_lines,
        reason = "one typed 27-row fixture keeps the sealed campaign identities auditable"
    )]
    fn provider_free_campaign_executes_all_27_rows_with_real_fake_processes() {
        use std::os::unix::fs::PermissionsExt as _;

        struct TaskAssets {
            task_id: &'static str,
            objective_text: String,
            objective: PathBuf,
            source_snapshot: PathBuf,
            source_digest: Digest,
            visible: PathBuf,
            visible_digest: Digest,
            hidden: PathBuf,
            hidden_digest: Digest,
            command_verifier: CommandVerifierProfile,
        }

        const TASK_IDS: [&str; 3] = [
            "greenfield-engineering-app",
            "bounded-defect-repair",
            "artifact-reconciliation",
        ];
        const CAMPAIGN_SCHEDULE: [(ExecutionVariant, u32, u32); 9] = [
            (ExecutionVariant::N0, 0, 0),
            (ExecutionVariant::N4, 0, 1),
            (ExecutionVariant::N7, 0, 2),
            (ExecutionVariant::N4, 1, 3),
            (ExecutionVariant::N7, 1, 4),
            (ExecutionVariant::N0, 1, 5),
            (ExecutionVariant::N7, 2, 6),
            (ExecutionVariant::N0, 2, 7),
            (ExecutionVariant::N4, 2, 8),
        ];
        const FAKE: &str = r#"#!/usr/bin/python3
import json
import re
import sys
from pathlib import Path

args = sys.argv[1:]
digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

def codex_output(turn=None):
    events = []
    if turn is not None:
        events.append({"type": "item.completed", "item": {
            "type": "agent_message", "text": json.dumps(turn, separators=(",", ":"))}})
    events.append({"type": "turn.completed", "usage": {
        "input_tokens": 11, "cached_input_tokens": 3,
        "reasoning_tokens": 2, "output_tokens": 2}})
    return "\n".join(json.dumps(event, separators=(",", ":")) for event in events) + "\n"

if args[:2] == ["adapter", "run"]:
    target = Path(args[args.index("--target") + 1])
    sandbox = target.parent / (target.name + ".fake-sandbox")
    sandbox.mkdir()
    print(json.dumps({
        "adapter": {"provider": "codex", "role_id": "ao-next-n0-worker-01",
            "command": "codex exec", "exit_code": 0, "stdout": codex_output(),
            "stderr": "", "transcript": "", "blocker": None},
        "target_repo": str(target), "sandbox_path": str(sandbox),
        "changed_files": ["product.txt"], "diff_summary": "added product.txt",
        "transcript_summary": {"changed_files": ["product.txt"], "concerns": [],
            "blockers": [], "usage": {"input_tokens": 0, "output_tokens": 0,
            "total_tokens": 0}, "cost_usd": None, "raw_summary": None,
            "transcript_ids": []}}, separators=(",", ":")))
elif args[:3] == ["adapter", "patch", "preview"]:
    print(json.dumps({"action_digest": digest}, separators=(",", ":")))
elif args[:3] == ["adapter", "patch", "apply"]:
    target = Path(args[args.index("--target") + 1])
    (target / "product.txt").write_text("ready\n")
    print(json.dumps({"action_digest": digest}, separators=(",", ":")))
else:
    prompt = sys.stdin.read()
    match = re.search(r"campaign-run-(\d{2})", prompt)
    if match is None:
        raise SystemExit("missing campaign run identity")
    ordinal = int(match.group(1))
    if "workspace-write" in args:
        if ordinal != 1:
            (Path.cwd() / "product.txt").write_text("ready\n")
        sys.stdout.write(codex_output())
    else:
        run_id = match.group(0)
        turn = {"actions": [
            {"kind": "effect", "value": {"effect_id": "write-product",
                "run_id": run_id, "kind": "write_file", "program": None,
                "content": "ready\n", "args": [], "paths": ["product.txt"],
                "timeout_ms": 0,
                "input_digest": "sha256:0a65f55a486b344123e64dee533170fef69a3520e70ec67bee039f12332f92a0"}},
            {"kind": "verify"}],
            "usage": {"input_tokens": 11, "cached_input_tokens": 3,
                "reasoning_tokens": 2, "output_tokens": 2, "output_bytes": 0},
            "model_claimed_success": True, "control_mutations": []}
        sys.stdout.write(codex_output(turn))
"#;

        let campaign = TempDir::new().expect("campaign temporary");
        let fake_program = campaign.path().join("fake-provider.py");
        std::fs::write(&fake_program, FAKE).expect("fake provider");
        std::fs::set_permissions(&fake_program, std::fs::Permissions::from_mode(0o700))
            .expect("fake permissions");
        let fake_digest = digest_regular_file(&fake_program).expect("fake digest");
        let current_ao = CurrentAoBinding {
            schema_version: "ao.next.current-ao-binding.v1".into(),
            ao2_program: fake_program.clone(),
            ao2_program_digest: fake_digest.clone(),
            provider_program: fake_program.clone(),
            provider_program_digest: fake_digest.clone(),
            adapter_version: "current-ao-native-v1".into(),
        };
        let mut profiles = vec![
            profile(ExecutionVariant::N0, "current-ao", "current-ao-native-v1"),
            profile(ExecutionVariant::N4, "codex", "native-codex-direct-v1"),
            profile(ExecutionVariant::N7, "ao-next-codex", "ao-next-process-v1"),
        ];
        profiles[0].adapter_digest =
            canonical_digest(&current_ao).expect("current-AO adapter digest");
        let workspace_digest =
            canonical_digest(&Vec::<SnapshotEntry>::new()).expect("empty workspace digest");
        let output_schema = campaign.path().join("turn.schema.json");
        std::fs::write(&output_schema, ADAPTER_TURN_SCHEMA_BYTES).expect("turn schema");

        let mut assets = Vec::new();
        for task_id in TASK_IDS {
            let protected = campaign.path().join("tasks").join(task_id);
            let visible = protected.join("visible");
            let hidden = protected.join("hidden");
            std::fs::create_dir_all(&visible).expect("visible fixtures");
            std::fs::create_dir_all(&hidden).expect("hidden fixtures");
            std::fs::write(visible.join("example.txt"), b"visible\n").expect("visible fixture");
            let hidden_program = hidden.join("test_product.py");
            std::fs::write(
                &hidden_program,
                b"from pathlib import Path\nassert Path('product.txt').read_text() == 'ready\\n'\n",
            )
            .expect("hidden test");
            let objective_text = format!(
                "For {task_id}, create product.txt containing ready followed by a newline."
            );
            let objective = protected.join("objective.md");
            std::fs::write(&objective, &objective_text).expect("objective");
            let source = SourceSnapshot {
                schema_version: "ao.next.source-snapshot.v1".into(),
                task_id: task_id.into(),
                tree_digest: workspace_digest.clone(),
                files: Vec::new(),
            };
            let source_digest = canonical_digest(&source).expect("source digest");
            let source_snapshot = protected.join("source-snapshot.json");
            std::fs::write(
                &source_snapshot,
                serde_json::to_vec(&source).expect("source JSON"),
            )
            .expect("source snapshot");
            let mut entry = CommandVerifierEntry {
                verifier_id: format!("{task_id}-hidden-product-check"),
                verifier_digest: digest_bytes(b"unsealed verifier entry"),
                program: "/usr/bin/python3".into(),
                args: vec![hidden_program.display().to_string()],
                working_directory: PathBuf::new(),
                timeout_ms: 5_000,
                max_output_bytes: 16 * 1024,
                expected_exit_status: 0,
                required_artifacts: Vec::new(),
            };
            entry.verifier_digest = entry.calculated_digest().expect("verifier entry digest");
            let mut command_verifier = CommandVerifierProfile {
                schema_version: "ao.next.command-verifier-profile.v1".into(),
                profile_id: format!("{task_id}-verifier-v1"),
                profile_digest: digest_bytes(b"unsealed verifier profile"),
                entries: vec![entry],
            };
            command_verifier.profile_digest = command_verifier
                .calculated_digest()
                .expect("verifier profile digest");
            assets.push(TaskAssets {
                task_id,
                objective_text,
                objective,
                source_snapshot,
                source_digest,
                visible_digest: canonical_digest(
                    &snapshot_tree(&visible, 64 * 1024).expect("visible snapshot"),
                )
                .expect("visible digest"),
                visible,
                hidden_digest: canonical_digest(
                    &snapshot_tree(&hidden, 64 * 1024).expect("hidden snapshot"),
                )
                .expect("hidden digest"),
                hidden,
                command_verifier,
            });
        }

        let mut corpus = CorpusManifest {
            schema_version: "ao.next.evaluation-corpus.v2".into(),
            corpus_kind: CorpusKind::SealedLive,
            corpus_digest: digest_bytes(b"unsealed campaign corpus"),
            required_trial_count: 3,
            schedule: counterbalanced_schedule(),
            tasks: assets
                .iter()
                .map(|asset| EvaluationTask {
                    task_id: asset.task_id.into(),
                    task_kind: "sealed_local_task".into(),
                    source_digest: asset.source_digest.clone(),
                    objective_digest: digest_bytes(asset.objective_text.as_bytes()),
                    workspace_seed_digest: workspace_digest.clone(),
                    visible_fixtures_digest: asset.visible_digest.clone(),
                    hidden_tests_digest: asset.hidden_digest.clone(),
                    verifier_profile_digest: asset.command_verifier.profile_digest.clone(),
                    variant_profiles: profiles.clone(),
                })
                .collect(),
        };
        corpus.corpus_digest = corpus.calculated_digest().expect("campaign corpus digest");

        let input_root = campaign.path().join("inputs");
        std::fs::create_dir(&input_root).expect("input root");
        let mut fixtures = Vec::new();
        let mut rows = Vec::new();
        for (task_index, asset) in assets.iter().enumerate() {
            for (schedule_index, (variant, trial_index, schedule_position)) in
                CAMPAIGN_SCHEDULE.iter().copied().enumerate()
            {
                let ordinal = task_index * CAMPAIGN_SCHEDULE.len() + schedule_index;
                let live_variant = match variant {
                    ExecutionVariant::N0 => LiveVariant::N0,
                    ExecutionVariant::N4 => LiveVariant::N4,
                    ExecutionVariant::N7 => LiveVariant::N7,
                };
                let mut fixture = fixture(live_variant);
                let selected_profile = profiles
                    .iter()
                    .find(|profile| profile.variant == variant)
                    .expect("variant profile");
                let run_id = format!("campaign-run-{ordinal:02}");
                let workspace_id = format!("campaign-workspace-{ordinal:02}");
                fixture.input.corpus = corpus.clone();
                fixture.input.task_id = asset.task_id.into();
                fixture.input.trial_id = format!("campaign-trial-{ordinal:02}");
                fixture.input.trial_index = trial_index;
                fixture.input.schedule_position = schedule_position;
                fixture.input.workspace_instance_id = workspace_id.clone();
                fixture.input.source_snapshot = asset.source_snapshot.clone();
                fixture.input.objective = asset.objective.clone();
                fixture.input.visible_fixtures = asset.visible.clone();
                fixture.input.hidden_tests = asset.hidden.clone();
                fixture.input.output_schema = output_schema.clone();
                fixture.input.command_verifier = asset.command_verifier.clone();
                fixture.input.current_ao =
                    (variant == ExecutionVariant::N0).then(|| current_ao.clone());
                fixture.input.request.run_id = run_id;
                fixture.input.request.objective = asset.objective_text.clone();
                fixture.input.request.source = SourceIdentity {
                    repository: format!("sealed-local-campaign/{}", asset.task_id),
                    head: asset.source_digest.clone(),
                };
                fixture.input.request.workspace.workspace_id = workspace_id;
                fixture.input.request.workspace.seed_digest = workspace_digest.clone();
                fixture.input.request.model_profile.model_identifier =
                    selected_profile.model_identifier.clone();
                fixture.input.request.model_profile.system_prompt_digest =
                    selected_profile.prompt_digest.clone();
                fixture.input.request.model_profile.tool_contract_digest =
                    selected_profile.adapter_digest.clone();
                fixture.input.request.model_profile.adapter_version =
                    selected_profile.adapter_version.clone();
                fixture.input.request.policy_digest = selected_profile.policy_digest.clone();
                let verifier_entry = &asset.command_verifier.entries[0];
                fixture.input.request.verifier_profile = VerifierProfile {
                    profile_id: asset.command_verifier.profile_id.clone(),
                    profile_digest: asset.command_verifier.profile_digest.clone(),
                    commands: vec![StructuredCommand {
                        program: verifier_entry.program.clone(),
                        args: verifier_entry.args.clone(),
                        timeout_ms: verifier_entry.timeout_ms,
                    }],
                    required_artifacts: Vec::new(),
                };
                fixture.input.request.limits.max_turns = 1;
                fixture.input.request.limits.max_repair_attempts = 0;
                fixture.input.request.limits.max_tokens = LIVE_TOKEN_ENVELOPE;
                let input_path = input_root.join(format!("{ordinal:02}.json"));
                std::fs::write(
                    &input_path,
                    serde_json::to_vec(&fixture.input).expect("campaign input JSON"),
                )
                .expect("campaign input");
                rows.push(serde_json::json!({"input": input_path, "variant": variant}));
                fixtures.push(fixture);
            }
        }

        let qualification_path = campaign.path().join("qualification.json");
        std::fs::write(
            &qualification_path,
            serde_json::to_vec(&serde_json::json!({
                "schema_version": "ao.next.provider-free-campaign.v2",
                "rows": rows,
                "normalization_root": campaign.path().join("normalized"),
                "evidence_root": campaign.path().join("evidence"),
                "mission_evidence": {"evidence_id": "provider-free-evidence-01", "status": "qualified"},
                "correlation_chain": {"mission_id": "provider-free-mission-01", "correlation_id": "provider-free-correlation-01", "evidence_id": "provider-free-evidence-01"}
            }))
            .expect("qualification JSON"),
        )
        .expect("qualification");
        let output = super::super::campaign::execute(&crate::commands::QualifyLiveCampaignArgs {
            qualification: qualification_path,
            trusted_corpus_digest: corpus.corpus_digest.to_string(),
            trusted_verifier_profiles: assets
                .iter()
                .map(|asset| {
                    format!(
                        "{}={}",
                        asset.task_id, asset.command_verifier.profile_digest
                    )
                })
                .collect(),
            fake_provider_program: fake_program,
            fake_provider_program_digest: fake_digest.to_string(),
        })
        .expect("real provider-free campaign qualification");

        assert_eq!(output.status, 0);
        assert_eq!(output.value["rows"], 27);
        assert_eq!(output.value["verified_capture_indexes"], 27);
        assert_eq!(output.value["local_fake_processes"], 45);
        assert_eq!(output.value["task_successes"], 26);
        assert_eq!(output.value["valid_task_failures"], 1);
        assert_eq!(output.value["native_write_successes"], 9);
        assert_eq!(
            output.value["security_coverage"],
            serde_json::json!([
                "denied-rg",
                "denied-python3",
                "denied-shell",
                "denied-network",
                "denied-traversal",
                "denied-symlink",
                "denied-oversized-content",
                "denied-stale-preimage",
                "rejected-malformed-action",
                "detected-hidden-test-exposure",
                "verified-evidence-recovery",
                "prevented-duplicate-effect",
                "replayed-interrupted-checkpoint"
            ])
        );
        assert!(
            campaign
                .path()
                .join("evidence/security-coverage.json")
                .is_file()
        );
        let recovery: ao_next_eval::comparison::RecoveryQualification =
            serde_json::from_value(output.value["recovery_qualification"].clone())
                .expect("strict recovery qualification");
        assert_eq!(recovery.corpus_digest, corpus.corpus_digest);
        assert!(recovery.recovery_attempted);
        assert!(recovery.recovery_no_duplicate_effect);
        assert_eq!(recovery.live_provider_processes, 0);
        assert_eq!(
            canonical_digest(&recovery).expect("recovery qualification digest"),
            Digest::new(
                output.value["recovery_qualification_digest"]
                    .as_str()
                    .expect("recovery qualification digest")
            )
            .expect("valid recovery qualification digest")
        );
        let corpus_path = campaign.path().join("recovery-corpus.json");
        std::fs::write(
            &corpus_path,
            serde_json::to_vec(&corpus).expect("recovery corpus JSON"),
        )
        .expect("write recovery corpus");
        let recovery_only =
            super::super::campaign::execute_recovery(&crate::commands::QualifyRecoveryArgs {
                corpus: corpus_path,
                evidence_root: campaign.path().join("recovery-only-evidence"),
            })
            .expect("provider-free recovery-only qualification");
        assert_eq!(recovery_only.status, 0);
        assert_eq!(recovery_only.value["live_provider_processes"], 0);
        assert_eq!(
            recovery_only.value["recovery_qualification"]["corpus_digest"],
            corpus.corpus_digest.as_str()
        );
        assert_eq!(
            output.value["correlation_chain"],
            serde_json::json!({
                "mission_id": "provider-free-mission-01",
                "correlation_id": "provider-free-correlation-01",
                "evidence_id": "provider-free-evidence-01"
            })
        );
        assert_eq!(
            std::fs::read_dir(campaign.path().join("normalized"))
                .expect("normalized rows")
                .count(),
            27
        );
        assert_eq!(fixtures.len(), 27);
    }

    #[test]
    fn live_envelope_derivation_and_observed_usage_boundaries_are_exact() {
        assert_eq!(
            required_live_token_envelope(262_144, 20_000)
                .expect("checked live envelope derivation"),
            564_288
        );
        assert!(required_live_token_envelope(u64::MAX, 1).is_err());
        assert!(required_live_token_envelope(u64::MAX / 2, u64::MAX / 2).is_err());

        let usage = TokenUsage {
            input_tokens: 225_206,
            cached_input_tokens: 193_792,
            reasoning_tokens: 0,
            output_tokens: 6_100,
            output_bytes: 0,
        };
        let capture_digest = digest_bytes(b"observed retained capture");
        let error = validate_trusted_usage(&usage, 425_097, &capture_digest)
            .expect_err("observed usage exceeds 425097");
        assert_eq!(
            error.diagnostic.as_ref().expect("diagnostic")["usage"]["total_tokens"],
            425_098
        );
        assert_eq!(
            validate_trusted_usage(&usage, 425_098, &capture_digest)
                .expect("observed usage passes exact boundary"),
            425_098
        );

        let cached_contradiction = TokenUsage {
            cached_input_tokens: usage.input_tokens + 1,
            ..usage.clone()
        };
        assert!(validate_trusted_usage(&cached_contradiction, u64::MAX, &capture_digest).is_err());
        let reasoning_contradiction = TokenUsage {
            reasoning_tokens: usage.output_tokens + 1,
            ..usage.clone()
        };
        assert!(
            validate_trusted_usage(&reasoning_contradiction, u64::MAX, &capture_digest).is_err()
        );
        let overflow = TokenUsage {
            input_tokens: u64::MAX,
            cached_input_tokens: 1,
            reasoning_tokens: 0,
            output_tokens: 0,
            output_bytes: 0,
        };
        assert!(validate_trusted_usage(&overflow, u64::MAX, &capture_digest).is_err());
    }

    #[test]
    #[allow(
        clippy::too_many_lines,
        reason = "one regression binds the rejected and exact-boundary N0 control counts"
    )]
    fn observed_n0_usage_is_gated_before_preview_apply_and_verifier() {
        let mut rejected = fixture(LiveVariant::N0);
        rejected.input.request.limits.max_tokens = 425_097;
        let sandbox = rejected.root.path().join("ao2-observed-over-limit");
        std::fs::create_dir(&sandbox).expect("current-AO sandbox");
        let mut provider = codex_output_with_usage(None, 225_206, 193_792, 0, 6_100);
        provider.stderr = b"private provider text must remain private".to_vec();
        let counts = Arc::new(Mutex::new(InvocationCounts::default()));
        let cancellation = CancellationToken::new();
        let mut verifier = counting_verifier(&rejected.input, counts.clone(), cancellation.clone());
        let error = execute_n0(
            &rejected.input,
            CountingN0Runner {
                outputs: current_ao_outputs_with_provider(
                    &rejected.input.request.workspace.root,
                    &sandbox,
                    provider,
                ),
                counts: counts.clone(),
                apply_product: Some(rejected.product.clone()),
            },
            &capture_context(&rejected.input, LiveVariant::N0),
            &mut verifier,
            &cancellation,
            invocation_limits(&rejected.input.request).expect("invocation limits"),
        )
        .expect_err("425098 trusted tokens must fail at a 425097 boundary");
        assert_eq!(error.code, "runtime_failure");
        let counts = counts.lock().expect("invocation counts");
        assert_eq!(counts.provider, 1);
        assert_eq!(counts.preview, 0);
        assert_eq!(counts.apply, 0);
        assert_eq!(counts.verifier, 0);
        assert!(!rejected.product.exists());
        drop(counts);

        let terminal: serde_json::Value = serde_json::from_slice(
            &std::fs::read(
                rejected
                    .input
                    .raw_capture_root
                    .join("capture-terminal.json"),
            )
            .expect("token-envelope terminal metadata"),
        )
        .expect("terminal JSON");
        assert_eq!(terminal["failure_stage"], "token-envelope");
        assert_eq!(terminal["token_envelope"]["usage"]["input_tokens"], 225_206);
        assert_eq!(
            terminal["token_envelope"]["usage"]["cached_input_tokens"],
            193_792
        );
        assert_eq!(terminal["token_envelope"]["usage"]["reasoning_tokens"], 0);
        assert_eq!(terminal["token_envelope"]["usage"]["output_tokens"], 6_100);
        assert_eq!(terminal["token_envelope"]["usage"]["total_tokens"], 425_098);
        assert!(
            terminal["token_envelope"]["capture_digest"]
                .as_str()
                .is_some()
        );
        assert!(
            !serde_json::to_string(&terminal)
                .expect("terminal text")
                .contains("private provider text")
        );
        let index_digest = Digest::new(
            terminal["capture_index_digest"]
                .as_str()
                .expect("capture index digest"),
        )
        .expect("valid capture index digest");
        verify_raw_capture_index(
            &rejected.input.raw_capture_root,
            &capture_context(&rejected.input, LiveVariant::N0),
            &index_digest,
            rejected.input.request.limits.max_output_bytes,
        )
        .expect("failure capture remains independently verifiable");

        let mut accepted = fixture(LiveVariant::N0);
        accepted.input.request.limits.max_tokens = 425_098;
        let sandbox = accepted.root.path().join("ao2-observed-boundary");
        std::fs::create_dir(&sandbox).expect("current-AO sandbox");
        let counts = Arc::new(Mutex::new(InvocationCounts::default()));
        let cancellation = CancellationToken::new();
        let mut verifier = counting_verifier(&accepted.input, counts.clone(), cancellation.clone());
        let execution = execute_n0(
            &accepted.input,
            CountingN0Runner {
                outputs: current_ao_outputs_with_provider(
                    &accepted.input.request.workspace.root,
                    &sandbox,
                    codex_output_with_usage(None, 225_206, 193_792, 0, 6_100),
                ),
                counts: counts.clone(),
                apply_product: Some(accepted.product.clone()),
            },
            &capture_context(&accepted.input, LiveVariant::N0),
            &mut verifier,
            &cancellation,
            invocation_limits(&accepted.input.request).expect("invocation limits"),
        )
        .expect("425098 trusted tokens pass at the exact boundary");
        assert_eq!(execution.0, RunState::Passed);
        let counts = counts.lock().expect("invocation counts");
        assert_eq!(counts.provider, 1);
        assert_eq!(counts.preview, 1);
        assert_eq!(counts.apply, 1);
        assert_eq!(counts.verifier, 1);
    }

    #[test]
    fn valid_n0_envelope_runs_each_post_capture_control_once() {
        let mut n0 = fixture(LiveVariant::N0);
        n0.input.request.model_profile.context_limit = 262_144;
        n0.input.request.model_profile.output_limit = 20_000;
        n0.input.request.limits.max_tokens = 564_288;
        let sandbox = n0.root.path().join("ao2-valid-live-envelope");
        std::fs::create_dir(&sandbox).expect("current-AO sandbox");
        let counts = Arc::new(Mutex::new(InvocationCounts::default()));
        let output = execute_with_runners(
            &n0.input,
            LiveVariant::N0,
            MeasurementOrigin::OfflineFixture,
            CountingN0Runner {
                outputs: current_ao_outputs_with_provider(
                    &n0.input.request.workspace.root,
                    &sandbox,
                    codex_output_with_usage(None, 225_206, 193_792, 0, 6_100),
                ),
                counts: counts.clone(),
                apply_product: Some(n0.product.clone()),
            },
            CountingVerifierRunner {
                counts: counts.clone(),
            },
        )
        .expect("valid N0 fake-provider record");
        assert_eq!(output.status, 0);
        assert_eq!(output.value["schema_version"], "ao.next.live-run-record.v1");
        assert_eq!(
            output.value["measurement"]["tokens"]["reported_total_tokens"],
            425_098
        );
        let counts = counts.lock().expect("invocation counts");
        assert_eq!(counts.provider, 1);
        assert_eq!(counts.preview, 1);
        assert_eq!(counts.apply, 1);
        assert_eq!(counts.verifier, 1);
    }

    #[test]
    fn n4_over_limit_capture_skips_verifier_after_native_provider_mutation() {
        let mut n4 = fixture(LiveVariant::N4);
        n4.input.request.limits.max_tokens = 72_000;
        let counts = Arc::new(Mutex::new(InvocationCounts::default()));
        let error = execute_with_runners(
            &n4.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output_with_usage(None, 36_001, 36_000, 0, 0)]),
                direct_write: Some(n4.product.clone()),
                additional_write: None,
            },
            CountingVerifierRunner {
                counts: counts.clone(),
            },
        )
        .expect_err("N4 over-limit usage is not a valid live row");
        assert_eq!(error.code, "runtime_failure");
        assert_eq!(counts.lock().expect("invocation counts").verifier, 0);
        assert_eq!(
            std::fs::read_to_string(&n4.product).expect("native provider product"),
            "ready\n",
            "N4 workspace mutation occurs inside the provider process"
        );
        let terminal: serde_json::Value = serde_json::from_slice(
            &std::fs::read(n4.input.raw_capture_root.join("capture-terminal.json"))
                .expect("token-envelope terminal metadata"),
        )
        .expect("terminal JSON");
        assert_eq!(terminal["failure_stage"], "token-envelope");
    }

    #[test]
    fn n7_contradictory_usage_skips_effects_and_verifier() {
        for (input_tokens, cached_input_tokens, reasoning_tokens, output_tokens) in
            [(11, 12, 0, 1), (11, 3, 2, 1)]
        {
            let n7 = fixture(LiveVariant::N7);
            let effect = EffectRequest {
                effect_id: "write-product".into(),
                run_id: n7.input.request.run_id.clone(),
                kind: EffectKind::RunProgram,
                program: Some("/usr/bin/python3".into()),
                content: None,
                args: vec![
                    "-c".into(),
                    "from pathlib import Path; import sys; Path(sys.argv[1]).write_text('ready\\n')"
                        .into(),
                    n7.product.display().to_string(),
                ],
                paths: vec![n7.product.clone()],
                timeout_ms: 1_000,
                input_digest: digest_bytes(b"write product fixture"),
            };
            let turn = AdapterTurn {
                actions: vec![AdapterAction::Effect(effect), AdapterAction::Verify],
                usage: TokenUsage::default(),
                model_claimed_success: false,
                control_mutations: Vec::new(),
            };
            let counts = Arc::new(Mutex::new(InvocationCounts::default()));
            let error = execute_with_runners(
                &n7.input,
                LiveVariant::N7,
                MeasurementOrigin::OfflineFixture,
                FakeProvider {
                    outputs: VecDeque::from([codex_output_with_usage(
                        Some(&turn),
                        input_tokens,
                        cached_input_tokens,
                        reasoning_tokens,
                        output_tokens,
                    )]),
                    direct_write: None,
                    additional_write: None,
                },
                CountingVerifierRunner {
                    counts: counts.clone(),
                },
            )
            .expect_err("contradictory N7 trusted usage is not a valid live row");
            assert_eq!(error.code, "runtime_failure");
            assert_eq!(counts.lock().expect("invocation counts").verifier, 0);
            assert!(!n7.product.exists(), "N7 admitted effect must not execute");
            let terminal: serde_json::Value = serde_json::from_slice(
                &std::fs::read(n7.input.raw_capture_root.join("capture-terminal.json"))
                    .expect("token-envelope terminal metadata"),
            )
            .expect("terminal JSON");
            assert_eq!(terminal["failure_stage"], "token-envelope");
        }
    }

    #[test]
    fn n0_prepares_canonical_git_base_before_real_preview_boundary() {
        let n0 = fixture(LiveVariant::N0);
        let sandbox = n0.root.path().join("ao2-sandbox-git-check");
        std::fs::create_dir_all(&sandbox).expect("current-AO sandbox");
        let adapter_output = current_ao_outputs(&n0.input.request.workspace.root, &sandbox)
            .pop_front()
            .expect("adapter output");

        let output = execute_with_runners(
            &n0.input,
            LiveVariant::N0,
            MeasurementOrigin::OfflineFixture,
            GitCheckingN0Runner {
                adapter_output,
                workspace: n0.input.request.workspace.root.clone(),
                product: n0.product.clone(),
            },
            BoundedProcessRunner,
        )
        .expect("deterministic Git workspace enables real N0 preview boundary");

        assert_eq!(output.status, 0);
        assert!(n0.input.request.workspace.root.join(".git").is_dir());
    }

    #[test]
    #[allow(
        clippy::too_many_lines,
        reason = "one Git identity regression covers stable product, control, and index bindings"
    )]
    fn git_preparation_is_deterministic_seed_bound_and_product_neutral() {
        let mut identities = Vec::new();
        for _ in 0..3 {
            let temporary = TempDir::new().expect("workspace parent");
            let workspace = temporary.path().join("workspace");
            std::fs::create_dir(&workspace).expect("workspace");
            std::fs::write(workspace.join("product.txt"), b"sealed product\n")
                .expect("sealed product");
            let before = snapshot_tree(&workspace, 64 * 1024).expect("snapshot before Git");
            let identity = prepare_git_workspace(
                &workspace,
                std::slice::from_ref(&workspace),
                &digest_bytes(b"same seed"),
            )
            .expect("deterministic Git workspace");
            let after = snapshot_product_tree(&workspace, 64 * 1024, Some(&identity))
                .expect("snapshot after Git");
            assert_eq!(before, after);
            verify_git_workspace(&identity, true).expect("clean prepared workspace");
            identities.push((temporary, identity));
        }
        assert_eq!(identities[0].1.head_commit, identities[1].1.head_commit);
        assert_eq!(identities[1].1.head_commit, identities[2].1.head_commit);

        let empty_parent = TempDir::new().expect("empty workspace parent");
        let empty = empty_parent.path().join("empty");
        std::fs::create_dir(&empty).expect("empty workspace");
        let empty_identity = prepare_git_workspace(
            &empty,
            std::slice::from_ref(&empty),
            &digest_bytes(b"same seed"),
        )
        .expect("allowed empty seed commit");
        verify_git_workspace(&empty_identity, true).expect("empty seed is clean");

        let changed_parent = TempDir::new().expect("changed seed parent");
        let changed = changed_parent.path().join("workspace");
        std::fs::create_dir(&changed).expect("changed seed workspace");
        std::fs::write(changed.join("product.txt"), b"sealed product\n").expect("sealed product");
        let changed_identity = prepare_git_workspace(
            &changed,
            std::slice::from_ref(&changed),
            &digest_bytes(b"different seed"),
        )
        .expect("different seed workspace");
        assert_ne!(identities[0].1.head_commit, changed_identity.head_commit);

        std::fs::write(
            identities[0].1.repository_root.join("product.txt"),
            b"dirty\n",
        )
        .expect("dirty seed");
        let error = verify_git_workspace(&identities[0].1, true).expect_err("dirty seed rejected");
        assert!(error.message.contains("dirty before provider spawn"));

        std::fs::write(
            identities[0].1.repository_root.join("product.txt"),
            b"sealed product\n",
        )
        .expect("restore seed");
        verify_git_workspace(&identities[0].1, true).expect("restored seed is clean");

        let mut wrong_head = identities[0].1.clone();
        wrong_head.head_commit = "0".repeat(40);
        assert!(verify_git_workspace(&wrong_head, true).is_err());
        let mut wrong_branch = identities[0].1.clone();
        wrong_branch.branch = "wrong-branch".into();
        assert!(verify_git_workspace(&wrong_branch, true).is_err());
        let mut wrong_common_dir = identities[0].1.clone();
        wrong_common_dir.common_dir = wrong_common_dir.repository_root.join("missing-common-dir");
        assert!(verify_git_workspace(&wrong_common_dir, true).is_err());

        let untracked = identities[0].1.repository_root.join("untracked.txt");
        std::fs::write(&untracked, b"untracked\n").expect("untracked product");
        assert!(verify_git_workspace(&identities[0].1, true).is_err());
        std::fs::remove_file(untracked).expect("remove untracked product");
        verify_git_workspace(&identities[0].1, true).expect("untracked product removed");

        let config = identities[0].1.common_dir.join("config");
        let config_bytes = std::fs::read(&config).expect("Git config bytes");
        let mut changed_config = config_bytes.clone();
        changed_config.extend_from_slice(b"\n[ao-next]\nmarker = true\n");
        std::fs::write(&config, changed_config).expect("change Git control data");
        assert!(
            verify_git_workspace(&identities[0].1, false).is_err(),
            "post-run Git control mutation escaped identity verification"
        );
        std::fs::write(&config, config_bytes).expect("restore Git config");
        verify_git_workspace(&identities[0].1, false).expect("restored Git control data");

        run_git_checked(
            &identities[0].1.repository_root,
            vec![
                "rm".into(),
                "--cached".into(),
                "--".into(),
                "product.txt".into(),
            ],
            "mutate index projection",
        )
        .expect("valid staged index mutation");
        assert!(
            verify_git_workspace(&identities[0].1, false).is_err(),
            "logical Git index mutation escaped identity verification"
        );
        run_git_checked(
            &identities[0].1.repository_root,
            vec!["add".into(), "--".into(), "product.txt".into()],
            "restore index projection",
        )
        .expect("restore staged index");
        verify_git_workspace(&identities[0].1, false).expect("restored Git index projection");

        run_git_checked(
            &identities[0].1.repository_root,
            vec![
                "update-index".into(),
                "--skip-worktree".into(),
                "--".into(),
                "product.txt".into(),
            ],
            "mutate index flags",
        )
        .expect("valid index flag mutation");
        assert!(
            verify_git_workspace(&identities[0].1, false).is_err(),
            "Git index flags escaped identity verification"
        );
        run_git_checked(
            &identities[0].1.repository_root,
            vec![
                "update-index".into(),
                "--no-skip-worktree".into(),
                "--".into(),
                "product.txt".into(),
            ],
            "restore index flags",
        )
        .expect("restore index flags");
        verify_git_workspace(&identities[0].1, false).expect("restored Git index flags");

        std::fs::write(identities[0].1.common_dir.join("index"), b"invalid index")
            .expect("corrupt index");
        assert!(verify_git_workspace(&identities[0].1, true).is_err());
    }

    #[test]
    fn single_provider_process_refuses_a_second_start() {
        let temporary = TempDir::new().expect("temporary");
        let invocation = PreparedInvocation {
            program: "/usr/bin/true".into(),
            args: Vec::new(),
            stdin: Vec::new(),
            cwd: temporary.path().to_path_buf(),
            environment: None,
            limits: InvocationLimits {
                max_input_bytes: 0,
                max_output_bytes: 0,
                timeout_ms: 100,
            },
        };
        let mut provider = SingleProviderProcess::new(FixedProcessResult {
            result: Some(Ok(InvocationOutput {
                status: 0,
                stdout: Vec::new(),
                stderr: Vec::new(),
            })),
        });
        provider
            .run(&invocation, &CancellationToken::new())
            .expect("first provider start");
        let error = provider
            .run(&invocation, &CancellationToken::new())
            .expect_err("second provider start rejected");
        assert!(error.to_string().contains("budget exhausted"));
    }

    #[test]
    fn git_preparation_rejects_existing_nested_and_unsafe_metadata() {
        for kind in ["directory", "case-variant", "file", "nested", "submodule"] {
            let temporary = TempDir::new().expect("workspace parent");
            let workspace = temporary.path().join("workspace");
            std::fs::create_dir(&workspace).expect("workspace");
            match kind {
                "directory" => std::fs::create_dir(workspace.join(".git")).expect("Git dir"),
                "case-variant" => {
                    std::fs::create_dir(workspace.join(".GIT")).expect("case-variant Git dir");
                }
                "file" => std::fs::write(workspace.join(".git"), b"gitdir: elsewhere\n")
                    .expect("Git file"),
                "nested" => {
                    std::fs::create_dir(workspace.join("nested")).expect("nested");
                    std::fs::create_dir(workspace.join("nested/.git")).expect("nested Git dir");
                }
                "submodule" => {
                    std::fs::create_dir(workspace.join("nested")).expect("nested");
                    std::fs::write(workspace.join("nested/.git"), b"gitdir: ../../modules/x\n")
                        .expect("submodule marker");
                }
                _ => unreachable!(),
            }
            let error = prepare_git_workspace(
                &workspace,
                std::slice::from_ref(&workspace),
                &digest_bytes(b"seed"),
            )
            .expect_err("unexpected Git metadata rejected");
            assert!(error.message.contains("Git metadata"));
        }

        #[cfg(unix)]
        {
            use std::os::unix::fs::symlink;

            let temporary = TempDir::new().expect("symlink parent");
            let workspace = temporary.path().join("workspace");
            std::fs::create_dir(&workspace).expect("workspace");
            symlink(temporary.path(), workspace.join("nested-link")).expect("nested symlink");
            assert!(
                prepare_git_workspace(
                    &workspace,
                    std::slice::from_ref(&workspace),
                    &digest_bytes(b"seed")
                )
                .is_err()
            );

            let link = temporary.path().join("workspace-link");
            symlink(&workspace, &link).expect("workspace symlink");
            assert!(prepare_git_workspace(&link, &[workspace], &digest_bytes(b"seed")).is_err());

            let git_symlink_parent = TempDir::new().expect("Git symlink parent");
            let git_symlink_workspace = git_symlink_parent.path().join("workspace");
            let git_symlink_target = git_symlink_parent.path().join("Git-target");
            std::fs::create_dir(&git_symlink_workspace).expect("Git symlink workspace");
            std::fs::create_dir(&git_symlink_target).expect("Git symlink target");
            symlink(&git_symlink_target, git_symlink_workspace.join(".git"))
                .expect("root Git symlink");
            assert!(
                prepare_git_workspace(
                    &git_symlink_workspace,
                    std::slice::from_ref(&git_symlink_workspace),
                    &digest_bytes(b"seed")
                )
                .is_err()
            );
        }
    }

    #[test]
    fn deterministic_git_environment_clears_host_identity_and_configuration() {
        let temporary = TempDir::new().expect("temporary");
        let program = git_program().expect("Git program");
        let environment = git_environment(temporary.path(), &program);
        for (name, value) in [
            ("GIT_CONFIG_NOSYSTEM", "1"),
            ("GIT_AUTHOR_NAME", "AO Next"),
            ("GIT_AUTHOR_EMAIL", "ao-next@invalid"),
            ("GIT_COMMITTER_NAME", "AO Next"),
            ("GIT_COMMITTER_EMAIL", "ao-next@invalid"),
            ("GIT_AUTHOR_DATE", GIT_TIMESTAMP),
            ("GIT_COMMITTER_DATE", GIT_TIMESTAMP),
        ] {
            assert_eq!(environment.get(name).map(String::as_str), Some(value));
        }
        assert_eq!(
            environment.get("HOME").map(String::as_str),
            temporary.path().to_str()
        );
        assert!(environment.contains_key("PATH"));
    }

    #[test]
    fn git_process_failures_timeouts_and_oversized_output_fail_closed() {
        let mut failed = FixedProcessResult {
            result: Some(Ok(InvocationOutput {
                status: 1,
                stdout: Vec::new(),
                stderr: b"fatal: injected Git failure\n".to_vec(),
            })),
        };
        let error = run_git_checked_with_runner(
            Path::new("/"),
            Vec::new(),
            "injected failure",
            &mut failed,
        )
        .expect_err("nonzero Git exit rejected");
        assert!(error.message.contains("injected Git failure"));

        let mut timed_out = FixedProcessResult {
            result: Some(Err(InvocationError::TimedOut)),
        };
        let error = run_git_checked_with_runner(
            Path::new("/"),
            Vec::new(),
            "injected timeout",
            &mut timed_out,
        )
        .expect_err("Git timeout rejected");
        assert!(error.message.contains("timed out"));

        let mut oversized = FixedProcessResult {
            result: Some(Ok(InvocationOutput {
                status: 0,
                stdout: vec![b'x'; GIT_OUTPUT_LIMIT + 1],
                stderr: Vec::new(),
            })),
        };
        let error = run_git_checked_with_runner(
            Path::new("/"),
            Vec::new(),
            "injected oversized output",
            &mut oversized,
        )
        .expect_err("oversized Git output rejected");
        assert!(error.message.contains("output exceeds"));
    }

    #[test]
    fn n0_retains_exact_provider_bytes_before_preview_failure() {
        let n0 = fixture(LiveVariant::N0);
        let sandbox = n0.root.path().join("ao2-sandbox-preview-failure");
        std::fs::create_dir_all(&sandbox).expect("current-AO sandbox");
        let mut outputs = current_ao_outputs(&n0.input.request.workspace.root, &sandbox);
        let expected = decode_current_ao_output(
            outputs.front().expect("adapter output"),
            &n0.input.request.workspace.root,
            invocation_limits(&n0.input.request).expect("limits"),
        )
        .expect("provider bytes");
        outputs.get_mut(1).expect("preview output").status = 1;
        outputs.get_mut(1).expect("preview output").stderr =
            b"fatal: resolve Git common directory\n".to_vec();

        execute_with_runners(
            &n0.input,
            LiveVariant::N0,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs,
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("preview failure remains an infrastructure failure");

        assert_eq!(
            std::fs::read(n0.input.raw_capture_root.join("capture-000.stdout"))
                .expect("retained stdout"),
            expected.stdout
        );
        assert_eq!(
            std::fs::read(n0.input.raw_capture_root.join("capture-000.stderr"))
                .expect("retained stderr"),
            expected.stderr
        );
        assert!(
            n0.input
                .raw_capture_root
                .join("capture-index.json")
                .is_file()
        );
        let terminal: serde_json::Value = serde_json::from_slice(
            &std::fs::read(n0.input.raw_capture_root.join("capture-terminal.json"))
                .expect("capture terminal metadata"),
        )
        .expect("capture terminal JSON");
        assert_eq!(terminal["failure_stage"], "preview");
        assert!(terminal["capture_index_digest"].as_str().is_some());
        assert_eq!(
            std::fs::read_dir(&n0.input.raw_capture_root)
                .expect("capture root")
                .count(),
            4
        );
    }

    #[test]
    fn n0_preview_failure_exposes_bounded_structured_ao2_diagnostic() {
        let n0 = fixture(LiveVariant::N0);
        let sandbox = n0.root.path().join("ao2-sandbox-diagnostic");
        std::fs::create_dir_all(&sandbox).expect("current-AO sandbox");
        let mut outputs = current_ao_outputs(&n0.input.request.workspace.root, &sandbox);
        outputs.get_mut(1).expect("preview output").status = 1;
        outputs.get_mut(1).expect("preview output").stderr =
            b"Error: resolve Git common directory\n".to_vec();
        let expected_program_digest = n0
            .input
            .current_ao
            .as_ref()
            .expect("current-AO binding")
            .ao2_program_digest
            .clone();

        let error = execute_with_runners(
            &n0.input,
            LiveVariant::N0,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs,
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("preview failure");
        let diagnostic = error.diagnostic.expect("structured AO2 diagnostic");
        assert_eq!(diagnostic["stage"], "preview");
        assert_eq!(diagnostic["exit_status"], 1);
        assert_eq!(
            diagnostic["program_digest"],
            expected_program_digest.as_str()
        );
        assert!(diagnostic["elapsed_ms"].as_u64().is_some());
        assert!(
            diagnostic["stderr"]["bounded_text"]
                .as_str()
                .is_some_and(|text| text.contains("resolve Git common directory"))
        );
        assert!(diagnostic["stderr"]["digest"].as_str().is_some());
        assert_eq!(diagnostic["command"][0], "adapter");
        assert!(diagnostic["target_identity"].as_str().is_some());
        assert!(diagnostic["sandbox_identity"].as_str().is_some());
    }

    #[test]
    fn n0_retains_provider_bytes_and_diagnostics_before_apply_failure() {
        let n0 = fixture(LiveVariant::N0);
        let sandbox = n0.root.path().join("ao2-sandbox-apply-failure");
        std::fs::create_dir_all(&sandbox).expect("current-AO sandbox");
        let mut outputs = current_ao_outputs(&n0.input.request.workspace.root, &sandbox);
        let expected = decode_current_ao_output(
            outputs.front().expect("adapter output"),
            &n0.input.request.workspace.root,
            invocation_limits(&n0.input.request).expect("limits"),
        )
        .expect("provider bytes");
        outputs.get_mut(2).expect("apply output").status = 1;
        outputs.get_mut(2).expect("apply output").stderr = b"apply rejected\n".to_vec();

        let error = execute_with_runners(
            &n0.input,
            LiveVariant::N0,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs,
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("apply failure remains an infrastructure failure");
        assert_eq!(
            std::fs::read(n0.input.raw_capture_root.join("capture-000.stdout"))
                .expect("retained stdout"),
            expected.stdout
        );
        assert_eq!(
            error
                .diagnostic
                .as_ref()
                .and_then(|diagnostic| diagnostic["stage"].as_str()),
            Some("apply")
        );
        assert!(
            error
                .diagnostic
                .as_ref()
                .and_then(|diagnostic| diagnostic["stderr"]["bounded_text"].as_str())
                .is_some_and(|text| text.contains("apply rejected"))
        );
        let terminal: serde_json::Value = serde_json::from_slice(
            &std::fs::read(n0.input.raw_capture_root.join("capture-terminal.json"))
                .expect("capture terminal metadata"),
        )
        .expect("capture terminal JSON");
        assert_eq!(terminal["failure_stage"], "apply");
    }

    #[test]
    fn n4_retains_provider_bytes_before_verifier_execution() {
        let n4 = fixture(LiveVariant::N4);
        let output = execute_with_runners(
            &n4.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(None)]),
                direct_write: Some(n4.product.clone()),
                additional_write: None,
            },
            CaptureCheckingVerifier {
                capture_root: n4.input.raw_capture_root.clone(),
                inner: BoundedProcessRunner,
            },
        )
        .expect("capture-first N4 run");

        assert_eq!(output.status, 0);
    }

    #[test]
    fn n4_retains_provider_bytes_when_verifier_fails() {
        let n4 = fixture(LiveVariant::N4);
        let output = execute_with_runners(
            &n4.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(None)]),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect("verifier failure remains a measured failed fixture");
        assert_eq!(output.status, 5);
        assert_eq!(output.value["measurement"]["task_success"], false);
        assert!(
            n4.input
                .raw_capture_root
                .join("capture-index.json")
                .is_file()
        );
        let terminal: serde_json::Value = serde_json::from_slice(
            &std::fs::read(n4.input.raw_capture_root.join("capture-terminal.json"))
                .expect("capture terminal metadata"),
        )
        .expect("capture terminal JSON");
        assert_eq!(terminal["failure_stage"], "verifier");
    }

    #[test]
    fn invalid_input_starts_no_provider_and_creates_no_capture() {
        let mut invalid = fixture(LiveVariant::N4);
        invalid.input.request.policy_digest = digest_bytes(b"drifted policy");
        let error = execute_with_runners(
            &invalid.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::new(),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("invalid input rejected before provider spawn");
        assert_eq!(error.code, "invalid_input");
        assert_eq!(
            std::fs::read_dir(&invalid.input.raw_capture_root)
                .expect("capture root")
                .count(),
            0
        );
    }

    #[test]
    fn post_git_revalidation_rejects_source_snapshot_drift() {
        let n7 = fixture(LiveVariant::N7);
        let task = n7
            .input
            .corpus
            .tasks
            .iter()
            .find(|task| task.task_id == n7.input.task_id)
            .expect("selected task")
            .clone();
        let git = prepare_git_workspace(
            &n7.input.request.workspace.root,
            &n7.input.request.authority.allowed_roots,
            &n7.input.request.workspace.seed_digest,
        )
        .expect("Git workspace");
        let mut source: serde_json::Value = serde_json::from_slice(
            &std::fs::read(&n7.input.source_snapshot).expect("source bytes"),
        )
        .expect("source snapshot");
        source["task_id"] = serde_json::json!("drifted-source");
        std::fs::write(
            &n7.input.source_snapshot,
            serde_json::to_vec(&source).expect("drifted source bytes"),
        )
        .expect("drifted source snapshot");

        assert!(revalidate_post_git_inputs(&n7.input, &task, &git).is_err());
    }

    #[cfg(unix)]
    #[test]
    fn capture_index_is_atomic_private_and_identity_bound() {
        use std::os::unix::fs::PermissionsExt as _;

        let n4 = fixture(LiveVariant::N4);
        execute_with_runners(
            &n4.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(None)]),
                direct_write: Some(n4.product.clone()),
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect("captured N4 run");

        let index_path = n4.input.raw_capture_root.join("capture-index.json");
        let index: serde_json::Value =
            serde_json::from_slice(&std::fs::read(&index_path).expect("capture index bytes"))
                .expect("capture index JSON");
        assert_eq!(index["run_id"], n4.input.request.run_id);
        assert_eq!(index["trial_id"], n4.input.trial_id);
        assert_eq!(
            index["workspace_instance_id"],
            n4.input.workspace_instance_id
        );
        assert_eq!(index["runtime_identity"]["runtime"], "codex");
        assert_eq!(index["entries"][0]["capture_order"], 0);
        assert_eq!(index["entries"][0]["status"], 0);
        assert!(index["entries"][0]["stdout_size_bytes"].as_u64().is_some());
        assert!(index["entries"][0]["stderr_size_bytes"].as_u64().is_some());
        for path in [
            index_path,
            n4.input.raw_capture_root.join("capture-000.stdout"),
            n4.input.raw_capture_root.join("capture-000.stderr"),
        ] {
            assert_eq!(
                std::fs::metadata(path)
                    .expect("capture metadata")
                    .permissions()
                    .mode()
                    & 0o777,
                0o600
            );
        }
        assert!(
            !n4.input
                .raw_capture_root
                .join("capture-index.json.incomplete")
                .exists()
        );
    }

    #[test]
    fn capture_index_verification_rejects_tampered_bytes_and_contradictions() {
        let tampered = fixture(LiveVariant::N4);
        let output = execute_with_runners(
            &tampered.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(None)]),
                direct_write: Some(tampered.product.clone()),
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect("captured N4 run");
        let expected = Digest::new(
            output.value["raw_capture_index_digest"]
                .as_str()
                .expect("capture index digest"),
        )
        .expect("valid digest");
        let context = capture_context(&tampered.input, LiveVariant::N4);
        verify_raw_capture_index(
            &tampered.input.raw_capture_root,
            &context,
            &expected,
            tampered.input.request.limits.max_output_bytes,
        )
        .expect("untouched capture verifies");
        std::fs::write(
            tampered.input.raw_capture_root.join("capture-000.stdout"),
            b"tampered",
        )
        .expect("tamper retained bytes");
        assert!(
            verify_raw_capture_index(
                &tampered.input.raw_capture_root,
                &context,
                &expected,
                tampered.input.request.limits.max_output_bytes,
            )
            .is_err()
        );

        let contradictory = fixture(LiveVariant::N4);
        let output = execute_with_runners(
            &contradictory.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(None)]),
                direct_write: Some(contradictory.product.clone()),
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect("second captured N4 run");
        let expected = Digest::new(
            output.value["raw_capture_index_digest"]
                .as_str()
                .expect("capture index digest"),
        )
        .expect("valid digest");
        let index_path = contradictory
            .input
            .raw_capture_root
            .join("capture-index.json");
        let mut index: serde_json::Value =
            serde_json::from_slice(&std::fs::read(&index_path).expect("index bytes"))
                .expect("index JSON");
        index["entries"][0]["stdout_size_bytes"] = serde_json::json!(999_999_u64);
        std::fs::write(
            &index_path,
            serde_json::to_vec(&index).expect("index bytes"),
        )
        .expect("contradict index");
        assert!(
            verify_raw_capture_index(
                &contradictory.input.raw_capture_root,
                &capture_context(&contradictory.input, LiveVariant::N4),
                &expected,
                contradictory.input.request.limits.max_output_bytes,
            )
            .is_err()
        );
    }

    #[test]
    fn baseline_record_digest_binds_observed_timing() {
        let fixture = fixture(LiveVariant::N4);
        let output = execute_with_runners(
            &fixture.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(None)]),
                direct_write: Some(fixture.product.clone()),
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect("captured N4 run");
        let record: LiveRunRecord = serde_json::from_value(output.value).expect("live run record");

        for variant in [LiveVariant::N0, LiveVariant::N4] {
            let original = live_record_digest(
                variant,
                &record.terminal_state,
                &record.measurement,
                &record.capture_digests,
                &record.raw_capture_index_digest,
                record.verifier_report_digest.as_ref(),
                record.n7_execution_authority_digest.as_ref(),
                &record.git_workspace,
                &record.ao2_control_diagnostics,
                &record.native_effect_observations,
            )
            .expect("original digest");
            let mut wall_clock_changed = record.measurement.clone();
            wall_clock_changed.wall_clock_ms = wall_clock_changed.wall_clock_ms.saturating_add(1);
            let mut model_wait_changed = record.measurement.clone();
            model_wait_changed.model_wait_ms = model_wait_changed.model_wait_ms.saturating_add(1);
            for changed in [&wall_clock_changed, &model_wait_changed] {
                assert_ne!(
                    live_record_digest(
                        variant,
                        &record.terminal_state,
                        changed,
                        &record.capture_digests,
                        &record.raw_capture_index_digest,
                        record.verifier_report_digest.as_ref(),
                        record.n7_execution_authority_digest.as_ref(),
                        &record.git_workspace,
                        &record.ao2_control_diagnostics,
                        &record.native_effect_observations,
                    )
                    .expect("changed digest"),
                    original,
                    "{variant:?} digest ignored observed timing"
                );
            }
        }
    }

    #[test]
    fn capture_retention_rejects_duplicates_bounds_paths_and_short_writes() {
        struct ZeroWriter;
        impl std::io::Write for ZeroWriter {
            fn write(&mut self, _: &[u8]) -> std::io::Result<usize> {
                Ok(0)
            }

            fn flush(&mut self) -> std::io::Result<()> {
                Ok(())
            }
        }

        let mut zero = ZeroWriter;
        assert_eq!(
            write_all_exact(&mut zero, b"bytes")
                .expect_err("zero-length short write")
                .kind(),
            std::io::ErrorKind::WriteZero
        );

        let duplicate = fixture(LiveVariant::N4);
        let context = capture_context(&duplicate.input, LiveVariant::N4);
        let output = codex_output(None);
        let (index, _) = retain_raw_capture_files(
            &duplicate.input.raw_capture_root,
            &context,
            &[],
            std::slice::from_ref(&output),
            None,
        )
        .expect("first immutable capture");
        let first = publish_raw_capture_index(&duplicate.input.raw_capture_root, &index)
            .expect("first immutable index")
            .digest()
            .clone();
        assert!(
            retain_raw_capture_files(
                &duplicate.input.raw_capture_root,
                &context,
                &[],
                std::slice::from_ref(&output),
                None,
            )
            .is_err()
        );
        verify_raw_capture_index(
            &duplicate.input.raw_capture_root,
            &context,
            &first,
            duplicate.input.request.limits.max_output_bytes,
        )
        .expect("first capture unchanged");

        let oversized = fixture(LiveVariant::N4);
        let mut context = capture_context(&oversized.input, LiveVariant::N4);
        context.maximum_output_bytes = 1;
        assert!(
            retain_raw_capture_files(
                &oversized.input.raw_capture_root,
                &context,
                &[],
                &[InvocationOutput {
                    status: 0,
                    stdout: b"too large".to_vec(),
                    stderr: Vec::new(),
                }],
                None,
            )
            .is_err()
        );
        assert_eq!(
            std::fs::read_dir(&oversized.input.raw_capture_root)
                .expect("empty oversized root")
                .count(),
            0
        );

        assert!(checked_capture_path(&duplicate.input.raw_capture_root, "../escape").is_err());
        #[cfg(unix)]
        {
            use std::os::unix::fs::{PermissionsExt as _, symlink};

            let symlink_root = TempDir::new().expect("symlink capture root");
            let target = symlink_root.path().join("target");
            std::fs::write(&target, b"target").expect("target");
            symlink(&target, symlink_root.path().join("capture-000.stdout"))
                .expect("capture symlink");
            assert!(checked_capture_path(symlink_root.path(), "capture-000.stdout").is_err());

            let unsafe_root = fixture(LiveVariant::N4);
            std::fs::set_permissions(
                &unsafe_root.input.raw_capture_root,
                std::fs::Permissions::from_mode(0o755),
            )
            .expect("unsafe permissions");
            assert!(validate_input(&unsafe_root.input, LiveVariant::N4, Utc::now()).is_err());
        }
    }

    #[test]
    fn capture_index_is_staged_before_output_retained_event_can_fail() {
        let fixture = fixture(LiveVariant::N7);
        let journal = CheckpointJournal::new(
            execution_journal_root(&fixture.input),
            execution_journal_maximum_bytes(&fixture.input.request),
        )
        .expect("journal");
        let event_root = execution_journal_root(&fixture.input).join("execution-events");
        let retained_index = Arc::new(Mutex::new(None));
        let retained_failure = Arc::new(Mutex::new(None));
        let context = capture_context(&fixture.input, LiveVariant::N7);
        let execution_authority_digest = digest_bytes(b"authority");
        let mut runner = CaptureFirstRunner {
            runner: CollidingJournalRunner { event_root },
            raw_capture_root: fixture.input.raw_capture_root.clone(),
            capture_context: context,
            retained_index,
            retained_failure,
            runtime: "codex".into(),
            max_tokens: fixture.input.request.limits.max_tokens,
            journal: Some(&journal),
            request: &fixture.input.request,
            prepared_run_digest: digest_bytes(b"prepared"),
            execution_authority: None,
            execution_authority_digest: Some(&execution_authority_digest),
        };
        let invocation = PreparedInvocation {
            program: "provider".into(),
            args: Vec::new(),
            stdin: Vec::new(),
            cwd: fixture.input.request.workspace.root.clone(),
            environment: None,
            limits: invocation_limits(&fixture.input.request).expect("limits"),
        };

        assert!(runner.run(&invocation, &CancellationToken::new()).is_err());
        assert!(
            fixture
                .input
                .raw_capture_root
                .join("capture-index.json.incomplete")
                .is_file(),
            "canonical incomplete index must precede provider_output_retained"
        );
        assert!(
            !fixture
                .input
                .raw_capture_root
                .join("capture-index.json")
                .exists()
        );
    }

    #[test]
    fn offline_fake_n0_n4_and_n7_execute_without_the_live_environment_gate() {
        assert_ne!(
            std::env::var("AO_NEXT_LIVE_PROVIDER_CALLS").as_deref(),
            Ok("operator-authorized")
        );

        let n0 = fixture(LiveVariant::N0);
        let sandbox = n0.root.path().join("ao2-sandbox-fixture");
        std::fs::create_dir_all(&sandbox).expect("current-AO sandbox");
        let output = execute_with_runners(
            &n0.input,
            LiveVariant::N0,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: current_ao_outputs(&n0.input.request.workspace.root, &sandbox),
                direct_write: None,
                additional_write: Some((n0.product.clone(), b"ready\n".to_vec())),
            },
            BoundedProcessRunner,
        )
        .expect("offline N0");
        assert_eq!(output.status, 0);
        assert!(output.value["n7_execution_authority_digest"].is_null());
        assert_eq!(output.value["measurement"]["variant"], "N0");
        assert_eq!(output.value["measurement"]["tokens"]["input_tokens"], 11);

        let n4 = fixture(LiveVariant::N4);
        let output = execute_with_runners(
            &n4.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(None)]),
                direct_write: Some(n4.product.clone()),
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect("offline N4");
        assert_eq!(output.status, 0);
        assert!(output.value["n7_execution_authority_digest"].is_null());
        assert_eq!(output.value["terminal_state"], "passed");
        assert_eq!(output.value["measurement"]["variant"], "N4");
        assert_eq!(output.value["measurement"]["provider_usage_trusted"], true);
        assert_eq!(
            output.value["measurement"]["measurement_origin"],
            "offline_fixture"
        );
        assert_eq!(output.value["measurement"]["hidden_tests_passed"], 1);
        assert_eq!(output.value["measurement"]["worker_count"], 1);
        assert_eq!(output.value["measurement"]["dynamic_fanout"], false);

        let n7 = fixture(LiveVariant::N7);
        let turn = AdapterTurn {
            actions: vec![
                AdapterAction::Effect(ao_next_core::contracts::EffectRequest {
                    effect_id: "write-product".into(),
                    run_id: n7.input.request.run_id.clone(),
                    kind: ao_next_core::contracts::EffectKind::WriteFile,
                    program: None,
                    content: Some("ready\n".into()),
                    args: Vec::new(),
                    paths: vec![PathBuf::from("product.txt")],
                    timeout_ms: 0,
                    input_digest: digest_bytes(b"ao.next.file-does-not-exist.v1"),
                }),
                AdapterAction::Verify,
            ],
            usage: TokenUsage {
                input_tokens: 999,
                cached_input_tokens: 999,
                reasoning_tokens: 999,
                output_tokens: 999,
                output_bytes: 999,
            },
            model_claimed_success: true,
            control_mutations: Vec::new(),
        };
        let output = execute_with_runners(
            &n7.input,
            LiveVariant::N7,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(Some(&turn))]),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect("offline N7");
        assert_eq!(output.status, 0);
        assert!(output.value["n7_execution_authority_digest"].is_null());
        assert_eq!(output.value["terminal_state"], "passed");
        assert_eq!(output.value["measurement"]["variant"], "N7");
        assert_eq!(output.value["measurement"]["tokens"]["input_tokens"], 11);
        assert_eq!(output.value["measurement"]["hidden_test_exposure"], false);
        assert_eq!(output.value["measurement"]["changed_files"], 1);
        assert_eq!(output.value["measurement"]["worker_count"], 1);
        assert_eq!(output.value["measurement"]["dynamic_fanout"], false);
    }

    #[test]
    fn real_provider_free_ao2_qualification_from_bound_input() {
        let Some(input_path) = std::env::var_os("AO_NEXT_PROVIDER_FREE_INPUT") else {
            return;
        };
        assert!(std::env::var_os("AO_NEXT_LIVE_PROVIDER_CALLS").is_none());
        let variant = match std::env::var("AO_NEXT_PROVIDER_FREE_VARIANT").as_deref() {
            Ok("N0") => LiveVariant::N0,
            Ok("N4") => LiveVariant::N4,
            Ok("N7") => LiveVariant::N7,
            _ => panic!("AO_NEXT_PROVIDER_FREE_VARIANT must be N0, N4, or N7"),
        };
        let input: LiveRunInput = decode_file(Path::new(&input_path)).expect("bound offline input");
        let output = match variant {
            LiveVariant::N0 => execute_with_runners(
                &input,
                variant,
                MeasurementOrigin::OfflineFixture,
                BoundedProcessRunner,
                BoundedProcessRunner,
            ),
            LiveVariant::N4 | LiveVariant::N7 => execute_with_runners(
                &input,
                variant,
                MeasurementOrigin::OfflineFixture,
                ProviderFreeRunner::from_environment().expect("bound provider-free program"),
                BoundedProcessRunner,
            ),
        }
        .expect("real provider-free control path");
        assert_eq!(output.status, 0);
        assert_eq!(
            output.value["measurement"]["measurement_origin"],
            "offline_fixture"
        );
        assert_eq!(output.value["measurement"]["task_success"], true);
        assert_eq!(output.value["measurement"]["worker_count"], 1);
        assert_eq!(output.value["measurement"]["dynamic_fanout"], false);
        assert_eq!(
            output.value["capture_digests"].as_array().map(Vec::len),
            Some(1)
        );
        if variant == LiveVariant::N0 {
            assert_eq!(
                output.value["ao2_control_diagnostics"]
                    .as_array()
                    .map(Vec::len),
                Some(2)
            );
        }
        println!(
            "{}",
            serde_json::to_string(&output.value).expect("qualification record")
        );
    }

    #[test]
    fn drift_and_hidden_material_copy_fail_closed() {
        let mut drift = fixture(LiveVariant::N7);
        drift.input.request.policy_digest = digest_bytes(b"drifted policy");
        let error = execute_with_runners(
            &drift.input,
            LiveVariant::N7,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::new(),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("policy drift");
        assert_eq!(error.code, "invalid_input");

        let mut effort_drift = fixture(LiveVariant::N7);
        effort_drift.input.request.model_profile.reasoning_effort = "low".into();
        let error = execute_with_runners(
            &effort_drift.input,
            LiveVariant::N7,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::new(),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("reasoning effort drift");
        assert_eq!(error.code, "invalid_input");

        let mut current_ao_drift = fixture(LiveVariant::N0);
        current_ao_drift
            .input
            .current_ao
            .as_mut()
            .expect("current-AO binding")
            .provider_program_digest = digest_bytes(b"drifted current-AO provider");
        let error = execute_with_runners(
            &current_ao_drift.input,
            LiveVariant::N0,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::new(),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("current-AO binding drift");
        assert_eq!(error.code, "invalid_input");

        let mut exposed_authority = fixture(LiveVariant::N7);
        exposed_authority
            .input
            .request
            .authority
            .allowed_roots
            .push(exposed_authority.input.hidden_tests.clone());
        let error = execute_with_runners(
            &exposed_authority.input,
            LiveVariant::N7,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::new(),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("hidden authority exposure");
        assert_eq!(error.code, "invalid_input");
        assert!(error.message.contains("N7 model authority"));

        let leaked = fixture(LiveVariant::N4);
        let mut hidden_bytes = b"public-prefix\n".to_vec();
        hidden_bytes.extend_from_slice(
            &std::fs::read(leaked.input.hidden_tests.join("test_product.py"))
                .expect("hidden bytes"),
        );
        let output = execute_with_runners(
            &leaked.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(None)]),
                direct_write: Some(leaked.product.clone()),
                additional_write: Some((
                    leaked.input.request.workspace.root.join("copied-hidden.py"),
                    hidden_bytes,
                )),
            },
            BoundedProcessRunner,
        )
        .expect("leak record");
        assert_ne!(output.status, 0);
        assert_eq!(output.value["measurement"]["hidden_test_exposure"], true);
        assert_eq!(output.value["measurement"]["task_success"], false);
    }

    #[test]
    fn n7_rejects_a_second_provider_process_before_spawn() {
        let n7 = fixture(LiveVariant::N7);
        let turn = AdapterTurn {
            actions: Vec::new(),
            usage: TokenUsage::default(),
            model_claimed_success: false,
            control_mutations: Vec::new(),
        };
        let output = execute_with_runners(
            &n7.input,
            LiveVariant::N7,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([codex_output(Some(&turn))]),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect("bounded N7 row");
        assert_eq!(output.status, 4);
        assert_eq!(output.value["measurement"]["worker_turns"], 1);
        assert_eq!(output.value["measurement"]["task_success"], false);
    }

    #[test]
    fn malformed_provider_output_is_retained_before_the_run_stops() {
        let malformed = fixture(LiveVariant::N4);
        let error = execute_with_runners(
            &malformed.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([InvocationOutput {
                    status: 0,
                    stdout: b"{malformed-provider-output\n".to_vec(),
                    stderr: b"provider diagnostic".to_vec(),
                }]),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("malformed provider output");
        assert_eq!(error.code, "runtime_failure");
        assert_eq!(
            std::fs::read(malformed.input.raw_capture_root.join("capture-000.stdout"))
                .expect("retained stdout"),
            b"{malformed-provider-output\n"
        );
        assert!(
            malformed
                .input
                .raw_capture_root
                .join("capture-index.json")
                .is_file()
        );
    }

    #[test]
    fn nonzero_provider_status_is_retained_before_classification() {
        let failed = fixture(LiveVariant::N4);
        let provider_output = InvocationOutput {
            status: 7,
            stdout: b"provider partial output\n".to_vec(),
            stderr: b"provider failed\n".to_vec(),
        };
        let error = execute_with_runners(
            &failed.input,
            LiveVariant::N4,
            MeasurementOrigin::OfflineFixture,
            FakeProvider {
                outputs: VecDeque::from([provider_output.clone()]),
                direct_write: None,
                additional_write: None,
            },
            BoundedProcessRunner,
        )
        .expect_err("nonzero provider status remains a provider failure");
        assert_eq!(error.code, "runtime_failure");
        assert_eq!(
            std::fs::read(failed.input.raw_capture_root.join("capture-000.stdout"))
                .expect("retained provider stdout"),
            provider_output.stdout
        );
        assert_eq!(
            std::fs::read(failed.input.raw_capture_root.join("capture-000.stderr"))
                .expect("retained provider stderr"),
            provider_output.stderr
        );
        let terminal: serde_json::Value = serde_json::from_slice(
            &std::fs::read(failed.input.raw_capture_root.join("capture-terminal.json"))
                .expect("capture terminal metadata"),
        )
        .expect("capture terminal JSON");
        assert_eq!(terminal["failure_stage"], "provider");
    }
}

#[cfg(all(test, windows))]
mod windows_tests {
    use super::*;
    use tempfile::TempDir;

    #[test]
    fn windows_git_workspace_preparation_is_seed_bound_and_clean() {
        let temporary = TempDir::new().expect("temporary");
        let workspace = temporary.path().join("workspace");
        std::fs::create_dir(&workspace).expect("workspace");
        std::fs::write(workspace.join("source.txt"), b"source\n").expect("source");
        let identity = prepare_git_workspace(
            &workspace,
            std::slice::from_ref(&workspace),
            &digest_bytes(b"windows-seed"),
        )
        .expect("Windows Git workspace");
        verify_git_workspace(&identity, true).expect("stable Windows Git workspace");
    }

    #[test]
    fn windows_fake_program_is_digest_bound_and_locked_while_it_runs() {
        let temporary = TempDir::new().expect("temporary");
        let program = temporary.path().join("fake-provider.exe");
        std::fs::copy(std::env::current_exe().expect("test executable"), &program)
            .expect("fake executable");
        let digest = digest_regular_file(&program).expect("fake digest");
        let calls = Arc::new(Mutex::new(0));
        let mut runner =
            DigestBoundFakeRunner::new(&program, &digest, calls.clone()).expect("bound fake");
        assert!(
            std::fs::write(&program, b"drift").is_err(),
            "bound executable must reject in-place drift"
        );
        let output = runner
            .run(
                &PreparedInvocation {
                    program: "codex".into(),
                    args: vec!["--list".into()],
                    stdin: Vec::new(),
                    cwd: temporary.path().to_path_buf(),
                    environment: None,
                    limits: InvocationLimits {
                        max_input_bytes: 0,
                        max_output_bytes: 1024 * 1024,
                        timeout_ms: 10_000,
                    },
                },
                &CancellationToken::new(),
            )
            .expect("bound Windows executable runs");
        assert_eq!(output.status, 0);
        assert_eq!(*calls.lock().expect("fake call count"), 1);
    }
}
