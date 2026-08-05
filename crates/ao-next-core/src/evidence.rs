use std::collections::{BTreeMap, BTreeSet};
use std::fs::{File, OpenOptions};
use std::io::{Read, Write};
use std::path::{Component, Path, PathBuf};

use chrono::{DateTime, Utc};
use sha2::{Digest as _, Sha256};
use thiserror::Error;

use crate::adapter::AdapterIdentity;
use crate::contracts::{
    ArtifactEntry, ArtifactManifest, Digest, RunRequest, RunState, TerminalReadback, VerifierReport,
};
use crate::strict_json::{StrictJsonError, canonical_digest, canonical_json_bytes};

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct StoreLimits {
    pub max_artifact_bytes: u64,
    pub max_total_bytes: u64,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ArtifactSpec {
    pub artifact_id: String,
    pub path: PathBuf,
    pub original_ref: String,
    pub media_type: String,
    pub producer: String,
    pub input_digests: Vec<Digest>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SealedRun {
    pub manifest: ArtifactManifest,
    pub readback: TerminalReadback,
}

#[derive(Debug, Error)]
pub enum EvidenceError {
    #[error("evidence I/O failed: {0}")]
    Io(String),
    #[error("path is outside allowed roots: {0}")]
    PathOutsideAllowedRoots(PathBuf),
    #[error("parent traversal is not allowed: {0}")]
    ParentTraversal(PathBuf),
    #[error("symlinks are not allowed: {0}")]
    SymlinkNotAllowed(PathBuf),
    #[error("path is not a regular file: {0}")]
    NonRegularFile(PathBuf),
    #[error("artifact is oversized: {actual} bytes exceeds {limit}")]
    ArtifactOversized { actual: u64, limit: u64 },
    #[error("evidence total is oversized: {actual} bytes exceeds {limit}")]
    TotalOversized { actual: u64, limit: u64 },
    #[error("digest mismatch at {path}: expected {expected}, observed {observed}")]
    DigestMismatch {
        path: PathBuf,
        expected: Digest,
        observed: Digest,
    },
    #[error("content reference does not match its digest: {0}")]
    InvalidContentRef(String),
    #[error("duplicate artifact identity or content reference: {0}")]
    DuplicateArtifact(String),
    #[error("verifier report did not pass")]
    VerificationNotPassed,
    #[error("verifier report identity mismatch")]
    VerifierIdentityMismatch,
    #[error("sealed terminal evidence is semantically contradictory")]
    TerminalContradiction,
    #[error("strict JSON failure: {0}")]
    StrictJson(#[from] StrictJsonError),
}

impl From<std::io::Error> for EvidenceError {
    fn from(error: std::io::Error) -> Self {
        Self::Io(error.to_string())
    }
}

#[derive(Clone, Debug)]
pub struct ArtifactStore {
    root: PathBuf,
    allowed_source_roots: Vec<PathBuf>,
    limits: StoreLimits,
}

impl ArtifactStore {
    /// Creates or opens a digest-addressed evidence store.
    ///
    /// # Errors
    ///
    /// Returns [`EvidenceError`] when the root is a symlink, is not a directory,
    /// or its artifact directory cannot be created.
    pub fn new(
        root: impl AsRef<Path>,
        allowed_source_roots: Vec<PathBuf>,
        limits: StoreLimits,
    ) -> Result<Self, EvidenceError> {
        let root = root.as_ref().to_path_buf();
        std::fs::create_dir_all(&root)?;
        let metadata = std::fs::symlink_metadata(&root)?;
        if metadata.file_type().is_symlink() {
            return Err(EvidenceError::SymlinkNotAllowed(root));
        }
        if !metadata.is_dir() {
            return Err(EvidenceError::NonRegularFile(root));
        }
        std::fs::create_dir_all(root.join("artifacts/sha256"))?;
        Ok(Self {
            root,
            allowed_source_roots,
            limits,
        })
    }

    /// Retains one bounded regular file at its exact content digest.
    ///
    /// # Errors
    ///
    /// Returns [`EvidenceError`] for unsafe paths, non-regular inputs, size
    /// violations, I/O failures, or conflicting retained bytes.
    pub fn retain(&self, spec: &ArtifactSpec) -> Result<ArtifactEntry, EvidenceError> {
        validate_source_file(&spec.path, &self.allowed_source_roots)?;
        let bytes = read_regular_file(&spec.path, self.limits.max_artifact_bytes)?;
        let digest = digest_bytes(&bytes);
        let hex = &digest.as_str()[7..];
        let content_ref = format!("artifacts/sha256/{hex}");
        let target = self.root.join(&content_ref);

        let existing_total = retained_total(&self.root.join("artifacts/sha256"))?;
        let target_exists = target.exists();
        let proposed_total = if target_exists {
            existing_total
        } else {
            existing_total.saturating_add(bytes.len() as u64)
        };
        if proposed_total > self.limits.max_total_bytes {
            return Err(EvidenceError::TotalOversized {
                actual: proposed_total,
                limit: self.limits.max_total_bytes,
            });
        }

        if target_exists {
            let observed = digest_file(&target, self.limits.max_artifact_bytes)?;
            if observed != digest {
                return Err(EvidenceError::DigestMismatch {
                    path: target,
                    expected: digest,
                    observed,
                });
            }
        } else {
            match OpenOptions::new()
                .write(true)
                .create_new(true)
                .open(&target)
            {
                Ok(mut file) => {
                    file.write_all(&bytes)?;
                    file.sync_all()?;
                }
                Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => {
                    let observed = digest_file(&target, self.limits.max_artifact_bytes)?;
                    if observed != digest {
                        return Err(EvidenceError::DigestMismatch {
                            path: target,
                            expected: digest,
                            observed,
                        });
                    }
                }
                Err(error) => return Err(error.into()),
            }
        }

        Ok(ArtifactEntry {
            artifact_id: spec.artifact_id.clone(),
            media_type: spec.media_type.clone(),
            digest,
            content_ref,
            original_ref: spec.original_ref.clone(),
            size_bytes: bytes.len() as u64,
            producer: spec.producer.clone(),
            input_digests: spec.input_digests.clone(),
        })
    }

    fn write_named_json<T>(&self, name: &str, value: &T) -> Result<(), EvidenceError>
    where
        T: serde::Serialize,
    {
        if name.contains('/') || name.contains('\\') || name == "." || name == ".." {
            return Err(EvidenceError::InvalidContentRef(name.to_owned()));
        }
        let bytes = canonical_json_bytes(value)?;
        let temporary = self.root.join(format!(".{name}.tmp"));
        let target = self.root.join(name);
        {
            let mut file = File::create(&temporary)?;
            file.write_all(&bytes)?;
            file.sync_all()?;
        }
        std::fs::rename(temporary, target)?;
        Ok(())
    }
}

#[must_use]
pub fn digest_bytes(bytes: &[u8]) -> Digest {
    let hex = format!("{:x}", Sha256::digest(bytes));
    Digest::from_sha256_hex(&hex)
}

/// Calculates a bounded regular file's SHA-256 digest.
///
/// # Errors
///
/// Returns [`EvidenceError`] when the path is a symlink or non-regular file,
/// exceeds `maximum_bytes`, or cannot be read.
pub fn digest_file(path: &Path, maximum_bytes: u64) -> Result<Digest, EvidenceError> {
    read_regular_file(path, maximum_bytes).map(|bytes| digest_bytes(&bytes))
}

pub(crate) fn read_regular_file(path: &Path, maximum_bytes: u64) -> Result<Vec<u8>, EvidenceError> {
    let metadata = std::fs::symlink_metadata(path)?;
    if metadata.file_type().is_symlink() {
        return Err(EvidenceError::SymlinkNotAllowed(path.to_path_buf()));
    }
    if !metadata.is_file() {
        return Err(EvidenceError::NonRegularFile(path.to_path_buf()));
    }
    if metadata.len() > maximum_bytes {
        return Err(EvidenceError::ArtifactOversized {
            actual: metadata.len(),
            limit: maximum_bytes,
        });
    }
    let file = File::open(path)?;
    let mut bytes = Vec::new();
    file.take(maximum_bytes.saturating_add(1))
        .read_to_end(&mut bytes)?;
    if bytes.len() as u64 > maximum_bytes {
        return Err(EvidenceError::ArtifactOversized {
            actual: bytes.len() as u64,
            limit: maximum_bytes,
        });
    }
    Ok(bytes)
}

/// Independently verifies every retained artifact in a manifest.
///
/// # Errors
///
/// Returns [`EvidenceError`] for duplicate identities, unsafe content refs,
/// non-regular retained files, size violations, or digest drift.
pub fn verify_evidence(
    root: &Path,
    manifest: &ArtifactManifest,
    maximum_total_bytes: u64,
) -> Result<(), EvidenceError> {
    let mut artifact_ids = BTreeSet::new();
    let mut content_refs = BTreeSet::new();
    let mut total = 0_u64;
    for entry in &manifest.entries {
        if !artifact_ids.insert(entry.artifact_id.clone()) {
            return Err(EvidenceError::DuplicateArtifact(entry.artifact_id.clone()));
        }
        if !content_refs.insert(entry.content_ref.clone()) {
            return Err(EvidenceError::DuplicateArtifact(entry.content_ref.clone()));
        }
        let expected_ref = format!("artifacts/sha256/{}", &entry.digest.as_str()[7..]);
        if entry.content_ref != expected_ref || !is_safe_relative(&entry.content_ref) {
            return Err(EvidenceError::InvalidContentRef(entry.content_ref.clone()));
        }
        let path = root.join(&entry.content_ref);
        let metadata = std::fs::symlink_metadata(&path)?;
        if metadata.file_type().is_symlink() {
            return Err(EvidenceError::SymlinkNotAllowed(path));
        }
        if !metadata.is_file() {
            return Err(EvidenceError::NonRegularFile(path));
        }
        if metadata.len() != entry.size_bytes {
            return Err(EvidenceError::ArtifactOversized {
                actual: metadata.len(),
                limit: entry.size_bytes,
            });
        }
        total = total.saturating_add(metadata.len());
        if total > maximum_total_bytes {
            return Err(EvidenceError::TotalOversized {
                actual: total,
                limit: maximum_total_bytes,
            });
        }
        let observed = digest_file(&path, entry.size_bytes)?;
        if observed != entry.digest {
            return Err(EvidenceError::DigestMismatch {
                path,
                expected: entry.digest.clone(),
                observed,
            });
        }
    }
    Ok(())
}

/// Seals a passed verifier report into retained artifacts and terminal readback.
///
/// # Errors
///
/// Returns [`EvidenceError`] when verification did not pass, identities differ,
/// any artifact cannot be retained, the manifest audit fails, or durable JSON
/// output cannot be written. No passed readback is written before all checks pass.
pub fn seal_verified_run(
    request: &RunRequest,
    adapter: &AdapterIdentity,
    report: &VerifierReport,
    store: &ArtifactStore,
    artifacts: &[ArtifactSpec],
    completed_at: DateTime<Utc>,
) -> Result<SealedRun, EvidenceError> {
    if !report.passed {
        return Err(EvidenceError::VerificationNotPassed);
    }
    if report.run_id != request.run_id
        || report.verifier_profile_digest != request.verifier_profile.profile_digest
    {
        return Err(EvidenceError::VerifierIdentityMismatch);
    }
    let entries = artifacts
        .iter()
        .map(|artifact| store.retain(artifact))
        .collect::<Result<Vec<_>, _>>()?;
    let manifest = ArtifactManifest {
        schema_version: "ao.next.artifact-manifest.v1".into(),
        run_id: request.run_id.clone(),
        source: request.source.clone(),
        entries,
    };
    verify_evidence(&store.root, &manifest, store.limits.max_total_bytes)?;

    let request_digest = canonical_digest(request)?;
    let verifier_report_digest = canonical_digest(report)?;
    let artifact_manifest_digest = canonical_digest(&manifest)?;
    let readback = TerminalReadback {
        schema_version: "ao.next.terminal-readback.v1".into(),
        run_id: request.run_id.clone(),
        source: request.source.clone(),
        workspace: request.workspace.clone(),
        adapter: adapter.clone(),
        request_digest,
        policy_digest: request.policy_digest.clone(),
        verifier_report_digest,
        artifact_manifest_digest,
        terminal_state: RunState::Passed,
        completed_at,
        safety_boundaries: BTreeMap::from([
            ("approves_work".into(), false),
            ("executes_mission_work".into(), false),
            ("grants_authority".into(), false),
            ("publishes".into(), false),
        ]),
        exact_next_action: "Await separately authorized live evaluation".into(),
    };
    store.write_named_json("artifact-manifest.json", &manifest)?;
    store.write_named_json("verifier-report.json", report)?;
    store.write_named_json("terminal-readback.json", &readback)?;
    Ok(SealedRun { manifest, readback })
}

/// Independently verifies a sealed manifest, verifier report, and terminal readback.
///
/// # Errors
///
/// Returns [`EvidenceError`] for unsafe or oversized files, strict decoding
/// failures, identity or digest drift, artifact-manifest failures, or terminal
/// contradictions.
pub fn verify_sealed_run(
    root: &Path,
    request: &RunRequest,
    maximum_bytes: u64,
) -> Result<(), EvidenceError> {
    let manifest_path = root.join("artifact-manifest.json");
    let verifier_path = root.join("verifier-report.json");
    let terminal_path = root.join("terminal-readback.json");
    let manifest_bytes = read_regular_file(&manifest_path, maximum_bytes)?;
    let verifier_bytes = read_regular_file(&verifier_path, maximum_bytes)?;
    let terminal_bytes = read_regular_file(&terminal_path, maximum_bytes)?;
    let total = (manifest_bytes.len() as u64)
        .saturating_add(verifier_bytes.len() as u64)
        .saturating_add(terminal_bytes.len() as u64);
    if total > maximum_bytes {
        return Err(EvidenceError::TotalOversized {
            actual: total,
            limit: maximum_bytes,
        });
    }

    let maximum_json_bytes = usize::try_from(maximum_bytes).unwrap_or(usize::MAX);
    let manifest: ArtifactManifest =
        crate::strict_json::decode_strict_json(&manifest_bytes, maximum_json_bytes)?;
    let report: VerifierReport =
        crate::strict_json::decode_strict_json(&verifier_bytes, maximum_json_bytes)?;
    let readback: TerminalReadback =
        crate::strict_json::decode_strict_json(&terminal_bytes, maximum_json_bytes)?;

    if manifest.schema_version != "ao.next.artifact-manifest.v1"
        || report.schema_version != "ao.next.verifier-report.v1"
        || readback.schema_version != "ao.next.terminal-readback.v1"
        || manifest.run_id != request.run_id
        || report.run_id != request.run_id
        || readback.run_id != request.run_id
        || manifest.source != request.source
        || readback.source != request.source
        || readback.workspace != request.workspace
        || report.verifier_profile_digest != request.verifier_profile.profile_digest
        || readback.policy_digest != request.policy_digest
    {
        return Err(EvidenceError::TerminalContradiction);
    }

    compare_digest(
        &terminal_path,
        &readback.request_digest,
        &canonical_digest(request)?,
    )?;
    compare_digest(
        &verifier_path,
        &readback.verifier_report_digest,
        &canonical_digest(&report)?,
    )?;
    compare_digest(
        &manifest_path,
        &readback.artifact_manifest_digest,
        &canonical_digest(&manifest)?,
    )?;
    if !report.passed
        || readback.terminal_state != RunState::Passed
        || readback.safety_boundaries.values().any(|value| *value)
        || readback.adapter.runtime != request.model_profile.runtime
        || readback.adapter.model_identifier != request.model_profile.model_identifier
        || readback.adapter.adapter_version != request.model_profile.adapter_version
    {
        return Err(EvidenceError::TerminalContradiction);
    }
    verify_evidence(root, &manifest, maximum_bytes)
}

fn compare_digest(path: &Path, expected: &Digest, observed: &Digest) -> Result<(), EvidenceError> {
    if expected != observed {
        return Err(EvidenceError::DigestMismatch {
            path: path.to_path_buf(),
            expected: expected.clone(),
            observed: observed.clone(),
        });
    }
    Ok(())
}

fn validate_source_file(path: &Path, allowed_roots: &[PathBuf]) -> Result<(), EvidenceError> {
    if path
        .components()
        .any(|component| component == Component::ParentDir)
    {
        return Err(EvidenceError::ParentTraversal(path.to_path_buf()));
    }
    let metadata = std::fs::symlink_metadata(path)?;
    if metadata.file_type().is_symlink() {
        return Err(EvidenceError::SymlinkNotAllowed(path.to_path_buf()));
    }
    if !metadata.is_file() {
        return Err(EvidenceError::NonRegularFile(path.to_path_buf()));
    }
    let canonical_path = std::fs::canonicalize(path)?;
    for root in allowed_roots {
        let root_metadata = std::fs::symlink_metadata(root)?;
        if root_metadata.file_type().is_symlink() {
            return Err(EvidenceError::SymlinkNotAllowed(root.clone()));
        }
        let canonical_root = std::fs::canonicalize(root)?;
        if canonical_path.starts_with(canonical_root) {
            return Ok(());
        }
    }
    Err(EvidenceError::PathOutsideAllowedRoots(path.to_path_buf()))
}

fn retained_total(directory: &Path) -> Result<u64, EvidenceError> {
    let mut total = 0_u64;
    for entry in std::fs::read_dir(directory)? {
        let path = entry?.path();
        let metadata = std::fs::symlink_metadata(&path)?;
        if metadata.file_type().is_symlink() {
            return Err(EvidenceError::SymlinkNotAllowed(path));
        }
        if !metadata.is_file() {
            return Err(EvidenceError::NonRegularFile(path));
        }
        total = total.saturating_add(metadata.len());
    }
    Ok(total)
}

fn is_safe_relative(value: &str) -> bool {
    let path = Path::new(value);
    !path.is_absolute()
        && path
            .components()
            .all(|component| matches!(component, Component::Normal(_)))
}
