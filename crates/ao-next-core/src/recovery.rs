use std::collections::BTreeSet;
use std::fs::File;
use std::io::Write;
use std::path::{Path, PathBuf};

use chrono::{DateTime, Utc};
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::contracts::{Digest, RunRequest};
use crate::evidence::{EvidenceError, digest_bytes, read_regular_file};
use crate::strict_json::{
    StrictJsonError, canonical_digest, canonical_json_bytes, decode_strict_json,
};

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct CheckpointIdentity {
    pub request_digest: Digest,
    pub source_digest: Digest,
    pub workspace_digest: Digest,
    pub policy_digest: Digest,
    pub model_profile_digest: Digest,
    pub verifier_profile_digest: Digest,
}

impl CheckpointIdentity {
    /// Derives every recovery identity from one exact run request.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] when a request component cannot be canonically
    /// serialized.
    pub fn from_request(request: &RunRequest) -> Result<Self, RecoveryError> {
        Ok(Self {
            request_digest: canonical_digest(request)?,
            source_digest: canonical_digest(&request.source)?,
            workspace_digest: canonical_digest(&request.workspace)?,
            policy_digest: request.policy_digest.clone(),
            model_profile_digest: canonical_digest(&request.model_profile)?,
            verifier_profile_digest: request.verifier_profile.profile_digest.clone(),
        })
    }
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(rename_all = "snake_case", tag = "kind")]
pub enum JournalEventKind {
    EffectCommitted { effect_id: String },
    VerifierRecorded { report_digest: Digest },
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct JournalEvent {
    pub schema_version: String,
    pub sequence: u64,
    pub kind: JournalEventKind,
}

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Checkpoint {
    pub schema_version: String,
    pub run_id: String,
    pub sequence: u64,
    pub identity: CheckpointIdentity,
    pub committed_effects: BTreeSet<String>,
    pub events_digest: Digest,
    pub recorded_at: DateTime<Utc>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ResumePlan {
    pub skipped_committed_effects: Vec<String>,
    pub remaining_effects: Vec<String>,
}

#[derive(Debug, Error)]
pub enum RecoveryError {
    #[error("recovery I/O failed: {0}")]
    Io(String),
    #[error("recovery artifact is unsafe: {0}")]
    UnsafePath(PathBuf),
    #[error("recovery artifact is oversized: {actual} bytes exceeds {limit}")]
    Oversized { actual: u64, limit: u64 },
    #[error("checkpoint event digest mismatch")]
    EventDigestMismatch,
    #[error("checkpoint digest mismatch: expected {expected}, observed {observed}")]
    CheckpointDigestMismatch { expected: Digest, observed: Digest },
    #[error("checkpoint identity mismatch")]
    IdentityMismatch,
    #[error("committed effect is absent from the durable event log: {0}")]
    CommittedEffectMissing(String),
    #[error("durable verifier event is missing")]
    VerifierEventMissing,
    #[error("journal event sequence is invalid")]
    EventSequenceInvalid,
    #[error("checkpoint schema is invalid")]
    InvalidSchema,
    #[error("strict JSON failure: {0}")]
    StrictJson(#[from] StrictJsonError),
}

impl From<std::io::Error> for RecoveryError {
    fn from(error: std::io::Error) -> Self {
        Self::Io(error.to_string())
    }
}

impl From<EvidenceError> for RecoveryError {
    fn from(error: EvidenceError) -> Self {
        Self::Io(error.to_string())
    }
}

#[derive(Clone, Debug)]
pub struct CheckpointJournal {
    root: PathBuf,
    maximum_bytes: u64,
}

impl CheckpointJournal {
    /// Creates a checkpoint journal beneath an existing regular directory.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] when the root cannot be created or is a symlink
    /// or non-directory entry.
    pub fn new(root: impl AsRef<Path>, maximum_bytes: u64) -> Result<Self, RecoveryError> {
        let root = root.as_ref().to_path_buf();
        std::fs::create_dir_all(&root)?;
        let metadata = std::fs::symlink_metadata(&root)?;
        if metadata.file_type().is_symlink() || !metadata.is_dir() {
            return Err(RecoveryError::UnsafePath(root));
        }
        Ok(Self {
            root,
            maximum_bytes,
        })
    }

    /// Commits a checkpoint only after its effect and verifier events are durable.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] for event digest drift, missing committed effect
    /// or verifier events, invalid sequence/schema, size violations, or I/O failure.
    pub fn commit(&self, checkpoint: &Checkpoint, event_log: &Path) -> Result<(), RecoveryError> {
        if checkpoint.schema_version != "ao.next.checkpoint.v1" {
            return Err(RecoveryError::InvalidSchema);
        }
        let event_bytes = read_regular_file(event_log, self.maximum_bytes)?;
        if digest_bytes(&event_bytes) != checkpoint.events_digest {
            return Err(RecoveryError::EventDigestMismatch);
        }
        let events = decode_event_log(&event_bytes, self.maximum_bytes)?;
        let mut committed = BTreeSet::new();
        let mut verifier_recorded = false;
        for (expected_sequence, event) in events.iter().enumerate() {
            if event.sequence != expected_sequence as u64
                || event.schema_version != "ao.next.journal-event.v1"
            {
                return Err(RecoveryError::EventSequenceInvalid);
            }
            match &event.kind {
                JournalEventKind::EffectCommitted { effect_id } => {
                    committed.insert(effect_id.clone());
                }
                JournalEventKind::VerifierRecorded { .. } => verifier_recorded = true,
            }
        }
        for effect_id in &checkpoint.committed_effects {
            if !committed.contains(effect_id) {
                return Err(RecoveryError::CommittedEffectMissing(effect_id.clone()));
            }
        }
        if !verifier_recorded {
            return Err(RecoveryError::VerifierEventMissing);
        }
        if checkpoint.sequence != events.len() as u64 {
            return Err(RecoveryError::EventSequenceInvalid);
        }

        let bytes = canonical_json_bytes(checkpoint)?;
        if bytes.len() as u64 > self.maximum_bytes {
            return Err(RecoveryError::Oversized {
                actual: bytes.len() as u64,
                limit: self.maximum_bytes,
            });
        }
        let digest = digest_bytes(&bytes);
        durable_replace(&self.root.join("checkpoint.json"), &bytes)?;
        durable_replace(
            &self.root.join("checkpoint.sha256"),
            digest.as_str().as_bytes(),
        )?;
        Ok(())
    }

    /// Loads a checkpoint and returns only effects not already committed.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] for digest or identity drift, unsafe/oversized
    /// checkpoint artifacts, strict JSON failure, or invalid schema.
    pub fn resume(
        &self,
        identity: &CheckpointIdentity,
        pending_effects: &[String],
    ) -> Result<ResumePlan, RecoveryError> {
        let checkpoint = self.load()?;
        if &checkpoint.identity != identity {
            return Err(RecoveryError::IdentityMismatch);
        }
        let mut skipped = Vec::new();
        let mut remaining = Vec::new();
        for effect_id in pending_effects {
            if checkpoint.committed_effects.contains(effect_id) {
                skipped.push(effect_id.clone());
            } else {
                remaining.push(effect_id.clone());
            }
        }
        Ok(ResumePlan {
            skipped_committed_effects: skipped,
            remaining_effects: remaining,
        })
    }

    fn load(&self) -> Result<Checkpoint, RecoveryError> {
        let digest_bytes_raw = read_regular_file(&self.root.join("checkpoint.sha256"), 128)?;
        let expected = Digest::new(
            std::str::from_utf8(&digest_bytes_raw)
                .map_err(|error| RecoveryError::Io(error.to_string()))?
                .trim(),
        )
        .map_err(|error| RecoveryError::Io(error.to_string()))?;
        let bytes = read_regular_file(&self.root.join("checkpoint.json"), self.maximum_bytes)?;
        let observed = digest_bytes(&bytes);
        if observed != expected {
            return Err(RecoveryError::CheckpointDigestMismatch { expected, observed });
        }
        let maximum_bytes = usize::try_from(self.maximum_bytes).unwrap_or(usize::MAX);
        let checkpoint: Checkpoint = decode_strict_json(&bytes, maximum_bytes)?;
        if checkpoint.schema_version != "ao.next.checkpoint.v1" {
            return Err(RecoveryError::InvalidSchema);
        }
        Ok(checkpoint)
    }
}

/// Writes an ordered canonical JSONL event log and returns its exact digest.
///
/// # Errors
///
/// Returns [`RecoveryError`] when the encoded log exceeds `maximum_bytes`, its
/// parent is unavailable, or durable writing fails.
pub fn write_durable_event_log(
    path: &Path,
    events: &[JournalEvent],
    maximum_bytes: u64,
) -> Result<Digest, RecoveryError> {
    let mut bytes = Vec::new();
    for event in events {
        bytes.extend_from_slice(&canonical_json_bytes(event)?);
        bytes.push(b'\n');
    }
    if bytes.len() as u64 > maximum_bytes {
        return Err(RecoveryError::Oversized {
            actual: bytes.len() as u64,
            limit: maximum_bytes,
        });
    }
    durable_replace(path, &bytes)?;
    Ok(digest_bytes(&bytes))
}

fn decode_event_log(bytes: &[u8], maximum_bytes: u64) -> Result<Vec<JournalEvent>, RecoveryError> {
    let mut events = Vec::new();
    for line in bytes.split(|byte| *byte == b'\n') {
        if line.is_empty() {
            continue;
        }
        let maximum_bytes = usize::try_from(maximum_bytes).unwrap_or(usize::MAX);
        events.push(decode_strict_json(line, maximum_bytes)?);
    }
    Ok(events)
}

fn durable_replace(path: &Path, bytes: &[u8]) -> Result<(), RecoveryError> {
    let file_name = path
        .file_name()
        .and_then(|value| value.to_str())
        .ok_or_else(|| RecoveryError::UnsafePath(path.to_path_buf()))?;
    let temporary = path.with_file_name(format!(".{file_name}.tmp"));
    {
        let mut file = File::create(&temporary)?;
        file.write_all(bytes)?;
        file.sync_all()?;
    }
    std::fs::rename(temporary, path)?;
    Ok(())
}
