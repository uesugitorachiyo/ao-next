use std::collections::{BTreeMap, BTreeSet};
use std::fs::{File, OpenOptions};
use std::io::Write;
use std::path::{Path, PathBuf};

use chrono::{DateTime, Utc};
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::adapter::EffectObservation;
use crate::contracts::{Digest, EffectRequest, RunRequest};
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
#[serde(deny_unknown_fields, rename_all = "snake_case", tag = "kind")]
pub enum JournalEventKind {
    ProviderRequestIntent {
        prepared_run_digest: Digest,
    },
    ProviderProcessStarted {
        invocation_digest: Digest,
    },
    ProviderOutputRetained {
        raw_capture_digest: Digest,
    },
    ProviderCaptureIndexPublished {
        index_digest: Digest,
    },
    ProviderCaptureVerified {
        index_digest: Digest,
    },
    AdapterTurnNormalized {
        turn_digest: Digest,
    },
    EffectIntent {
        effect_id: String,
        effect_digest: Digest,
    },
    EffectCommitted {
        effect_id: String,
    },
    EffectCompleted {
        observation: EffectObservation,
    },
    VerificationStarted {
        attempt: u32,
    },
    VerifierRecorded {
        report_digest: Digest,
    },
    TerminalPublished {
        record_digest: Digest,
    },
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

#[derive(Clone, Debug, Eq, PartialEq)]
pub enum JournalEffectState {
    Fresh,
    Unknown,
    Completed(EffectObservation),
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ProviderJournalState {
    pub prepared_run_digest: Option<Digest>,
    pub provider_process_started: bool,
    pub raw_capture_digest: Option<Digest>,
    pub capture_index_digest: Option<Digest>,
    pub capture_verified: bool,
    pub adapter_turn_digest: Option<Digest>,
}

impl ProviderJournalState {
    #[must_use]
    pub const fn outcome_unknown(&self) -> bool {
        self.prepared_run_digest.is_some() && self.raw_capture_digest.is_none()
    }
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
    #[error("journal effect identity or digest drifted")]
    EffectIdentityMismatch,
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

fn provider_state_from_events(
    events: &[JournalEvent],
) -> Result<ProviderJournalState, RecoveryError> {
    let mut state = ProviderJournalState {
        prepared_run_digest: None,
        provider_process_started: false,
        raw_capture_digest: None,
        capture_index_digest: None,
        capture_verified: false,
        adapter_turn_digest: None,
    };
    let mut effect_seen = false;
    let mut provider_step = 0_u8;
    for event in events {
        match &event.kind {
            JournalEventKind::ProviderRequestIntent {
                prepared_run_digest,
            } if !effect_seen && provider_step == 0 => {
                state.prepared_run_digest = Some(prepared_run_digest.clone());
                provider_step = 1;
            }
            JournalEventKind::ProviderProcessStarted { .. }
                if !effect_seen && provider_step == 1 =>
            {
                state.provider_process_started = true;
                provider_step = 2;
            }
            JournalEventKind::ProviderOutputRetained { raw_capture_digest }
                if !effect_seen && provider_step == 2 =>
            {
                state.raw_capture_digest = Some(raw_capture_digest.clone());
                provider_step = 3;
            }
            JournalEventKind::ProviderCaptureIndexPublished { index_digest }
                if !effect_seen && provider_step == 3 =>
            {
                state.capture_index_digest = Some(index_digest.clone());
                provider_step = 4;
            }
            JournalEventKind::ProviderCaptureVerified { index_digest }
                if !effect_seen
                    && provider_step == 4
                    && state.capture_index_digest.as_ref() == Some(index_digest) =>
            {
                state.capture_verified = true;
                provider_step = 5;
            }
            JournalEventKind::AdapterTurnNormalized { turn_digest }
                if !effect_seen && provider_step == 5 =>
            {
                state.adapter_turn_digest = Some(turn_digest.clone());
                provider_step = 6;
            }
            JournalEventKind::EffectIntent { .. }
            | JournalEventKind::EffectCommitted { .. }
            | JournalEventKind::EffectCompleted { .. } => {
                if provider_step != 0 && provider_step != 6 {
                    return Err(RecoveryError::EventSequenceInvalid);
                }
                effect_seen = true;
            }
            JournalEventKind::VerificationStarted { .. }
            | JournalEventKind::VerifierRecorded { .. }
            | JournalEventKind::TerminalPublished { .. }
                if provider_step == 0 || provider_step == 6 => {}
            JournalEventKind::ProviderRequestIntent { .. }
            | JournalEventKind::ProviderProcessStarted { .. }
            | JournalEventKind::ProviderOutputRetained { .. }
            | JournalEventKind::ProviderCaptureIndexPublished { .. }
            | JournalEventKind::ProviderCaptureVerified { .. }
            | JournalEventKind::AdapterTurnNormalized { .. }
            | JournalEventKind::VerificationStarted { .. }
            | JournalEventKind::VerifierRecorded { .. }
            | JournalEventKind::TerminalPublished { .. } => {
                return Err(RecoveryError::EventSequenceInvalid);
            }
        }
    }
    Ok(state)
}

fn require_provider_ready(state: &ProviderJournalState) -> Result<(), RecoveryError> {
    if state.prepared_run_digest.is_some() && state.adapter_turn_digest.is_none() {
        return Err(RecoveryError::EventSequenceInvalid);
    }
    Ok(())
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

    /// Binds the append-only execution journal to one exact run request before
    /// worker dispatch.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] when the identity path is unsafe, unreadable,
    /// oversized, malformed, or already bound to a different request.
    pub fn bind_request(&self, request: &RunRequest) -> Result<(), RecoveryError> {
        let identity = CheckpointIdentity::from_request(request)?;
        let bytes = canonical_json_bytes(&identity)?;
        if bytes.len() as u64 > self.maximum_bytes {
            return Err(RecoveryError::Oversized {
                actual: bytes.len() as u64,
                limit: self.maximum_bytes,
            });
        }
        let path = self.root.join("execution-identity.json");
        if path.exists() || path.is_symlink() {
            let existing = read_regular_file(&path, self.maximum_bytes)?;
            if existing != bytes {
                return Err(RecoveryError::IdentityMismatch);
            }
            let maximum_bytes = usize::try_from(self.maximum_bytes).unwrap_or(usize::MAX);
            let recorded: CheckpointIdentity = decode_strict_json(&existing, maximum_bytes)?;
            if recorded != identity {
                return Err(RecoveryError::IdentityMismatch);
            }
            return Ok(());
        }
        durable_create_new(&path, &bytes)
    }

    /// Returns the durable provider-capture lifecycle for one exact request.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] for identity drift, malformed or reordered
    /// events, unsafe paths, size violations, or I/O failure.
    pub fn provider_state(
        &self,
        request: &RunRequest,
    ) -> Result<ProviderJournalState, RecoveryError> {
        self.bind_request(request)?;
        provider_state_from_events(&self.load_execution_events()?)
    }

    /// Confirms that no provider attempt or effect is already durable.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] when starting could repeat an unknown provider
    /// outcome or place provider events after effects.
    pub fn provider_may_start(&self, request: &RunRequest) -> Result<(), RecoveryError> {
        self.bind_request(request)?;
        let events = self.load_execution_events()?;
        let state = provider_state_from_events(&events)?;
        if state.prepared_run_digest.is_some()
            || events.iter().any(|event| {
                matches!(
                    event.kind,
                    JournalEventKind::EffectIntent { .. }
                        | JournalEventKind::EffectCommitted { .. }
                        | JournalEventKind::EffectCompleted { .. }
                )
            })
        {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        Ok(())
    }

    /// Durably records provider request intent before process execution.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless no provider attempt or effect is
    /// already durable.
    pub fn record_provider_request_intent(
        &self,
        request: &RunRequest,
        prepared: &Digest,
    ) -> Result<(), RecoveryError> {
        self.provider_may_start(request)?;
        self.append_execution_event(JournalEventKind::ProviderRequestIntent {
            prepared_run_digest: prepared.clone(),
        })
    }

    /// Durably records that the provider process transition was reached.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless provider request intent is the exact
    /// current lifecycle state.
    pub fn record_provider_process_started(
        &self,
        request: &RunRequest,
        invocation: &Digest,
    ) -> Result<(), RecoveryError> {
        let state = self.provider_state(request)?;
        if state.prepared_run_digest.is_none() || state.provider_process_started {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        self.append_execution_event(JournalEventKind::ProviderProcessStarted {
            invocation_digest: invocation.clone(),
        })
    }

    /// Durably records the retained raw provider capture.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless provider process start is the exact
    /// current lifecycle state.
    pub fn record_provider_output_retained(
        &self,
        request: &RunRequest,
        raw: &Digest,
    ) -> Result<(), RecoveryError> {
        let state = self.provider_state(request)?;
        if !state.provider_process_started || state.raw_capture_digest.is_some() {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        self.append_execution_event(JournalEventKind::ProviderOutputRetained {
            raw_capture_digest: raw.clone(),
        })
    }

    /// Durably records the published provider capture index.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless retained provider output is the exact
    /// current lifecycle state.
    pub fn record_provider_capture_published(
        &self,
        request: &RunRequest,
        index: &Digest,
    ) -> Result<(), RecoveryError> {
        let state = self.provider_state(request)?;
        if state.raw_capture_digest.is_none() || state.capture_index_digest.is_some() {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        self.append_execution_event(JournalEventKind::ProviderCaptureIndexPublished {
            index_digest: index.clone(),
        })
    }

    /// Durably records verification of the published provider capture index.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless the same index digest was published
    /// exactly once.
    pub fn record_provider_capture_verified(
        &self,
        request: &RunRequest,
        index: &Digest,
    ) -> Result<(), RecoveryError> {
        let state = self.provider_state(request)?;
        if state.capture_index_digest.as_ref() != Some(index) || state.capture_verified {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        self.append_execution_event(JournalEventKind::ProviderCaptureVerified {
            index_digest: index.clone(),
        })
    }

    /// Durably records the trusted normalized adapter turn.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless provider capture verification is the
    /// exact current lifecycle state.
    pub fn record_adapter_turn_normalized(
        &self,
        request: &RunRequest,
        turn: &Digest,
    ) -> Result<(), RecoveryError> {
        let state = self.provider_state(request)?;
        if !state.capture_verified || state.adapter_turn_digest.is_some() {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        self.append_execution_event(JournalEventKind::AdapterTurnNormalized {
            turn_digest: turn.clone(),
        })
    }

    /// Returns the durable state of one exact effect without executing it.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] for run/effect identity drift, malformed or
    /// reordered events, unsafe paths, size violations, or I/O failure.
    pub fn effect_state(
        &self,
        request: &RunRequest,
        effect: &EffectRequest,
    ) -> Result<JournalEffectState, RecoveryError> {
        self.bind_request(request)?;
        let effect_digest = canonical_digest(effect)?;
        let mut effects = BTreeMap::<String, (Digest, Option<EffectObservation>)>::new();
        let mut terminal_published = false;
        let events = self.load_execution_events()?;
        let provider_state = provider_state_from_events(&events)?;
        require_provider_ready(&provider_state)?;
        for event in events {
            match event.kind {
                JournalEventKind::EffectIntent {
                    effect_id,
                    effect_digest,
                } => {
                    if terminal_published
                        || effects.insert(effect_id, (effect_digest, None)).is_some()
                    {
                        return Err(RecoveryError::EffectIdentityMismatch);
                    }
                }
                JournalEventKind::EffectCompleted { observation } => {
                    if terminal_published {
                        return Err(RecoveryError::EventSequenceInvalid);
                    }
                    let Some((_, completion)) = effects.get_mut(&observation.effect_id) else {
                        return Err(RecoveryError::EventSequenceInvalid);
                    };
                    if completion.replace(observation).is_some() {
                        return Err(RecoveryError::EventSequenceInvalid);
                    }
                }
                JournalEventKind::EffectCommitted { .. } => {
                    return Err(RecoveryError::EventSequenceInvalid);
                }
                JournalEventKind::VerificationStarted { .. }
                | JournalEventKind::VerifierRecorded { .. }
                | JournalEventKind::ProviderRequestIntent { .. }
                | JournalEventKind::ProviderProcessStarted { .. }
                | JournalEventKind::ProviderOutputRetained { .. }
                | JournalEventKind::ProviderCaptureIndexPublished { .. }
                | JournalEventKind::ProviderCaptureVerified { .. }
                | JournalEventKind::AdapterTurnNormalized { .. } => {}
                JournalEventKind::TerminalPublished { .. } => {
                    if terminal_published {
                        return Err(RecoveryError::EventSequenceInvalid);
                    }
                    terminal_published = true;
                }
            }
        }
        let Some((recorded_digest, completion)) = effects.get(&effect.effect_id) else {
            return Ok(JournalEffectState::Fresh);
        };
        if recorded_digest != &effect_digest {
            return Err(RecoveryError::EffectIdentityMismatch);
        }
        Ok(completion
            .clone()
            .map_or(JournalEffectState::Unknown, JournalEffectState::Completed))
    }

    /// Durably records exact effect intent before native execution.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless the effect is fresh and the next event
    /// can be created and synced without replacing prior journal bytes.
    pub fn record_effect_intent(
        &self,
        request: &RunRequest,
        effect: &EffectRequest,
    ) -> Result<(), RecoveryError> {
        if self.effect_state(request, effect)? != JournalEffectState::Fresh {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        self.append_execution_event(JournalEventKind::EffectIntent {
            effect_id: effect.effect_id.clone(),
            effect_digest: canonical_digest(effect)?,
        })
    }

    /// Durably records exact effect completion after native execution.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless matching intent is durable and the next
    /// event can be created and synced without replacing prior journal bytes.
    pub fn record_effect_completion(
        &self,
        request: &RunRequest,
        effect: &EffectRequest,
        observation: &EffectObservation,
    ) -> Result<(), RecoveryError> {
        if observation.effect_id != effect.effect_id
            || self.effect_state(request, effect)? != JournalEffectState::Unknown
        {
            return Err(RecoveryError::EffectIdentityMismatch);
        }
        self.append_execution_event(JournalEventKind::EffectCompleted {
            observation: observation.clone(),
        })
    }

    /// Durably marks verification as started, or resumes one unmatched start.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] for identity drift or contradictory journal
    /// sequencing.
    pub fn begin_verification(&self, request: &RunRequest) -> Result<(), RecoveryError> {
        self.bind_request(request)?;
        let events = self.load_execution_events()?;
        require_provider_ready(&provider_state_from_events(&events)?)?;
        let starts = events
            .iter()
            .filter(|event| matches!(event.kind, JournalEventKind::VerificationStarted { .. }))
            .count();
        let records = events
            .iter()
            .filter(|event| matches!(event.kind, JournalEventKind::VerifierRecorded { .. }))
            .count();
        if events
            .iter()
            .any(|event| matches!(event.kind, JournalEventKind::TerminalPublished { .. }))
            || starts < records
            || starts > records.saturating_add(1)
        {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        if starts == records.saturating_add(1) {
            return Ok(());
        }
        let attempt = u32::try_from(records).map_err(|_| RecoveryError::EventSequenceInvalid)?;
        self.append_execution_event(JournalEventKind::VerificationStarted { attempt })
    }

    /// Durably records the verifier report for one started attempt.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] unless exactly one verification start remains
    /// unmatched.
    pub fn record_verifier(
        &self,
        request: &RunRequest,
        report_digest: &Digest,
    ) -> Result<(), RecoveryError> {
        self.bind_request(request)?;
        let events = self.load_execution_events()?;
        require_provider_ready(&provider_state_from_events(&events)?)?;
        let starts = events
            .iter()
            .filter(|event| matches!(event.kind, JournalEventKind::VerificationStarted { .. }))
            .count();
        let records = events
            .iter()
            .filter(|event| matches!(event.kind, JournalEventKind::VerifierRecorded { .. }))
            .count();
        if starts != records.saturating_add(1) {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        self.append_execution_event(JournalEventKind::VerifierRecorded {
            report_digest: report_digest.clone(),
        })
    }

    /// Publishes canonical terminal bytes at a content-addressed create-only
    /// path and then appends their durable journal event.
    ///
    /// # Errors
    ///
    /// Returns [`RecoveryError`] when verification is incomplete, a terminal
    /// contradicts an existing terminal, or retained bytes/path integrity fail.
    pub fn publish_terminal_record(
        &self,
        request: &RunRequest,
        bytes: &[u8],
    ) -> Result<Digest, RecoveryError> {
        self.bind_request(request)?;
        if bytes.len() as u64 > self.maximum_bytes {
            return Err(RecoveryError::Oversized {
                actual: bytes.len() as u64,
                limit: self.maximum_bytes,
            });
        }
        let digest = digest_bytes(bytes);
        let events = self.load_execution_events()?;
        require_provider_ready(&provider_state_from_events(&events)?)?;
        let starts = events
            .iter()
            .filter(|event| matches!(event.kind, JournalEventKind::VerificationStarted { .. }))
            .count();
        let records = events
            .iter()
            .filter(|event| matches!(event.kind, JournalEventKind::VerifierRecorded { .. }))
            .count();
        let terminals = events
            .iter()
            .filter_map(|event| match &event.kind {
                JournalEventKind::TerminalPublished { record_digest } => Some(record_digest),
                _ => None,
            })
            .collect::<Vec<_>>();
        if terminals.len() > 1 {
            return Err(RecoveryError::EventSequenceInvalid);
        }
        let terminal = terminals.first().copied();
        if starts == 0 || starts != records {
            return Err(RecoveryError::VerifierEventMissing);
        }
        let digest_hex = digest
            .as_str()
            .strip_prefix("sha256:")
            .ok_or(RecoveryError::EventDigestMismatch)?;
        let path = self.root.join(format!("terminal-{digest_hex}.json"));
        if let Some(recorded) = terminal {
            if recorded != &digest || read_regular_file(&path, self.maximum_bytes)? != bytes {
                return Err(RecoveryError::EventDigestMismatch);
            }
            return Ok(digest);
        }
        if path.exists() || path.is_symlink() {
            if read_regular_file(&path, self.maximum_bytes)? != bytes {
                return Err(RecoveryError::EventDigestMismatch);
            }
        } else {
            durable_create_new(&path, bytes)?;
        }
        self.append_execution_event(JournalEventKind::TerminalPublished {
            record_digest: digest.clone(),
        })?;
        Ok(digest)
    }

    fn append_execution_event(&self, kind: JournalEventKind) -> Result<(), RecoveryError> {
        let events = self.load_execution_events()?;
        let event = JournalEvent {
            schema_version: "ao.next.journal-event.v1".into(),
            sequence: events.len() as u64,
            kind,
        };
        let bytes = canonical_json_bytes(&event)?;
        if bytes.len() as u64 > self.maximum_bytes {
            return Err(RecoveryError::Oversized {
                actual: bytes.len() as u64,
                limit: self.maximum_bytes,
            });
        }
        let directory = self.execution_event_directory()?;
        let digest = digest_bytes(&bytes);
        let digest_hex = digest
            .as_str()
            .strip_prefix("sha256:")
            .ok_or(RecoveryError::EventDigestMismatch)?;
        durable_create_new(
            &directory.join(format!("{:020}-{digest_hex}.json", event.sequence)),
            &bytes,
        )
    }

    fn load_execution_events(&self) -> Result<Vec<JournalEvent>, RecoveryError> {
        let directory = self.execution_event_directory()?;
        let mut paths = std::fs::read_dir(&directory)?
            .map(|entry| entry.map(|value| value.path()))
            .collect::<Result<Vec<_>, _>>()?;
        paths.sort();
        let mut events = Vec::with_capacity(paths.len());
        let mut total_bytes = 0_u64;
        for (expected_sequence, path) in paths.into_iter().enumerate() {
            let name = path
                .file_name()
                .and_then(|value| value.to_str())
                .ok_or_else(|| RecoveryError::UnsafePath(path.clone()))?;
            let name_bytes = name.as_bytes();
            let expected_prefix = format!("{expected_sequence:020}-");
            if name_bytes.len() != 90
                || !name_bytes.starts_with(expected_prefix.as_bytes())
                || &name_bytes[85..] != b".json"
            {
                return Err(RecoveryError::EventSequenceInvalid);
            }
            let metadata = std::fs::symlink_metadata(&path)?;
            if metadata.file_type().is_symlink() || !metadata.is_file() {
                return Err(RecoveryError::UnsafePath(path));
            }
            total_bytes = total_bytes.saturating_add(metadata.len());
            if total_bytes > self.maximum_bytes {
                return Err(RecoveryError::Oversized {
                    actual: total_bytes,
                    limit: self.maximum_bytes,
                });
            }
            let bytes = read_regular_file(&path, self.maximum_bytes)?;
            let observed_digest = digest_bytes(&bytes);
            let observed_hex = observed_digest
                .as_str()
                .strip_prefix("sha256:")
                .ok_or(RecoveryError::EventDigestMismatch)?;
            if &name_bytes[21..85] != observed_hex.as_bytes() {
                return Err(RecoveryError::EventDigestMismatch);
            }
            let maximum_bytes = usize::try_from(self.maximum_bytes).unwrap_or(usize::MAX);
            let event: JournalEvent = decode_strict_json(&bytes, maximum_bytes)?;
            if event.schema_version != "ao.next.journal-event.v1"
                || event.sequence != expected_sequence as u64
            {
                return Err(RecoveryError::EventSequenceInvalid);
            }
            if canonical_json_bytes(&event)? != bytes {
                return Err(RecoveryError::EventDigestMismatch);
            }
            events.push(event);
        }
        Ok(events)
    }

    fn execution_event_directory(&self) -> Result<PathBuf, RecoveryError> {
        let directory = self.root.join("execution-events");
        std::fs::create_dir_all(&directory)?;
        let metadata = std::fs::symlink_metadata(&directory)?;
        if metadata.file_type().is_symlink() || !metadata.is_dir() {
            return Err(RecoveryError::UnsafePath(directory));
        }
        Ok(directory)
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
        require_provider_ready(&provider_state_from_events(&events)?)?;
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
                JournalEventKind::EffectCompleted { observation } => {
                    committed.insert(observation.effect_id.clone());
                }
                JournalEventKind::EffectIntent { .. }
                | JournalEventKind::ProviderRequestIntent { .. }
                | JournalEventKind::ProviderProcessStarted { .. }
                | JournalEventKind::ProviderOutputRetained { .. }
                | JournalEventKind::ProviderCaptureIndexPublished { .. }
                | JournalEventKind::ProviderCaptureVerified { .. }
                | JournalEventKind::AdapterTurnNormalized { .. }
                | JournalEventKind::VerificationStarted { .. }
                | JournalEventKind::TerminalPublished { .. } => {}
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

fn durable_create_new(path: &Path, bytes: &[u8]) -> Result<(), RecoveryError> {
    let mut file = OpenOptions::new().write(true).create_new(true).open(path)?;
    file.write_all(bytes)?;
    file.sync_all()?;
    Ok(())
}
