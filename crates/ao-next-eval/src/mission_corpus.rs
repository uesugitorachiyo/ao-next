use std::collections::BTreeSet;
use std::fs;
use std::path::{Component, Path};
use std::process::Command;

use ao_next_core::contracts::Digest;
use ao_next_core::evidence::digest_bytes;
use ao_next_core::strict_json::{StrictJsonError, canonical_digest, decode_strict_json};
use serde::{Deserialize, Serialize};
use thiserror::Error;

const MANIFEST_LIMIT: usize = 1024 * 1024;
const FILE_LIMIT: u64 = 1024 * 1024;
const TOTAL_LIMIT: u64 = 32 * 1024 * 1024;
const ENTRY_LIMIT: usize = 4096;
const SOURCE_STATUS: &str = "durable_ao_mission_source_status";
const ENGINE_STATUS: &str = "future_read_only_engine_projection";

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
struct MissionCorpus {
    schema_version: String,
    source_repository: String,
    source_head: String,
    status_domains: StatusDomains,
    source_files: Vec<SourceFile>,
    vectors: Vec<BehaviorVector>,
    manifest_digest: Digest,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
struct StatusDomains {
    mission_durable_source: String,
    engine_projection: String,
    conflated: bool,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
struct SourceFile {
    path: String,
    mode: String,
    bytes: u64,
    digest: Digest,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, Ord, PartialEq, PartialOrd, Serialize)]
#[serde(rename_all = "snake_case")]
enum BehaviorSurface {
    Commands,
    StrictContracts,
    LifecycleTransitions,
    Readbacks,
    ArchiveImport,
    PublicSafety,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, Ord, PartialEq, PartialOrd, Serialize)]
#[serde(rename_all = "snake_case")]
enum ExpectedDisposition {
    Accepted,
    Rejected,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
struct BehaviorVector {
    id: String,
    surfaces: Vec<BehaviorSurface>,
    expected_disposition: ExpectedDisposition,
    source_path: String,
    fixture_path: String,
    bytes: u64,
    digest: Digest,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct VerifiedMissionCorpus {
    pub source_head: String,
    pub source_file_count: usize,
    pub vector_count: usize,
    pub manifest_digest: Digest,
}

#[derive(Debug, Error)]
pub enum MissionCorpusError {
    #[error("corpus path is unsafe: {0}")]
    UnsafePath(String),
    #[error("corpus input is not a regular non-symlink file or directory: {0}")]
    UnsafeFile(String),
    #[error("corpus input is oversized")]
    Oversized,
    #[error("corpus manifest is invalid: {0}")]
    Invalid(String),
    #[error("corpus digest drift: expected {expected}, observed {observed}")]
    DigestDrift { expected: Digest, observed: Digest },
    #[error("source head drift: expected {expected}, observed {observed}")]
    SourceHeadDrift { expected: String, observed: String },
    #[error("source inventory drift")]
    SourceInventoryDrift,
    #[error("corpus vector inventory drift")]
    VectorInventoryDrift,
    #[error("strict JSON failure: {0}")]
    StrictJson(#[from] StrictJsonError),
    #[error("I/O failure: {0}")]
    Io(#[from] std::io::Error),
}

impl MissionCorpus {
    fn calculated_digest(&self) -> Result<Digest, MissionCorpusError> {
        Ok(canonical_digest(&(
            &self.schema_version,
            &self.source_repository,
            &self.source_head,
            &self.status_domains,
            &self.source_files,
            &self.vectors,
        ))?)
    }

    fn validate(&self) -> Result<(), MissionCorpusError> {
        if self.schema_version != "ao.next.mission-equivalence-corpus.v1"
            || self.source_repository != "ao-mission"
            || !is_git_head(&self.source_head)
        {
            return invalid("unsupported corpus or source identity");
        }
        if self.status_domains.mission_durable_source != SOURCE_STATUS
            || self.status_domains.engine_projection != ENGINE_STATUS
            || self.status_domains.conflated
            || self.status_domains.mission_durable_source == self.status_domains.engine_projection
        {
            return invalid("Mission source status and Engine projection must remain distinct");
        }
        if self.source_files.is_empty() || self.source_files.len() > ENTRY_LIMIT {
            return invalid("source inventory is empty or oversized");
        }
        let mut previous = None;
        let mut total = 0_u64;
        for entry in &self.source_files {
            validate_relative_path(&entry.path)?;
            if !matches!(entry.mode.as_str(), "100644" | "100755")
                || entry.bytes > FILE_LIMIT
                || previous.is_some_and(|path: &str| path >= entry.path.as_str())
            {
                return invalid("source inventory entries are invalid or reordered");
            }
            previous = Some(&entry.path);
            total = total
                .checked_add(entry.bytes)
                .ok_or(MissionCorpusError::Oversized)?;
        }
        if total > TOTAL_LIMIT {
            return Err(MissionCorpusError::Oversized);
        }

        let source_entries = self
            .source_files
            .iter()
            .map(|entry| (entry.path.as_str(), (&entry.digest, entry.bytes)))
            .collect::<std::collections::BTreeMap<_, _>>();
        let mut ids = BTreeSet::new();
        let mut fixture_paths = Vec::new();
        let mut coverage = BTreeSet::new();
        let mut dispositions = BTreeSet::new();
        for vector in &self.vectors {
            validate_relative_path(&vector.source_path)?;
            validate_relative_path(&vector.fixture_path)?;
            if vector.id.trim().is_empty()
                || !ids.insert(vector.id.as_str())
                || vector.surfaces.is_empty()
                || vector.surfaces.windows(2).any(|pair| pair[0] >= pair[1])
                || vector.bytes > FILE_LIMIT
                || !vector.fixture_path.starts_with("vectors/")
                || source_entries.get(vector.source_path.as_str())
                    != Some(&(&vector.digest, vector.bytes))
            {
                return invalid("behavior vector is invalid");
            }
            fixture_paths.push(vector.fixture_path.as_str());
            coverage.extend(vector.surfaces.iter().copied());
            dispositions.insert(vector.expected_disposition);
        }
        if fixture_paths.windows(2).any(|pair| pair[0] >= pair[1])
            || coverage
                != BTreeSet::from([
                    BehaviorSurface::Commands,
                    BehaviorSurface::StrictContracts,
                    BehaviorSurface::LifecycleTransitions,
                    BehaviorSurface::Readbacks,
                    BehaviorSurface::ArchiveImport,
                    BehaviorSurface::PublicSafety,
                ])
            || dispositions
                != BTreeSet::from([ExpectedDisposition::Accepted, ExpectedDisposition::Rejected])
        {
            return invalid("behavior corpus coverage is incomplete or reordered");
        }
        let observed = self.calculated_digest()?;
        if observed != self.manifest_digest {
            return Err(MissionCorpusError::DigestDrift {
                expected: self.manifest_digest.clone(),
                observed,
            });
        }
        Ok(())
    }
}

/// Verifies the frozen corpus, its copied vectors, and the exact tracked Mission source tree.
///
/// # Errors
///
/// Fails closed on malformed or oversized JSON, unsafe filesystem inputs, status-domain
/// conflation, head drift, and any missing, extra, reordered, or digest-drifted entry.
pub fn verify_mission_corpus(
    manifest_path: &Path,
    corpus_root: &Path,
    source_root: &Path,
) -> Result<VerifiedMissionCorpus, MissionCorpusError> {
    require_directory(source_root)?;
    let manifest = load_snapshot(manifest_path, corpus_root)?;
    verify_source(&manifest, source_root)?;
    Ok(verified(&manifest))
}

/// Verifies the frozen manifest and copied vectors without requiring the old source checkout.
///
/// # Errors
///
/// Fails closed on malformed or oversized JSON, unsafe filesystem inputs, incomplete
/// coverage, status-domain conflation, and ordered manifest or vector digest drift.
pub fn verify_mission_corpus_snapshot(
    manifest_path: &Path,
    corpus_root: &Path,
) -> Result<VerifiedMissionCorpus, MissionCorpusError> {
    let manifest = load_snapshot(manifest_path, corpus_root)?;
    Ok(verified(&manifest))
}

fn load_snapshot(
    manifest_path: &Path,
    corpus_root: &Path,
) -> Result<MissionCorpus, MissionCorpusError> {
    require_directory(corpus_root)?;
    let manifest_bytes = read_regular_bounded(manifest_path, MANIFEST_LIMIT as u64)?;
    let manifest: MissionCorpus = decode_strict_json(&manifest_bytes, MANIFEST_LIMIT)?;
    manifest.validate()?;
    verify_vectors(&manifest, corpus_root)?;
    Ok(manifest)
}

fn verified(manifest: &MissionCorpus) -> VerifiedMissionCorpus {
    VerifiedMissionCorpus {
        source_head: manifest.source_head.clone(),
        source_file_count: manifest.source_files.len(),
        vector_count: manifest.vectors.len(),
        manifest_digest: manifest.manifest_digest.clone(),
    }
}

fn verify_vectors(manifest: &MissionCorpus, corpus_root: &Path) -> Result<(), MissionCorpusError> {
    let vector_root = corpus_root.join("vectors");
    require_directory(&vector_root)?;
    let actual = collect_files(corpus_root, &vector_root)?;
    let expected = manifest
        .vectors
        .iter()
        .map(|vector| vector.fixture_path.as_str())
        .collect::<Vec<_>>();
    if actual.iter().map(String::as_str).collect::<Vec<_>>() != expected {
        return Err(MissionCorpusError::VectorInventoryDrift);
    }
    for vector in &manifest.vectors {
        let bytes = read_regular_bounded(&corpus_root.join(&vector.fixture_path), FILE_LIMIT)?;
        if bytes.len() as u64 != vector.bytes || digest_bytes(&bytes) != vector.digest {
            return Err(MissionCorpusError::VectorInventoryDrift);
        }
    }
    Ok(())
}

fn verify_source(manifest: &MissionCorpus, source_root: &Path) -> Result<(), MissionCorpusError> {
    let head = git(source_root, &["rev-parse", "HEAD"])?;
    let observed_head = String::from_utf8(head)
        .map_err(|_| MissionCorpusError::Invalid("git head is not UTF-8".into()))?
        .trim()
        .to_owned();
    if observed_head != manifest.source_head {
        return Err(MissionCorpusError::SourceHeadDrift {
            expected: manifest.source_head.clone(),
            observed: observed_head,
        });
    }

    let tracked = git(source_root, &["ls-files", "-s", "-z"])?;
    let actual = parse_git_inventory(&tracked)?;
    let expected = manifest
        .source_files
        .iter()
        .map(|entry| (entry.mode.as_str(), entry.path.as_str()))
        .collect::<Vec<_>>();
    if actual != expected {
        return Err(MissionCorpusError::SourceInventoryDrift);
    }
    let mut total = 0_u64;
    for entry in &manifest.source_files {
        let bytes = read_regular_bounded(&source_root.join(&entry.path), FILE_LIMIT)?;
        total = total
            .checked_add(bytes.len() as u64)
            .ok_or(MissionCorpusError::Oversized)?;
        if bytes.len() as u64 != entry.bytes || digest_bytes(&bytes) != entry.digest {
            return Err(MissionCorpusError::SourceInventoryDrift);
        }
    }
    if total > TOTAL_LIMIT {
        return Err(MissionCorpusError::Oversized);
    }
    Ok(())
}

fn parse_git_inventory(bytes: &[u8]) -> Result<Vec<(&str, &str)>, MissionCorpusError> {
    if bytes.len() > MANIFEST_LIMIT || bytes.last() != Some(&0) {
        return invalid("git inventory is malformed or oversized");
    }
    bytes[..bytes.len() - 1]
        .split(|byte| *byte == 0)
        .map(|record| {
            let record = std::str::from_utf8(record)
                .map_err(|_| MissionCorpusError::Invalid("git inventory is not UTF-8".into()))?;
            let (metadata, path) = record.split_once('\t').ok_or_else(|| {
                MissionCorpusError::Invalid("git inventory entry is malformed".into())
            })?;
            let mut fields = metadata.split(' ');
            let mode = fields.next().unwrap_or_default();
            if !matches!(mode, "100644" | "100755") || fields.clone().count() != 2 {
                return invalid("git inventory contains an unsupported entry");
            }
            validate_relative_path(path)?;
            Ok((mode, path))
        })
        .collect()
}

fn git(root: &Path, args: &[&str]) -> Result<Vec<u8>, MissionCorpusError> {
    let output = Command::new("git")
        .arg("-C")
        .arg(root)
        .args(args)
        .output()?;
    if !output.status.success() {
        return invalid("git could not inspect the bound source tree");
    }
    Ok(output.stdout)
}

fn collect_files(root: &Path, directory: &Path) -> Result<Vec<String>, MissionCorpusError> {
    let mut pending = vec![directory.to_path_buf()];
    let mut files = Vec::new();
    while let Some(current) = pending.pop() {
        let mut entries = fs::read_dir(&current)?.collect::<Result<Vec<_>, _>>()?;
        entries.sort_by_key(fs::DirEntry::file_name);
        for entry in entries.into_iter().rev() {
            let path = entry.path();
            let metadata = fs::symlink_metadata(&path)?;
            if metadata.file_type().is_symlink() {
                return Err(MissionCorpusError::UnsafeFile(path.display().to_string()));
            }
            if metadata.is_dir() {
                pending.push(path);
            } else if metadata.is_file() {
                let relative = path
                    .strip_prefix(root)
                    .map_err(|_| MissionCorpusError::UnsafePath(path.display().to_string()))?
                    .to_string_lossy()
                    .replace('\\', "/");
                validate_relative_path(&relative)?;
                files.push(relative);
            } else {
                return Err(MissionCorpusError::UnsafeFile(path.display().to_string()));
            }
            if files.len() + pending.len() > ENTRY_LIMIT {
                return Err(MissionCorpusError::Oversized);
            }
        }
    }
    files.sort();
    Ok(files)
}

fn require_directory(path: &Path) -> Result<(), MissionCorpusError> {
    let metadata = fs::symlink_metadata(path)?;
    if metadata.file_type().is_symlink() || !metadata.is_dir() {
        return Err(MissionCorpusError::UnsafeFile(path.display().to_string()));
    }
    Ok(())
}

fn read_regular_bounded(path: &Path, limit: u64) -> Result<Vec<u8>, MissionCorpusError> {
    let metadata = fs::symlink_metadata(path)?;
    if metadata.file_type().is_symlink() || !metadata.is_file() {
        return Err(MissionCorpusError::UnsafeFile(path.display().to_string()));
    }
    if metadata.len() > limit {
        return Err(MissionCorpusError::Oversized);
    }
    Ok(fs::read(path)?)
}

fn validate_relative_path(path: &str) -> Result<(), MissionCorpusError> {
    if path.is_empty()
        || path.contains('\\')
        || Path::new(path)
            .components()
            .any(|component| !matches!(component, Component::Normal(_)))
    {
        return Err(MissionCorpusError::UnsafePath(path.into()));
    }
    Ok(())
}

fn is_git_head(value: &str) -> bool {
    value.len() == 40
        && value
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
}

fn invalid<T>(message: impl Into<String>) -> Result<T, MissionCorpusError> {
    Err(MissionCorpusError::Invalid(message.into()))
}
