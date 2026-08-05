use std::collections::BTreeMap;
use std::path::{Component, Path, PathBuf};

use chrono::{DateTime, Utc};
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::contracts::{
    AuthorityEnvelope, Digest, EffectKind, EffectRequest, StructuredCommand, VerifierProfile,
    VerifierReport, VerifierResult,
};
use crate::effects::{EffectBrokerError, LocalEffectBroker};
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
