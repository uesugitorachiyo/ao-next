use std::collections::{BTreeMap, BTreeSet};
use std::path::{Component, Path, PathBuf};

use chrono::{DateTime, Utc};
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::adapter::process::ProcessRunner;
use crate::adapter::{CancellationToken, InvocationLimits, InvocationOutput, PreparedInvocation};
use crate::contracts::{
    AuthorityEnvelope, Digest, EffectKind, EffectRequest, StructuredCommand, VerifierProfile,
    VerifierReport, VerifierResult,
};
use crate::effects::{EffectBrokerError, LocalEffectBroker};
use crate::engine::{EngineVerifier, VerificationOutcome};
use crate::evidence::{EvidenceError, digest_bytes, digest_file, read_regular_file};
use crate::strict_json::{StrictJsonError, canonical_digest, decode_strict_json};

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct VerificationPlan {
    pub commands: Vec<StructuredCommand>,
    pub required_files: Vec<PathBuf>,
    pub strict_json_files: Vec<PathBuf>,
    pub digest_expectations: BTreeMap<PathBuf, Digest>,
    pub max_file_bytes: u64,
}

#[derive(Clone, Debug, Default)]
pub struct VerifierRegistry {
    plans: BTreeMap<String, VerificationPlan>,
}

impl VerifierRegistry {
    #[must_use]
    pub const fn new() -> Self {
        Self {
            plans: BTreeMap::new(),
        }
    }

    /// Registers a deterministic verifier plan and returns its canonical digest.
    ///
    /// # Errors
    ///
    /// Returns [`VerificationError`] when canonical plan serialization fails.
    pub fn register(
        &mut self,
        profile_id: impl Into<String>,
        plan: VerificationPlan,
    ) -> Result<Digest, VerificationError> {
        let digest = canonical_digest(&plan)?;
        self.plans.insert(profile_id.into(), plan);
        Ok(digest)
    }

    fn get(&self, profile_id: &str) -> Option<&VerificationPlan> {
        self.plans.get(profile_id)
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct VerifiedWorkspace {
    root: PathBuf,
}

impl VerifiedWorkspace {
    /// Binds an existing non-symlink directory to one of the allowed roots.
    ///
    /// # Errors
    ///
    /// Returns [`VerificationError`] when the path is a symlink, is not a
    /// directory, is outside every allowed root, or cannot be canonicalized.
    pub fn new(root: &Path, allowed_roots: &[PathBuf]) -> Result<Self, VerificationError> {
        let metadata = std::fs::symlink_metadata(root)
            .map_err(|error| VerificationError::Io(error.to_string()))?;
        if metadata.file_type().is_symlink() {
            return Err(VerificationError::UnsafeWorkspace(root.to_path_buf()));
        }
        if !metadata.is_dir() {
            return Err(VerificationError::UnsafeWorkspace(root.to_path_buf()));
        }
        let canonical = std::fs::canonicalize(root)
            .map_err(|error| VerificationError::Io(error.to_string()))?;
        let mut allowed = false;
        for allowed_root in allowed_roots {
            let allowed_metadata = std::fs::symlink_metadata(allowed_root)
                .map_err(|error| VerificationError::Io(error.to_string()))?;
            if allowed_metadata.file_type().is_symlink() {
                return Err(VerificationError::UnsafeWorkspace(allowed_root.clone()));
            }
            let canonical_allowed = std::fs::canonicalize(allowed_root)
                .map_err(|error| VerificationError::Io(error.to_string()))?;
            if canonical.starts_with(canonical_allowed) {
                allowed = true;
                break;
            }
        }
        if !allowed {
            return Err(VerificationError::UnsafeWorkspace(root.to_path_buf()));
        }
        Ok(Self { root: canonical })
    }

    #[must_use]
    pub fn root(&self) -> &Path {
        &self.root
    }
}

#[derive(Debug, Error)]
pub enum VerificationError {
    #[error("unknown verifier profile: {0}")]
    UnknownProfile(String),
    #[error("verifier profile digest mismatch")]
    ProfileDigestMismatch,
    #[error("verifier profile command or file contract mismatch")]
    ProfileContractMismatch,
    #[error("unsafe workspace or verifier path: {0}")]
    UnsafeWorkspace(PathBuf),
    #[error("verifier I/O failed: {0}")]
    Io(String),
    #[error("effect broker failed: {0}")]
    Effect(String),
    #[error("strict JSON failed: {0}")]
    StrictJson(#[from] StrictJsonError),
    #[error("evidence helper failed: {0}")]
    Evidence(String),
}

impl From<EffectBrokerError> for VerificationError {
    fn from(error: EffectBrokerError) -> Self {
        Self::Effect(error.to_string())
    }
}

impl From<EvidenceError> for VerificationError {
    fn from(error: EvidenceError) -> Self {
        Self::Evidence(error.to_string())
    }
}

pub trait ProductVerifier {
    /// Runs deterministic mechanical and product checks for one bound profile.
    ///
    /// # Errors
    ///
    /// Returns [`VerificationError`] when the profile or workspace is unsafe or
    /// a verifier cannot execute. Ordinary check failures are retained in a
    /// non-passing [`VerifierReport`].
    fn verify(
        &self,
        workspace: &VerifiedWorkspace,
        profile: &VerifierProfile,
    ) -> Result<VerifierReport, VerificationError>;
}

pub struct LocalProductVerifier<'a> {
    run_id: &'a str,
    authority: &'a AuthorityEnvelope,
    broker: &'a LocalEffectBroker,
    registry: &'a VerifierRegistry,
    recorded_at: DateTime<Utc>,
}

impl<'a> LocalProductVerifier<'a> {
    #[must_use]
    pub const fn new(
        run_id: &'a str,
        authority: &'a AuthorityEnvelope,
        broker: &'a LocalEffectBroker,
        registry: &'a VerifierRegistry,
        recorded_at: DateTime<Utc>,
    ) -> Self {
        Self {
            run_id,
            authority,
            broker,
            registry,
            recorded_at,
        }
    }
}

impl ProductVerifier for LocalProductVerifier<'_> {
    fn verify(
        &self,
        workspace: &VerifiedWorkspace,
        profile: &VerifierProfile,
    ) -> Result<VerifierReport, VerificationError> {
        let plan = self
            .registry
            .get(&profile.profile_id)
            .ok_or_else(|| VerificationError::UnknownProfile(profile.profile_id.clone()))?;
        if canonical_digest(plan)? != profile.profile_digest {
            return Err(VerificationError::ProfileDigestMismatch);
        }
        if plan.commands != profile.commands || plan.required_files != profile.required_artifacts {
            return Err(VerificationError::ProfileContractMismatch);
        }

        let mut results = Vec::new();
        for (index, command) in plan.commands.iter().enumerate() {
            results.push(self.verify_command(workspace, command, index)?);
        }
        for path in &plan.required_files {
            results.push(verify_required_file(workspace, path, plan.max_file_bytes));
        }
        for path in &plan.strict_json_files {
            results.push(verify_strict_json_file(
                workspace,
                path,
                plan.max_file_bytes,
            ));
        }
        for (path, expected) in &plan.digest_expectations {
            results.push(verify_digest(
                workspace,
                path,
                expected,
                plan.max_file_bytes,
            ));
        }
        let passed = results.iter().all(|result| result.passed);
        Ok(VerifierReport {
            schema_version: "ao.next.verifier-report.v1".into(),
            run_id: self.run_id.to_owned(),
            verifier_profile_digest: profile.profile_digest.clone(),
            started_at: self.recorded_at,
            completed_at: self.recorded_at,
            passed,
            results,
        })
    }
}

impl LocalProductVerifier<'_> {
    fn verify_command(
        &self,
        workspace: &VerifiedWorkspace,
        command: &StructuredCommand,
        index: usize,
    ) -> Result<VerifierResult, VerificationError> {
        let request = EffectRequest {
            effect_id: format!("verifier-command-{index}"),
            run_id: self.run_id.to_owned(),
            kind: EffectKind::RunProgram,
            program: Some(command.program.clone()),
            args: command.args.clone(),
            paths: vec![workspace.root.clone()],
            timeout_ms: command.timeout_ms,
            input_digest: canonical_digest(command)?,
        };
        let output = self.broker.execute(&request, self.authority)?;
        let mut combined = output.stdout;
        combined.extend_from_slice(&output.stderr);
        Ok(VerifierResult {
            verifier_id: format!("command:{index}:{}", command.program),
            passed: output.status == 0,
            exit_status: Some(output.status),
            output_digest: digest_bytes(&combined),
            message: if output.status == 0 {
                "structured command passed".into()
            } else {
                format!("structured command exited {}", output.status)
            },
        })
    }
}

fn verify_required_file(
    workspace: &VerifiedWorkspace,
    relative: &Path,
    maximum_bytes: u64,
) -> VerifierResult {
    let verifier_id = format!("file:{}", relative.display());
    match resolve_file(workspace, relative)
        .and_then(|path| digest_file(&path, maximum_bytes).map_err(VerificationError::from))
    {
        Ok(output_digest) => VerifierResult {
            verifier_id,
            passed: true,
            exit_status: None,
            output_digest,
            message: "required file is present and regular".into(),
        },
        Err(error) => failed_result(verifier_id, &error.to_string()),
    }
}

fn verify_strict_json_file(
    workspace: &VerifiedWorkspace,
    relative: &Path,
    maximum_bytes: u64,
) -> VerifierResult {
    let verifier_id = format!("json:{}", relative.display());
    let result = resolve_file(workspace, relative).and_then(|path| {
        let bytes = read_regular_file(&path, maximum_bytes)?;
        let maximum_bytes = usize::try_from(maximum_bytes).unwrap_or(usize::MAX);
        decode_strict_json::<serde_json::Value>(&bytes, maximum_bytes)?;
        Ok(digest_bytes(&bytes))
    });
    match result {
        Ok(output_digest) => VerifierResult {
            verifier_id,
            passed: true,
            exit_status: None,
            output_digest,
            message: "strict JSON is valid".into(),
        },
        Err(error) => failed_result(verifier_id, &error.to_string()),
    }
}

fn verify_digest(
    workspace: &VerifiedWorkspace,
    relative: &Path,
    expected: &Digest,
    maximum_bytes: u64,
) -> VerifierResult {
    let verifier_id = format!("digest:{}", relative.display());
    match resolve_file(workspace, relative)
        .and_then(|path| digest_file(&path, maximum_bytes).map_err(VerificationError::from))
    {
        Ok(observed) if &observed == expected => VerifierResult {
            verifier_id,
            passed: true,
            exit_status: None,
            output_digest: observed,
            message: "file digest matches".into(),
        },
        Ok(observed) => VerifierResult {
            verifier_id,
            passed: false,
            exit_status: None,
            output_digest: observed,
            message: format!("file digest does not match {expected}"),
        },
        Err(error) => failed_result(verifier_id, &error.to_string()),
    }
}

fn failed_result(verifier_id: String, message: &str) -> VerifierResult {
    VerifierResult {
        verifier_id,
        passed: false,
        exit_status: None,
        output_digest: digest_bytes(message.as_bytes()),
        message: message.to_owned(),
    }
}

fn resolve_file(
    workspace: &VerifiedWorkspace,
    relative: &Path,
) -> Result<PathBuf, VerificationError> {
    if relative.is_absolute()
        || relative
            .components()
            .any(|component| !matches!(component, Component::Normal(_)))
    {
        return Err(VerificationError::UnsafeWorkspace(relative.to_path_buf()));
    }
    let path = workspace.root.join(relative);
    let metadata = std::fs::symlink_metadata(&path)
        .map_err(|error| VerificationError::Io(error.to_string()))?;
    if metadata.file_type().is_symlink() || !metadata.is_file() {
        return Err(VerificationError::UnsafeWorkspace(path));
    }
    let canonical =
        std::fs::canonicalize(&path).map_err(|error| VerificationError::Io(error.to_string()))?;
    if !canonical.starts_with(&workspace.root) {
        return Err(VerificationError::UnsafeWorkspace(canonical));
    }
    Ok(canonical)
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct RequiredArtifactExpectation {
    pub path: PathBuf,
    pub digest: Digest,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct CommandVerifierEntry {
    pub verifier_id: String,
    pub verifier_digest: Digest,
    pub program: String,
    pub args: Vec<String>,
    pub working_directory: PathBuf,
    pub timeout_ms: u64,
    pub max_output_bytes: usize,
    pub expected_exit_status: i32,
    pub required_artifacts: Vec<RequiredArtifactExpectation>,
}

impl CommandVerifierEntry {
    /// Calculates the digest of every immutable entry field except the digest
    /// field itself.
    ///
    /// # Errors
    ///
    /// Returns a verification error when canonical serialization fails.
    pub fn calculated_digest(&self) -> Result<Digest, VerificationError> {
        #[derive(Serialize)]
        struct Material<'a> {
            verifier_id: &'a str,
            program: &'a str,
            args: &'a [String],
            working_directory: &'a Path,
            timeout_ms: u64,
            max_output_bytes: usize,
            expected_exit_status: i32,
            required_artifacts: &'a [RequiredArtifactExpectation],
        }
        Ok(canonical_digest(&Material {
            verifier_id: &self.verifier_id,
            program: &self.program,
            args: &self.args,
            working_directory: &self.working_directory,
            timeout_ms: self.timeout_ms,
            max_output_bytes: self.max_output_bytes,
            expected_exit_status: self.expected_exit_status,
            required_artifacts: &self.required_artifacts,
        })?)
    }
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct CommandVerifierProfile {
    pub schema_version: String,
    pub profile_id: String,
    pub profile_digest: Digest,
    pub entries: Vec<CommandVerifierEntry>,
}

impl CommandVerifierProfile {
    /// Calculates the digest of the profile identity and ordered entries.
    ///
    /// # Errors
    ///
    /// Returns a verification error when canonical serialization fails.
    pub fn calculated_digest(&self) -> Result<Digest, VerificationError> {
        #[derive(Serialize)]
        struct Material<'a> {
            schema_version: &'a str,
            profile_id: &'a str,
            entries: &'a [CommandVerifierEntry],
        }
        Ok(canonical_digest(&Material {
            schema_version: &self.schema_version,
            profile_id: &self.profile_id,
            entries: &self.entries,
        })?)
    }

    fn validate_for(
        &self,
        request: &crate::contracts::RunRequest,
    ) -> Result<(), VerificationError> {
        if self.schema_version != "ao.next.command-verifier-profile.v1"
            || self.profile_id.trim().is_empty()
            || self.entries.is_empty()
            || self.calculated_digest()? != self.profile_digest
        {
            return Err(VerificationError::ProfileDigestMismatch);
        }
        let mut identifiers = BTreeSet::new();
        for entry in &self.entries {
            if entry.verifier_id.trim().is_empty()
                || !identifiers.insert(&entry.verifier_id)
                || entry.program.trim().is_empty()
                || entry.timeout_ms == 0
                || entry.timeout_ms > request.limits.max_effect_timeout_ms
                || entry.max_output_bytes == 0
                || entry.max_output_bytes
                    > usize::try_from(request.limits.max_output_bytes).unwrap_or(usize::MAX)
                || entry.calculated_digest()? != entry.verifier_digest
                || !is_safe_relative(&entry.working_directory, true)
                || entry
                    .required_artifacts
                    .iter()
                    .any(|artifact| !is_safe_relative(&artifact.path, false))
            {
                return Err(VerificationError::ProfileContractMismatch);
            }
        }
        let commands = self
            .entries
            .iter()
            .map(|entry| StructuredCommand {
                program: entry.program.clone(),
                args: entry.args.clone(),
                timeout_ms: entry.timeout_ms,
            })
            .collect::<Vec<_>>();
        let required_artifacts = self
            .entries
            .iter()
            .flat_map(|entry| {
                entry
                    .required_artifacts
                    .iter()
                    .map(|artifact| artifact.path.clone())
            })
            .collect::<Vec<_>>();
        if request.verifier_profile.profile_id != self.profile_id
            || request.verifier_profile.profile_digest != self.profile_digest
            || request.verifier_profile.commands != commands
            || request.verifier_profile.required_artifacts != required_artifacts
            || !request
                .authority
                .capabilities
                .contains(&crate::contracts::Capability::RunLocalProgram)
            || self
                .entries
                .iter()
                .any(|entry| !request.authority.allowed_programs.contains(&entry.program))
        {
            return Err(VerificationError::ProfileContractMismatch);
        }
        Ok(())
    }
}

pub struct CommandEngineVerifier<R> {
    request_digest: Digest,
    workspace: VerifiedWorkspace,
    profile: CommandVerifierProfile,
    runner: R,
    cancellation: CancellationToken,
    recorded_at: DateTime<Utc>,
    max_artifact_bytes: u64,
    reports: Vec<VerifierReport>,
}

impl<R> CommandEngineVerifier<R> {
    /// Creates a verifier whose request and profile cannot change between
    /// construction and execution.
    ///
    /// # Errors
    ///
    /// Returns a verification error for unsafe paths, identity drift, invalid
    /// digests, or authority/profile disagreement.
    pub fn new(
        request: &crate::contracts::RunRequest,
        profile: CommandVerifierProfile,
        runner: R,
        cancellation: CancellationToken,
        recorded_at: DateTime<Utc>,
    ) -> Result<Self, VerificationError> {
        profile.validate_for(request)?;
        let workspace =
            VerifiedWorkspace::new(&request.workspace.root, &request.authority.allowed_roots)?;
        Ok(Self {
            request_digest: canonical_digest(request)?,
            workspace,
            profile,
            runner,
            cancellation,
            recorded_at,
            max_artifact_bytes: request.limits.max_input_bytes,
            reports: Vec::new(),
        })
    }

    #[must_use]
    pub fn reports(&self) -> &[VerifierReport] {
        &self.reports
    }

    #[must_use]
    pub const fn runner(&self) -> &R {
        &self.runner
    }
}

impl<R: ProcessRunner> EngineVerifier for CommandEngineVerifier<R> {
    fn verify(&mut self, request: &crate::contracts::RunRequest) -> VerificationOutcome {
        if canonical_digest(request).ok().as_ref() != Some(&self.request_digest) {
            return failed_verification_outcome("verifier request identity drifted");
        }
        let mut results = Vec::new();
        for entry in &self.profile.entries {
            results.push(run_command_entry(
                &mut self.runner,
                &self.cancellation,
                &self.workspace,
                entry,
            ));
            for artifact in &entry.required_artifacts {
                let mut result = verify_digest(
                    &self.workspace,
                    &artifact.path,
                    &artifact.digest,
                    self.max_artifact_bytes,
                );
                result.verifier_id =
                    format!("{}:artifact:{}", entry.verifier_id, artifact.path.display());
                results.push(result);
            }
        }
        let passed = results.iter().all(|result| result.passed);
        let report = VerifierReport {
            schema_version: "ao.next.verifier-report.v1".into(),
            run_id: request.run_id.clone(),
            verifier_profile_digest: self.profile.profile_digest.clone(),
            started_at: self.recorded_at,
            completed_at: self.recorded_at,
            passed,
            results,
        };
        let report_digest = canonical_digest(&report)
            .unwrap_or_else(|_| digest_bytes(b"command verifier report digest failure"));
        self.reports.push(report);
        VerificationOutcome {
            passed,
            report_digest,
            summary: if passed {
                "deterministic command verifier passed".into()
            } else {
                "deterministic command verifier failed".into()
            },
        }
    }
}

fn run_command_entry(
    runner: &mut impl ProcessRunner,
    cancellation: &CancellationToken,
    workspace: &VerifiedWorkspace,
    entry: &CommandVerifierEntry,
) -> VerifierResult {
    if cancellation.is_cancelled() {
        return failed_result(
            format!("command:{}", entry.verifier_id),
            "verifier invocation was cancelled",
        );
    }
    let cwd = match resolve_directory(workspace, &entry.working_directory) {
        Ok(path) => path,
        Err(error) => {
            return failed_result(format!("command:{}", entry.verifier_id), &error.to_string());
        }
    };
    let invocation = PreparedInvocation {
        program: entry.program.clone(),
        args: entry.args.clone(),
        stdin: Vec::new(),
        cwd,
        limits: InvocationLimits {
            max_input_bytes: 0,
            max_output_bytes: entry.max_output_bytes,
            timeout_ms: entry.timeout_ms,
        },
    };
    match runner.run(&invocation, cancellation) {
        Ok(output) => command_result(entry, &output),
        Err(error) => failed_result(format!("command:{}", entry.verifier_id), &error.to_string()),
    }
}

fn command_result(entry: &CommandVerifierEntry, output: &InvocationOutput) -> VerifierResult {
    let within_bound =
        output.stdout.len().saturating_add(output.stderr.len()) <= entry.max_output_bytes;
    let output_digest = canonical_digest(&(output.status, &output.stdout, &output.stderr))
        .unwrap_or_else(|_| digest_bytes(b"command verifier output digest failure"));
    let passed = within_bound && output.status == entry.expected_exit_status;
    VerifierResult {
        verifier_id: format!("command:{}", entry.verifier_id),
        passed,
        exit_status: Some(output.status),
        output_digest,
        message: if !within_bound {
            "structured command output exceeded its bound".into()
        } else if passed {
            "structured command matched expected status".into()
        } else {
            format!(
                "structured command exited {}; expected {}",
                output.status, entry.expected_exit_status
            )
        },
    }
}

fn failed_verification_outcome(message: &str) -> VerificationOutcome {
    VerificationOutcome {
        passed: false,
        report_digest: digest_bytes(message.as_bytes()),
        summary: message.into(),
    }
}

fn resolve_directory(
    workspace: &VerifiedWorkspace,
    relative: &Path,
) -> Result<PathBuf, VerificationError> {
    if !is_safe_relative(relative, true) {
        return Err(VerificationError::UnsafeWorkspace(relative.to_path_buf()));
    }
    let path = workspace.root.join(relative);
    let metadata = std::fs::symlink_metadata(&path)
        .map_err(|error| VerificationError::Io(error.to_string()))?;
    if metadata.file_type().is_symlink() || !metadata.is_dir() {
        return Err(VerificationError::UnsafeWorkspace(path));
    }
    let canonical =
        std::fs::canonicalize(&path).map_err(|error| VerificationError::Io(error.to_string()))?;
    if !canonical.starts_with(&workspace.root) {
        return Err(VerificationError::UnsafeWorkspace(canonical));
    }
    Ok(canonical)
}

fn is_safe_relative(path: &Path, allow_empty: bool) -> bool {
    !path.is_absolute()
        && (allow_empty || path.components().next().is_some())
        && path
            .components()
            .all(|component| matches!(component, Component::Normal(_)))
}
